package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/bridge"
	"github.com/edlitmus/halite/internal/signature"
)

// Runtime holds the extensions a node has loaded and running.
//
// One pool per extension, started on first use rather than at load: a
// node with twenty extensions configured and two in use should not be
// running twenty processes.
type Runtime struct {
	// WorkDirFor gives an extension its working directory, which under
	// the sandbox is the only place it may write.
	WorkDirFor func(name string) string
	// RunAs and RunAsGroup are the account an extension that did not
	// declare `root` drops to.
	RunAs      string
	RunAsGroup string
	// Timeout bounds one call.
	Timeout time.Duration
	// PoolSize is how many processes one extension may have.
	PoolSize int
	// Log receives what an extension says.
	Log func(name, level, message string)
	// Event forwards an extension's event frames onto the node's bus.
	Event func(name, tag string, data json.RawMessage)

	mu     sync.Mutex
	loaded map[string]*Loaded
	order  []string
}

// Loaded is one extension the runtime knows about.
type Loaded struct {
	Bundle *Bundle
	// Functions are what it declared at handshake, once a process has
	// run. Empty until then.
	Functions []signature.Signature
	// Sandbox is what its processes are confined by.
	Sandbox *bridge.Sandbox

	pool     *bridge.Pool
	poolOnce sync.Once
	poolErr  error
	rt       *Runtime
	name     string
}

// Add registers a verified bundle.
//
// Nothing is started. The declarations in the manifest are what the
// sandbox is built from, and they are signed — an extension that asked
// for the network at handshake but not in its manifest is asking for
// something nobody signed off on.
func (r *Runtime) Add(bundle *Bundle) error {
	name := bundle.Manifest.Name
	sandbox, err := bridge.From(bundle.Manifest.Declares, r.RunAs, r.RunAsGroup)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded == nil {
		r.loaded = map[string]*Loaded{}
	}
	if _, seen := r.loaded[name]; seen {
		return fmt.Errorf("%s is loaded twice", name)
	}
	r.loaded[name] = &Loaded{Bundle: bundle, Sandbox: sandbox, rt: r, name: name}
	r.order = append(r.order, name)
	sort.Strings(r.order)
	return nil
}

// Get answers with a loaded extension.
func (r *Runtime) Get(name string) (*Loaded, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.loaded[name]
	return l, ok
}

// Names is every loaded extension, in order.
func (r *Runtime) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

// Close stops every process.
func (r *Runtime) Close() {
	r.mu.Lock()
	loaded := make([]*Loaded, 0, len(r.loaded))
	for _, l := range r.loaded {
		loaded = append(loaded, l)
	}
	r.mu.Unlock()
	for _, l := range loaded {
		if l.pool != nil {
			l.pool.Close()
		}
	}
}

// Start brings the extension up and completes the handshake, so its
// declared functions are known without calling one.
//
// The first cut probed with an empty function name and read the
// "no such function" answer as success. That worked until an extension
// parsed its arguments before looking at the function name, and then
// failed with "unexpected end of JSON input" — a message about nothing
// the operator had done.
func (l *Loaded) Start(ctx context.Context) error {
	_, err := l.ensure(ctx)
	return err
}

// Call invokes a function in an extension.
func (l *Loaded) Call(ctx context.Context, function string, args, kwargs any, callCtx *bridge.CallContext) (json.RawMessage, error) {
	pool, err := l.ensure(ctx)
	if err != nil {
		return nil, err
	}
	return pool.Call(ctx, function, args, kwargs, callCtx)
}

// Describe says what this extension is and what confines it, for
// `sys.list_extensions`.
func (l *Loaded) Describe() map[string]any {
	m := l.Bundle.Manifest
	out := map[string]any{
		"name":        m.Name,
		"version":     m.Version,
		"kind":        m.Kind,
		"description": m.Description,
		"declares":    m.Declares,
		"sandbox":     l.Sandbox.Describe(),
		"signed_by":   l.Bundle.SignedBy,
		"unsigned":    l.Bundle.Unsigned,
		"directory":   l.Bundle.Dir,
	}
	if root, err := l.Bundle.Root(); err == nil {
		// The value an operator copies into a pin.
		out["root"] = root
	}
	if fns := l.functionNames(); len(fns) > 0 {
		out["functions"] = fns
	}
	return out
}

func (l *Loaded) functionNames() []string {
	l.rt.mu.Lock()
	defer l.rt.mu.Unlock()
	out := make([]string, 0, len(l.Functions))
	for _, sig := range l.Functions {
		out = append(out, sig.Name())
	}
	sort.Strings(out)
	return out
}

