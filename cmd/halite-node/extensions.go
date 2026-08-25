package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/bridge"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/value"
)

// openExtensions loads the signed extensions in the cache and registers
// what they provide.
//
// Failures are warnings rather than a stopped node. An extension that
// will not verify must not run — that is the whole point — but it must
// not stop the node either: a node that refuses to start because one
// extension's signature is wrong is a node that cannot be sent the
// highstate that would fix it.
func (n *node) openExtensions() {
	store := &extension.Store{
		Dir:     n.extensionDir(),
		Options: n.extensionLoadOptions(),
		Pins:    n.extensionPins(),
	}
	installed, err := store.Load()
	if err != nil {
		n.log.Warn("the extension cache could not be read",
			"dir", store.Dir, "error", err.Error())
		return
	}
	if len(installed) == 0 {
		return
	}
	usable, problems := store.Usable(installed)
	for _, problem := range problems {
		n.log.Warn("an extension was refused", "error", problem.Error())
	}

	n.extensions = &extension.Runtime{
		// Outside the cache, deliberately. The cache holds bundles that
		// are verified on every load, and a writable directory inside
		// one is both a file the manifest does not list — which
		// verification refuses — and, one level up, a directory the
		// store reads as a version of the extension.
		WorkDirFor: func(name string) string {
			return filepath.Join(n.extensionWorkDir(), name)
		},
		RunAs:      n.cfg.String("extension_user", ""),
		RunAsGroup: n.cfg.String("extension_group", ""),
		Timeout:    n.cfg.Duration("extension_timeout", 60*time.Second),
		PoolSize:   int(n.cfg.Int("extension_pool_size", 4)),
		Log: func(name, level, message string) {
			if level == "warn" || level == "error" {
				n.log.Warn("extension", "extension", name, "message", message)
				return
			}
			n.log.Info("extension", "extension", name, "message", message)
		},
		Event: func(name, tag string, data json.RawMessage) {
			if n.events == nil {
				return
			}
			decoded, err := value.DecodeJSON(data)
			if err != nil {
				return
			}
			payload := map[string]any{"extension": name, "data": decoded}
			// Namespaced under the extension, so an event an extension
			// fires cannot be mistaken for one the node itself did.
			_ = n.events.Send("extension/"+name+"/"+strings.TrimPrefix(tag, "/"), payload)
		},
	}

	for _, bundle := range usable {
		if err := n.extensions.Add(bundle); err != nil {
			n.log.Warn("an extension was refused",
				"extension", bundle.Manifest.Name, "error", err.Error())
			continue
		}
		n.log.Info("extension loaded",
			"extension", bundle.Manifest.Name, "version", bundle.Manifest.Version,
			"kind", bundle.Manifest.Kind, "signed_by", bundle.SignedBy,
			"unsigned", bundle.Unsigned)
		if bundle.Unsigned {
			// SPEC 24.4 asks for this on every load, and it is on every
			// load rather than once at startup because an operator
			// reading a single line at boot will not see it again.
			n.log.Warn("this extension is unsigned and was loaded anyway",
				"extension", bundle.Manifest.Name,
				"setting", "extension_require_signature")
		}
	}
	n.registerExtensionFunctions()
}

// registerExtensionFunctions puts each `module` extension's functions
// into the execution registry.
//
// A name that collides with a built-in is refused rather than
// overriding it: Salt lets a synced module shadow a shipped one, which
// means a file on the file server can change what `service.running`
// does. An extension that wants a different `service.running` gives it
// a different name.
func (n *node) registerExtensionFunctions() {
	for _, name := range n.extensions.Names() {
		loaded, ok := n.extensions.Get(name)
		if !ok || loaded.Bundle.Manifest.Kind != "module" {
			continue
		}
		// The signatures come from the handshake, so the process has to
		// have run once. That is the cost of an extension declaring
		// what it provides rather than the manifest claiming it.
		if err := n.warmExtension(loaded); err != nil {
			n.log.Warn("an extension could not be started",
				"extension", name, "error", err.Error())
			continue
		}
		for _, sig := range loaded.Functions {
			if _, exists := n.registry.Exec.Signatures().Lookup(sig.Name()); exists {
				n.log.Warn("an extension function is refused because a built-in has that name",
					"extension", name, "function", sig.Name())
				continue
			}
			n.registry.Exec.Add(exec.Module{Sig: sig, Fn: extensionCaller(loaded, sig.Name())})
			n.log.Info("extension function registered",
				"extension", name, "function", sig.Name())
		}
	}
}

// extensionCaller adapts one extension function to the execution
// registry.
func extensionCaller(loaded *extension.Loaded, name string) exec.Func {
	return func(c *exec.Context, args *value.Map) (any, error) {
		kwargs := map[string]any{}
		for _, e := range args.Entries() {
			kwargs[value.KeyString(e.Key)] = e.Val
		}
		_, function, _ := strings.Cut(name, ".")
		raw, err := loaded.Call(c.Ctx, function, nil, kwargs, callContextFor(c))
		if err != nil {
			return nil, err
		}
		return value.DecodeJSON(raw)
	}
}

func (n *node) extensionDir() string {
	if dir := n.cfg.String("extension_dir", ""); dir != "" {
		return dir
	}
	return filepath.Join(n.cfg.String("state_dir", config.DefaultStateDir), "ext")
}

// extensionWorkDir is where extensions may write, which is never inside
// the cache they are verified from.
func (n *node) extensionWorkDir() string {
	return filepath.Join(n.cfg.String("state_dir", config.DefaultStateDir), "ext-work")
}

