package rendersandbox

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// The protocol between the privileged parent and the render child.
//
// Length-prefixed JSON, for the reason the bridge protocol gives: a
// frame boundary must not depend on nothing in the payload ever holding
// a newline. It is deliberately *not* the bridge's frame type. That
// protocol carries an extension's vocabulary and is versioned against
// third-party implementations; this one is spoken only by this binary
// talking to itself, and coupling them would mean a change made for an
// extension could break the renderer.
//
// One render is in flight at a time. The parent writes a `render` frame
// and then reads frames until a `result` arrives, answering the
// callbacks that come back in the meantime: a template `include` has to
// be resolved by the parent, because the child cannot reach the hub's
// file server, and `salt['pkg.version']` has to be dispatched by the
// parent, because module execution is the privilege the child does not
// have. SPEC 25.4 puts it plainly: the sandbox returns data, never a
// callable.

// maxFrame bounds a single frame. A rendered highstate SLS is tens of
// kilobytes; the limit is generous against that and still refuses a
// child that claims a gigabyte.
const maxFrame = 64 << 20

const (
	// frameHello is the child's first word: it names the protocol it
	// speaks, so a parent talking to a binary that is not its own twin
	// finds out before it sends a pillar.
	frameHello  = "hello"
	frameRender = "render"
	frameResult = "result"
	// frameLoad asks the parent to resolve a template by name.
	frameLoad = "load"
	// frameCall asks the parent to dispatch an execution module, and
	// frameHas asks whether a name is dispatchable at all.
	frameCall = "call"
	frameHas  = "has"
	// frameUndefined reports a permissive resolution. It is the one
	// frame with no reply: OnUndefined returns nothing, and waiting for
	// an answer nobody has to give would only add a way to deadlock.
	frameUndefined = "undefined"
	// frameReply answers load, call and has.
	frameReply = "reply"
)

// frame is one message in either direction.
type frame struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	// Version is the protocol the child speaks, on the hello frame.
	Version int `json:"version,omitempty"`

	// Files is this frame's position table. Every wirePos in the frame
	// indexes it.
	Files []string `json:"files,omitempty"`

	// render.
	Body   string       `json:"body,omitempty"`
	Stages []string     `json:"stages,omitempty"`
	Opts   *wireOptions `json:"opts,omitempty"`

	// load, call, has, undefined.
	Name   string      `json:"name,omitempty"`
	Args   []wireNode  `json:"args,omitempty"`
	Kwargs []wireEntry `json:"kwargs,omitempty"`
	Pos    *wirePos    `json:"pos,omitempty"`

	// reply, result.
	OK bool `json:"ok,omitempty"`
	// NotFound distinguishes a template that is absent from one that
	// failed, because `{% include ... ignore missing %}` turns on
	// exactly that difference.
	NotFound bool      `json:"not_found,omitempty"`
	Text     string    `json:"text,omitempty"`
	Display  string    `json:"display,omitempty"`
	Value    *wireNode `json:"value,omitempty"`
	Error    string    `json:"error,omitempty"`

	// result.
	Result *wireResult `json:"result,omitempty"`
}

// wireResult is render.Result across the boundary.
type wireResult struct {
	Value    *wireNode        `json:"value,omitempty"`
	Text     string           `json:"text,omitempty"`
	Pipeline []string         `json:"pipeline,omitempty"`
	Warnings []render.Warning `json:"warnings,omitempty"`
}

