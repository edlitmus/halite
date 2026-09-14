package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/value"
)

// The hub's half of SPEC 24.
//
// The node has had this since the extension model landed; the hub has
// not, and the eight `extension_*` settings it already accepted did
// nothing on it. That mattered once external pillar existed: pillar
// compiles on the hub, so a `pillar`-kind extension has nowhere else to
// run.
//
// Same store, same verification, same pins as the node, because an
// extension that is trusted differently depending on which program
// loads it is an extension whose trust nobody can state. What differs is
// where it comes from: a node fetches `_ext/` from the hub, and the hub
// reads its own file roots.

// openExtensions loads the signed extensions in the hub's cache.
//
// Failures are warnings rather than a stopped hub, with one exception
// the caller enforces: an `ext_pillar` entry naming an extension that
// did not load is fatal, because a hub that serves pillar without the
// source that holds the secrets is the partial pillar SPEC 12.7 refuses.
func (h *hubContext) openExtensions() *extension.Runtime {
	store := &extension.Store{
		Dir:     h.extensionDir(),
		Options: h.extensionLoadOptions(),
		Pins:    h.extensionPins(),
	}
	installed, err := store.Load()
	if err != nil {
		h.log.Warn("the extension cache could not be read",
			"dir", store.Dir, "error", err.Error())
		return &extension.Runtime{}
	}
	usable, problems := store.Usable(installed)
	for _, problem := range problems {
		h.log.Warn("an extension was refused", "error", problem.Error())
	}

	runtime := &extension.Runtime{
		// Outside the cache, deliberately. The cache holds bundles
		// verified on every load, and a writable directory inside one
		// is a file the manifest does not list.
		WorkDirFor: func(name string) string {
			return filepath.Join(h.extensionWorkDir(), name)
		},
		RunAs:      h.cfg.String("extension_user", ""),
		RunAsGroup: h.cfg.String("extension_group", ""),
		Timeout:    h.cfg.Duration("extension_timeout", 60*time.Second),
		PoolSize:   int(h.cfg.Int("extension_pool_size", 4)),
		Log: func(name, level, message string) {
			if level == "warn" || level == "error" {
				h.log.Warn("extension", "extension", name, "message", message)
				return
			}
			h.log.Info("extension", "extension", name, "message", message)
		},
		Event: func(name, tag string, data json.RawMessage) {
			h.log.Info("extension event", "extension", name, "tag", tag, "data", string(data))
		},
	}
	for _, bundle := range usable {
		if err := runtime.Add(bundle); err != nil {
			h.log.Warn("an extension could not be registered",
				"extension", bundle.Manifest.Name, "error", err.Error())
			continue
		}
		h.log.Info("extension loaded",
			"extension", bundle.Manifest.Name,
			"version", bundle.Manifest.Version,
			"kind", bundle.Manifest.Kind)
	}
	return runtime
}

// warmExtension starts one process so the handshake happens and the
// extension's declared signatures are known.
//
// Done at startup rather than at the first pillar request: a bundle
// whose executable will not run on this platform is a thing to learn
// when the hub starts, not when the first node asks for its secrets.
func (h *hubContext) warmExtension(loaded *extension.Loaded) error {
	ctx, cancel := context.WithTimeout(context.Background(),
		h.cfg.Duration("extension_timeout", 60*time.Second))
	defer cancel()
	return loaded.Start(ctx)
}

// extensionDir is the verified bundle cache.
func (h *hubContext) extensionDir() string {
	if dir := h.cfg.String("extension_dir", ""); dir != "" {
		return dir
	}
	return filepath.Join(h.cfg.String("state_dir", config.DefaultStateDir), "ext")
}

// extensionWorkDir is where extensions may write, which is never inside
// the cache they are verified from.
func (h *hubContext) extensionWorkDir() string {
	return filepath.Join(h.cfg.String("state_dir", config.DefaultStateDir), "ext-work")
}

func (h *hubContext) extensionLoadOptions() extension.LoadOptions {
	var keys []extension.TrustKey
	for _, line := range h.cfg.StringSlice("extension_trust_keys") {
		key, err := extension.ParseTrustKey(line)
		if err != nil {
			// Fatal: a trust key that will not parse means the hub is
			// trusting fewer keys than the operator wrote, and finding
			// that out from a refused extension is finding out too late.
			cli.Fatalf("extension_trust_keys: %v", err)
		}
		keys = append(keys, key)
	}
	return extension.LoadOptions{
		TrustKeys:        keys,
		RequireSignature: h.cfg.Bool("extension_require_signature", true),
	}
}

// extensionPins reads `extension_pins`, a mapping of name to version
// and root.
func (h *hubContext) extensionPins() map[string]extension.Pin {
	raw, ok := h.cfg.Get("extension_pins")
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

// rootsSource serves `_ext/` out of the hub's own file roots.
//
// A node reaches the file server over the wire; the hub is the file
// server, so it reads the roots directly. The bundle is verified and
// pinned exactly as a node's is — being the source of a file is not a
// reason to trust it, and a hub whose tree has been written to by
// something else is precisely the case the signature is for.
type rootsSource struct {
	roots *fileserver.Roots
	env   string
}

func (s *rootsSource) List(prefix string) ([]extension.SourceFile, error) {
	paths, err := s.roots.List(s.env)
	if err != nil {
		return nil, err
	}
	var out []extension.SourceFile
	for _, p := range paths {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		// The digest only decides whether a file the cache already
		// holds needs fetching again; the signature is what decides
		// whether it may run. Reading the file to hash it is what the
		// wire protocol's manifest saves a node, and a hub reading its
		// own disk does not need saving.
		_, digest, err := s.roots.Read(s.env, p)
		if err != nil {
			return nil, err
		}
		out = append(out, extension.SourceFile{Path: p, Digest: digest})
	}
	return out, nil
}

func (s *rootsSource) Fetch(path string) ([]byte, error) {
	body, _, err := s.roots.Read(s.env, path)
	return body, err
}

// syncExtensions fetches what the tree offers into the cache.
//
// SPEC 24.5's distinction holds here as on a node: this fetches and
// does not load. What the hub runs does not change until it is
// restarted, so an extension published into the tree cannot change a
// running hub's behaviour.
func (h *hubContext) syncExtensions(roots *fileserver.Roots, env string) (extension.Report, error) {
	if roots == nil {
		return extension.Report{}, nil
	}
	// Made here rather than assumed. `make install` creates state_dir,
	// but a hub can reasonably be told to fetch its extensions before
	// it has ever served -- and the failure without this is a staging
	// error naming a directory two levels above the one the operator
	// configured.
	dir := h.extensionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return extension.Report{}, err
	}
	syncer := &extension.Syncer{
		Source:  &rootsSource{roots: roots, env: env},
		Dir:     dir,
		Options: h.extensionLoadOptions(),
		Pins:    h.extensionPins(),
	}
	return syncer.Sync()
}