func (n *node) extensionLoadOptions() extension.LoadOptions {
	var keys []extension.TrustKey
	for _, line := range n.cfg.StringSlice("extension_trust_keys") {
		key, err := extension.ParseTrustKey(line)
		if err != nil {
			// Fatal: a trust key that will not parse means the node is
			// trusting fewer keys than the operator wrote, and finding
			// that out from a refused extension is finding out too
			// late.
			cli.Fatalf("extension_trust_keys: %v", err)
		}
		keys = append(keys, key)
	}
	return extension.LoadOptions{
		TrustKeys:        keys,
		RequireSignature: n.cfg.Bool("extension_require_signature", true),
	}
}

// extensionPins reads `extension_pins`, a mapping of name to version
// and root.
func (n *node) extensionPins() map[string]extension.Pin {
	raw, ok := n.cfg.Get("extension_pins")
	if !ok || raw == nil {
		return nil
	}
	m, isMap := raw.(*value.Map)
	if !isMap {
		cli.Fatalf("`extension_pins` is a mapping of extension to version and root, not %s",
			value.TypeName(raw))
	}
	out := map[string]extension.Pin{}
	for _, e := range m.Entries() {
		name := value.KeyString(e.Key)
		switch v := e.Val.(type) {
		case string:
			out[name] = extension.Pin{Version: v}
		case *value.Map:
			pin := extension.Pin{}
			if version, ok := v.Get("version"); ok {
				pin.Version = value.KeyString(version)
			}
			if root, ok := v.Get("root"); ok {
				pin.Root = value.KeyString(root)
			}
			out[name] = pin
		default:
			cli.Fatalf("`extension_pins`: %s pins to %s, and a pin is a version or a mapping "+
				"of version and root", name, value.TypeName(e.Val))
		}
	}
	return out
}

// warmExtension starts one process so the handshake happens and the
// extension's signatures are known.
func (n *node) warmExtension(loaded *extension.Loaded) error {
	ctx, cancel := context.WithTimeout(context.Background(),
		n.cfg.Duration("extension_timeout", 60*time.Second))
	defer cancel()
	_, err := loaded.Call(ctx, "", nil, nil, nil)
	if err != nil && strings.Contains(err.Error(), "no function") {
		// The probe reached the extension and it answered that it has
		// no function by that name, which is the answer that proves the
		// handshake happened. Anything else is a real failure.
		return nil
	}
	return err
}

// callContextFor is what an extension is told about the run.
func callContextFor(c *exec.Context) *bridge.CallContext {
	return &bridge.CallContext{
		NodeID: c.NodeID, JobID: c.JobID, Env: c.Env, Test: c.Test,
	}
}

// describeExtensions is what `sys.list_extensions` answers with.
func (n *node) describeExtensions() []map[string]any {
	if n.extensions == nil {
		return nil
	}
	var out []map[string]any
	for _, name := range n.extensions.Names() {
		if loaded, ok := n.extensions.Get(name); ok {
			out = append(out, loaded.Describe())
		}
	}
	return out
}

// syncExtensions fetches the bundles this node is entitled to.
//
// SPEC 24.5: it fetches and does not load. What is running does not
// change until the node next starts, and the answer says which bundles
// arrived so an operator knows a restart will pick something up.
func (n *node) syncExtensions(kinds []string) (any, error) {
	source, err := n.extensionSource()
	if err != nil {
		return nil, err
	}
	syncer := &extension.Syncer{
		Source:  source,
		Dir:     n.extensionDir(),
		Options: n.extensionLoadOptions(),
		Pins:    n.extensionPins(),
		Kinds:   kinds,
	}
	report, err := syncer.Sync()
	if err != nil {
		return nil, err
	}

	changes := make([]any, 0, len(report.Changes))
	for _, change := range report.Changes {
		entry := value.NewMap(5)
		entry.Set("name", change.Name)
		entry.Set("version", change.Version)
		entry.Set("status", change.Status)
		if change.Reason != "" {
			entry.Set("reason", change.Reason)
		}
		if change.Root != "" {
			entry.Set("root", change.Root)
		}
		changes = append(changes, entry)
		if change.Status == "refused" {
			n.log.Warn("an extension bundle was refused",
				"extension", change.Name, "version", change.Version, "reason", change.Reason)
		}
	}
	out := value.NewMap(3)
	out.Set("extensions", changes)
	out.Set("changed", report.Changed())
	// Said plainly, because the difference from Salt's `sync_all` is
	// the point of the section and an operator who assumes the old
	// meaning will wonder why nothing happened.
	if report.Changed() {
		out.Set("note", "fetched, not loaded: restart the node to run the new bundles")
	}
	return out, nil
}

// extensionSource is the hub's file server, which is the only place
// SPEC 24.4 delivers bundles from.
func (n *node) extensionSource() (extension.Source, error) {
	remote, ok := n.files.(*fileserver.Remote)
	if !ok {
		return nil, errNoExtensionSource
	}
	return &remoteExtensionSource{remote: remote, env: n.env}, nil
}

var errNoExtensionSource = errString(
	"extensions are delivered by the hub's file server, and this node is working from its own tree")

// remoteExtensionSource adapts the hub's file server to what
// synchronization needs.
type remoteExtensionSource struct {
	remote *fileserver.Remote
	env    string
}

func (s *remoteExtensionSource) List(prefix string) ([]extension.SourceFile, error) {
	entries, err := s.remote.ListPrefix(s.env, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]extension.SourceFile, 0, len(entries))
	for _, e := range entries {
		out = append(out, extension.SourceFile{Path: e.Path, Digest: e.Hash})
	}
	return out, nil
}

func (s *remoteExtensionSource) Fetch(path string) ([]byte, error) {
	return s.remote.Read(s.env, path)
}