// wireOptions is render.Options minus everything that is a function or a
// privilege.
//
// Four fields are deliberately absent. Salt, Loader and OnUndefined are
// callbacks and travel as frames instead. GPG is not here at all: the
// gpg stage runs in the parent, so the child needs neither the keyring
// nor the settings that find it, and OnSecret -- which only the gpg
// walk calls -- has nothing to report from in here. That is the whole
// reason the pipeline is split rather than shipped whole.
type wireOptions struct {
	File      string `json:"file,omitempty"`
	SLS       string `json:"sls,omitempty"`
	Env       string `json:"env,omitempty"`
	PillarEnv string `json:"pillar_env,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	JobID     string `json:"job_id,omitempty"`

	Grains *wireNode   `json:"grains,omitempty"`
	Pillar *wireNode   `json:"pillar,omitempty"`
	Config *wireNode   `json:"config,omitempty"`
	Extra  []wireEntry `json:"extra,omitempty"`

	Undefined        int                  `json:"undefined,omitempty"`
	YAMLBool11       *bool                `json:"yaml_bool_11,omitempty"`
	Nondeterministic bool                 `json:"nondeterministic,omitempty"`
	Template         *wireTemplateOptions `json:"template,omitempty"`
	Renderer         []string             `json:"renderer,omitempty"`
	// HasSalt tells the child whether the parent can dispatch at all, so
	// that `salt['x.y'] is defined` answers the same in here as it would
	// out there when no dispatcher was set.
	HasSalt bool `json:"has_salt,omitempty"`
	// HasLoader likewise for template include and import.
	HasLoader bool `json:"has_loader,omitempty"`
}

// wireTemplateOptions mirrors template.Options without its callback.
type wireTemplateOptions struct {
	Undefined           int                 `json:"undefined,omitempty"`
	Delimiters          template.Delimiters `json:"delimiters"`
	TrimBlocks          bool                `json:"trim_blocks,omitempty"`
	LstripBlocks        bool                `json:"lstrip_blocks,omitempty"`
	KeepTrailingNewline bool                `json:"keep_trailing_newline,omitempty"`
	MaxOutput           int64               `json:"max_output,omitempty"`
	MaxIterations       int64               `json:"max_iterations,omitempty"`
	MaxDepth            int                 `json:"max_depth,omitempty"`
	MaxIncludeDepth     int                 `json:"max_include_depth,omitempty"`
	TimeoutNanos        int64               `json:"timeout_nanos,omitempty"`
	RandomSeed          string              `json:"random_seed,omitempty"`
	Nondeterministic    bool                `json:"nondeterministic,omitempty"`
}

func encodeTemplateOptions(o *template.Options) *wireTemplateOptions {
	if o == nil {
		return nil
	}
	return &wireTemplateOptions{
		Undefined:           int(o.Undefined),
		Delimiters:          o.Delimiters,
		TrimBlocks:          o.TrimBlocks,
		LstripBlocks:        o.LstripBlocks,
		KeepTrailingNewline: o.KeepTrailingNewline,
		MaxOutput:           o.MaxOutput,
		MaxIterations:       o.MaxIterations,
		MaxDepth:            o.MaxDepth,
		MaxIncludeDepth:     o.MaxIncludeDepth,
		TimeoutNanos:        int64(o.Timeout),
		RandomSeed:          o.RandomSeed,
		Nondeterministic:    o.Nondeterministic,
	}
}

func decodeTemplateOptions(o *wireTemplateOptions) *template.Options {
	if o == nil {
		return nil
	}
	out := template.Options{
		Undefined:           template.UndefinedMode(o.Undefined),
		Delimiters:          o.Delimiters,
		TrimBlocks:          o.TrimBlocks,
		LstripBlocks:        o.LstripBlocks,
		KeepTrailingNewline: o.KeepTrailingNewline,
		MaxOutput:           o.MaxOutput,
		MaxIterations:       o.MaxIterations,
		MaxDepth:            o.MaxDepth,
		MaxIncludeDepth:     o.MaxIncludeDepth,
		RandomSeed:          o.RandomSeed,
		Nondeterministic:    o.Nondeterministic,
	}
	out.Timeout = timeoutOf(o.TimeoutNanos)
	return &out
}

// encodeOptions converts the render options, filling the frame's file
// table as it goes.
func encodeOptions(opts render.Options, f *files) (*wireOptions, error) {
	out := &wireOptions{
		File:             opts.File,
		SLS:              opts.SLS,
		Env:              opts.Env,
		PillarEnv:        opts.PillarEnv,
		NodeID:           opts.NodeID,
		JobID:            opts.JobID,
		Undefined:        int(opts.Undefined),
		YAMLBool11:       opts.YAMLBool11,
		Nondeterministic: opts.Nondeterministic,
		Template:         encodeTemplateOptions(opts.TemplateOptions),
		Renderer:         opts.Renderer,
		HasSalt:          opts.Salt != nil,
		HasLoader:        opts.Loader != nil,
	}
	var err error
	if out.Grains, err = encodeMap(opts.Grains, f); err != nil {
		return nil, fmt.Errorf("grains: %w", err)
	}
	if out.Pillar, err = encodeMap(opts.Pillar, f); err != nil {
		return nil, fmt.Errorf("pillar: %w", err)
	}
	if out.Config, err = encodeMap(opts.Config, f); err != nil {
		return nil, fmt.Errorf("the configuration values: %w", err)
	}
	// Extra is a Go map, so its order is not stable. The keys are sorted
	// so that two renders of one file send identical bytes, which is
	// what makes a wire capture comparable between runs.
	for _, k := range sortedKeys(opts.Extra) {
		node, err := encodeValue(opts.Extra[k], f)
		if err != nil {
			return nil, fmt.Errorf("the template variable %q: %w", k, err)
		}
		out.Extra = append(out.Extra, wireEntry{K: wireNode{K: kindString, S: k}, V: node})
	}
	return out, nil
}

// decodeOptions rebuilds the options in the child. The callbacks are
// supplied by the caller, because they are frames rather than data.
func decodeOptions(w *wireOptions, f *files) (render.Options, error) {
	if w == nil {
		return render.Options{}, fmt.Errorf("the render request carries no options")
	}
	out := render.Options{
		File:             w.File,
		SLS:              w.SLS,
		Env:              w.Env,
		PillarEnv:        w.PillarEnv,
		NodeID:           w.NodeID,
		JobID:            w.JobID,
		Undefined:        template.UndefinedMode(w.Undefined),
		YAMLBool11:       w.YAMLBool11,
		Nondeterministic: w.Nondeterministic,
		TemplateOptions:  decodeTemplateOptions(w.Template),
		Renderer:         w.Renderer,
	}
	var err error
	if out.Grains, err = decodeMap(w.Grains, f); err != nil {
		return render.Options{}, err
	}
	if out.Pillar, err = decodeMap(w.Pillar, f); err != nil {
		return render.Options{}, err
	}
	if out.Config, err = decodeMap(w.Config, f); err != nil {
		return render.Options{}, err
	}
	if len(w.Extra) > 0 {
		out.Extra = make(map[string]any, len(w.Extra))
		for _, e := range w.Extra {
			key, err := decodeValue(e.K, f)
			if err != nil {
				return render.Options{}, err
			}
			name, ok := key.(string)
			if !ok {
				return render.Options{}, fmt.Errorf("a template variable is named by a %T", key)
			}
			v, err := decodeValue(e.V, f)
			if err != nil {
				return render.Options{}, err
			}
			out.Extra[name] = v
		}
	}
	return out, nil
}

func encodeArgs(args []any, f *files) ([]wireNode, error) {
	out := make([]wireNode, len(args))
	for i, a := range args {
		n, err := encodeValue(a, f)
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}

func decodeArgs(args []wireNode, f *files) ([]any, error) {
	out := make([]any, len(args))
	for i, a := range args {
		v, err := decodeValue(a, f)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func encodeKwargs(kwargs map[string]any, f *files) ([]wireEntry, error) {
	var out []wireEntry
	for _, k := range sortedKeys(kwargs) {
		n, err := encodeValue(kwargs[k], f)
		if err != nil {
			return nil, err
		}
		out = append(out, wireEntry{K: wireNode{K: kindString, S: k}, V: n})
	}
	return out, nil
}

func decodeKwargs(entries []wireEntry, f *files) (map[string]any, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(entries))
	for _, e := range entries {
		key, err := decodeValue(e.K, f)
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("a keyword argument is named by a %T", key)
		}
		v, err := decodeValue(e.V, f)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

// writeFrame writes one length-prefixed frame.
func writeFrame(w io.Writer, f frame) error {
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(body) > maxFrame {
		return fmt.Errorf("a %d byte render frame, past the %d byte limit", len(body), maxFrame)
	}
	header := [4]byte{
		byte(len(body) >> 24), byte(len(body) >> 16),
		byte(len(body) >> 8), byte(len(body)),
	}
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// readFrame reads one.
func readFrame(r io.Reader) (frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return frame{}, err
	}
	size := int(header[0])<<24 | int(header[1])<<16 | int(header[2])<<8 | int(header[3])
	if size < 0 || size > maxFrame {
		// Refused before anything is allocated for it.
		return frame{}, fmt.Errorf("a frame announced as %d bytes, past the %d byte limit", size, maxFrame)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return frame{}, err
	}
	var f frame
	if err := json.Unmarshal(body, &f); err != nil {
		return frame{}, fmt.Errorf("a frame that is not JSON: %w", err)
	}
	return f, nil
}

// restoreFiles rebuilds a frame's position table for decoding.
func restoreFiles(names []string) *files {
	f := newFiles()
	for _, n := range names {
		f.id(n)
	}
	return f
}

// posOf and unposOf carry a template position, which is a different type
// from a value position and travels the same way.
func posOf(p template.Pos, f *files) *wirePos {
	if p.File == "" && p.Line == 0 && p.Col == 0 {
		return nil
	}
	return f.pos(value.Pos{File: p.File, Line: p.Line, Col: p.Col})
}

func unposOf(p *wirePos, f *files) (template.Pos, error) {
	v, err := f.unpos(p)
	if err != nil {
		return template.Pos{}, err
	}
	return template.Pos{File: v.File, Line: v.Line, Col: v.Col}, nil
}