// ensure starts the pool on first use.
func (l *Loaded) ensure(ctx context.Context) (*bridge.Pool, error) {
	l.poolOnce.Do(func() {
		exe, err := l.Bundle.Executable(Platform(runtime.GOOS, runtime.GOARCH))
		if err != nil {
			l.poolErr = err
			return
		}
		name := l.name
		// The working directory has to exist before the process starts.
		// Without this the failure is `fork/exec <executable>: no such
		// file or directory` — which names the executable, exists, and
		// sends whoever reads it to entirely the wrong place.
		if dir := l.rt.workDir(name); dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				l.poolErr = fmt.Errorf("the working directory for %s: %w", name, err)
				return
			}
		}
		opts := bridge.Options{
			Path:    exe,
			Kind:    l.Bundle.Manifest.Kind,
			WorkDir: l.rt.workDir(name),
			// The whole environment, never the host's.
			Env:     []string{"PATH=/usr/bin:/bin", "HOME=" + l.rt.workDir(name)},
			Timeout: l.rt.timeout(),
			Sandbox: l.Sandbox,
			OnLog: func(level, message string) {
				if l.rt.Log != nil {
					l.rt.Log(name, level, message)
				}
			},
			OnEvent: func(tag string, data json.RawMessage) {
				if l.rt.Event != nil {
					l.rt.Event(name, tag, data)
				}
			},
			Stderr: func(line string) {
				if l.rt.Log != nil {
					l.rt.Log(name, "warn", line)
				}
			},
		}
		pool := bridge.NewPool(opts, l.rt.PoolSize)
		if err := pool.Warm(ctx); err != nil {
			pool.Close()
			l.poolErr = err
			return
		}
		l.pool = pool
		l.readSignatures()
	})
	if l.poolErr != nil {
		return nil, l.poolErr
	}
	return l.pool, nil
}

// readSignatures records what the extension declared at handshake.
//
// A function whose signature will not parse is dropped rather than
// making the whole extension unusable: an extension that ships one
// malformed signature among twenty should lose the one.
func (l *Loaded) readSignatures() {
	info, known := l.pool.Info()
	if !known {
		return
	}
	var sigs []signature.Signature
	for _, raw := range info.Functions {
		sig, err := parseSignature(raw)
		if err != nil {
			if l.rt.Log != nil {
				l.rt.Log(l.name, "warn", "a function signature would not parse: "+err.Error())
			}
			continue
		}
		sigs = append(sigs, sig)
	}
	l.rt.mu.Lock()
	l.Functions = sigs
	l.rt.mu.Unlock()
}

// parseSignature reads the section 15.6 shape an extension sends.
func parseSignature(raw json.RawMessage) (signature.Signature, error) {
	var wire struct {
		Module     string   `json:"module"`
		Function   string   `json:"function"`
		Doc        string   `json:"doc"`
		Mutates    bool     `json:"mutates"`
		Platforms  []string `json:"platforms"`
		Privileges []string `json:"privileges"`
		Params     []struct {
			Name     string `json:"name"`
			Type     string `json:"type"`
			Required bool   `json:"required"`
			Doc      string `json:"doc"`
			Default  any    `json:"default"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return signature.Signature{}, err
	}
	if wire.Module == "" || wire.Function == "" {
		return signature.Signature{}, fmt.Errorf("a signature with no module or function")
	}
	sig := signature.Signature{
		Module: wire.Module, Function: wire.Function, Doc: wire.Doc,
		Mutates: wire.Mutates, Platforms: wire.Platforms, Privileges: wire.Privileges,
		// An extension cannot be taken at its word about test-mode
		// honesty — the host has no way to check it — so it is
		// recorded as unreliable. That makes a bare extension call a
		// compilation warning as a prereq target, which is the
		// conservative reading and the right one.
		TestMode: signature.TestUnreliable,
		// An extension is arbitrary code by construction, so a wildcard
		// in the RBAC policy never grants one: the role has to name it.
		// SPEC 23.5's rule, applied to the thing it most obviously
		// covers.
		ArbitraryCode: true,
	}
	for _, p := range wire.Params {
		sig.Params = append(sig.Params, signature.Param{
			Name: p.Name, Type: namedType(p.Type),
			Required: p.Required, Doc: p.Doc, Default: p.Default,
		})
	}
	return sig, nil
}

func (r *Runtime) workDir(name string) string {
	if r.WorkDirFor == nil {
		return ""
	}
	return r.WorkDirFor(name)
}

func (r *Runtime) timeout() time.Duration {
	if r.Timeout <= 0 {
		return bridge.DefaultTimeout
	}
	return r.Timeout
}

// namedType reads a parameter type an extension declared.
//
// An unrecognised name becomes `any` rather than an error: an extension
// written against a later build of this protocol should lose the
// validation on one parameter, not the whole function.
func namedType(name string) signature.Type {
	switch name {
	case "string":
		return signature.String
	case "int":
		return signature.Int
	case "float":
		return signature.Float
	case "bool":
		return signature.Bool
	case "list":
		return signature.List
	case "map":
		return signature.Map
	case "path":
		return signature.Path
	case "mode":
		return signature.Mode
	case "duration":
		return signature.Duration
	}
	return signature.Any
}
