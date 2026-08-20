package template

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// UndefinedMode selects what happens when a name does not resolve.
type UndefinedMode int

const (
	// Strict makes a reference to an undefined name an error naming the
	// file, the line, and the identifier. This is the default, and it is
	// the deliberate departure from Jinja described in SPEC section
	// 10.2.6: Salt's default renders an empty string and produces a state
	// that silently does the wrong thing.
	Strict UndefinedMode = iota
	// Permissive restores Salt's behaviour. Every resolution is reported
	// through Options.OnUndefined so it can be logged with its position.
	Permissive
)

// Options configure a render. The zero value is not usable; start from
// DefaultOptions.
type Options struct {
	Undefined UndefinedMode
	// OnUndefined is called for every permissive resolution, so that a
	// migration can run permissive, collect the warnings, fix them, and
	// then switch to strict.
	OnUndefined func(name string, pos Pos)

	Delimiters          Delimiters
	TrimBlocks          bool
	LstripBlocks        bool
	KeepTrailingNewline bool

	// The limits of SPEC section 10.2.8. Templates are attacker-adjacent
	// input, especially in a gitfs-backed tree.
	MaxOutput       int64
	MaxIterations   int64
	MaxDepth        int
	MaxIncludeDepth int
	Timeout         time.Duration

	// RandomSeed makes the random, shuffle, and rand_str filters
	// deterministic for a given node and job, so that a test=True run and
	// the real run that follows it agree. An empty seed with
	// Nondeterministic false still seeds from the template name, which is
	// stable; Nondeterministic restores Salt's behaviour.
	RandomSeed       string
	Nondeterministic bool
}

// DefaultOptions returns the limits and defaults SPEC section 10.2
// specifies.
func DefaultOptions() Options {
	return Options{
		Undefined:  Strict,
		Delimiters: DefaultDelimiters(),
		// Salt renders configuration files through this engine, so a
		// trailing newline is kept rather than swallowed.
		KeepTrailingNewline: true,
		MaxOutput:           64 << 20,
		MaxIterations:       10_000_000,
		MaxDepth:            100,
		MaxIncludeDepth:     25,
		Timeout:             60 * time.Second,
	}
}

// Loader resolves a template name reached through include, import, or
// extends. The file server supplies one on a node; a directory loader
// serves local development and the test suite.
type Loader interface {
	// Load returns the source of a template and the name to report in
	// diagnostics.
	Load(name string) (src string, displayName string, err error)
}

// ErrNotFound is what a Loader returns for a missing template, so that
// `{% include ... ignore missing %}` can tell absence from failure.
var ErrNotFound = fmt.Errorf("template not found")

// DirLoader loads templates from a directory tree, refusing any name that
// escapes the root.
type DirLoader struct{ Root string }

// Load implements Loader.
func (d DirLoader) Load(name string) (string, string, error) {
	clean := path.Clean("/" + strings.ReplaceAll(name, "\\", "/"))
	full := filepath.Join(d.Root, filepath.FromSlash(clean))
	root, err := filepath.Abs(d.Root)
	if err != nil {
		return "", "", err
	}
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", "", err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", "", fmt.Errorf("template %q resolves outside the template root", name)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return "", "", err
	}
	return string(b), name, nil
}

// Environment holds what is shared across the templates of one tree: the
// loader, the global names, the filter and test tables, and the options.
type Environment struct {
	Loader  Loader
	Globals map[string]any
	Opts    Options

	filters map[string]FilterFunc
	tests   map[string]TestFunc

	mu    sync.Mutex
	cache map[string]*Template
}

// NewEnvironment builds an environment with the standard filter and test
// tables installed.
func NewEnvironment(loader Loader, opts Options) *Environment {
	if opts.Delimiters.VarStart == "" {
		opts.Delimiters = DefaultDelimiters()
	}
	if opts.MaxOutput == 0 {
		d := DefaultOptions()
		opts.MaxOutput, opts.MaxIterations = d.MaxOutput, d.MaxIterations
		opts.MaxDepth, opts.MaxIncludeDepth = d.MaxDepth, d.MaxIncludeDepth
		opts.Timeout = d.Timeout
	}
	e := &Environment{
		Loader:  loader,
		Globals: map[string]any{},
		Opts:    opts,
		filters: standardFilters(),
		tests:   standardTests(),
		cache:   map[string]*Template{},
	}
	installGlobals(e)
	return e
}

// AddFilter registers or replaces a filter.
func (e *Environment) AddFilter(name string, fn FilterFunc) { e.filters[name] = fn }

// AddTest registers or replaces a test.
func (e *Environment) AddTest(name string, fn TestFunc) { e.tests[name] = fn }

// FilterNames lists every filter, for `lint` and for the migration report.
func (e *Environment) FilterNames() []string { return sortedKeysOf(e.filters) }

// TestNames lists every test.
func (e *Environment) TestNames() []string { return sortedKeysOf(e.tests) }

// Template is a parsed template.
type Template struct {
	Name string
	Src  string
	Body []Node
	env  *Environment
}

// Parse compiles a template source. Parsing is separate from rendering so
// that `lint` can check a tree without executing it.
func (e *Environment) Parse(src, name string) (*Template, error) {
	toks, err := lex(src, name, e.Opts.Delimiters, lexOptions{
		TrimBlocks:   e.Opts.TrimBlocks,
		LstripBlocks: e.Opts.LstripBlocks,
	})
	if err != nil {
		return nil, err
	}
	if !e.Opts.KeepTrailingNewline {
		trimTrailingNewline(toks)
	}
	body, err := parseTemplate(toks, name, e.Opts)
	if err != nil {
		return nil, err
	}
	return &Template{Name: name, Src: src, Body: body, env: e}, nil
}

func trimTrailingNewline(toks []token) {
	for i := len(toks) - 1; i >= 0; i-- {
		if toks[i].kind == tokText {
			toks[i].val = strings.TrimSuffix(toks[i].val, "\n")
			return
		}
		if toks[i].kind != tokEOF {
			return
		}
	}
}

// load fetches and parses a named template, caching the result.
func (e *Environment) load(name string) (*Template, error) {
	e.mu.Lock()
	if t, ok := e.cache[name]; ok {
		e.mu.Unlock()
		return t, nil
	}
	e.mu.Unlock()

	if e.Loader == nil {
		return nil, fmt.Errorf("no template loader is configured, so %q cannot be resolved", name)
	}
	src, display, err := e.Loader.Load(name)
	if err != nil {
		return nil, err
	}
	t, err := e.Parse(src, display)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.cache[name] = t
	e.mu.Unlock()
	return t, nil
}

// SourceMapEntry ties a line of rendered output back to the template
// position that produced it.
type SourceMapEntry struct {
	OutLine int
	Pos     Pos
}

// Result is a render's output and the source map that lets the YAML stage
// report an error at the line the operator wrote rather than at the line
// the renderer produced. SPEC section 10.1.4.
type Result struct {
	Output    string
	SourceMap []SourceMapEntry
}

// MapLine resolves a 1-based output line to its template position.
func (r Result) MapLine(outLine int) (Pos, bool) {
	var best Pos
	found := false
	for _, e := range r.SourceMap {
		if e.OutLine <= outLine {
			best, found = e.Pos, true
			continue
		}
		break
	}
	return best, found
}

// Render executes a template against a context.
func (t *Template) Render(ctx map[string]any) (Result, error) {
	b := &budget{
		deadline:      time.Now().Add(t.env.Opts.Timeout),
		maxIterations: t.env.Opts.MaxIterations,
		maxOutput:     t.env.Opts.MaxOutput,
	}
	r := newRenderer(t.env, b)
	r.scope = newScope(nil)
	for k, v := range t.env.Globals {
		r.scope.set(k, v)
	}
	for k, v := range ctx {
		r.scope.set(k, v)
	}
	r.seed(t.Name)

	if err := r.renderRoot(t); err != nil {
		return Result{}, err
	}
	return Result{Output: r.out.String(), SourceMap: r.srcMap}, nil
}

// RenderString is the one-shot form: parse and render in one call.
func (e *Environment) RenderString(src, name string, ctx map[string]any) (Result, error) {
	t, err := e.Parse(src, name)
	if err != nil {
		return Result{}, err
	}
	return t.Render(ctx)
}

// budget is the shared resource accounting for one render operation,
// including the macros and includes it reaches.
type budget struct {
	deadline      time.Time
	iterations    int64
	maxIterations int64
	output        int64
	maxOutput     int64
	checks        int
}

func (b *budget) tick(pos Pos) error {
	b.checks++
	// Checking the clock on every statement would dominate the run, so it
	// is sampled.
	if b.checks%512 == 0 && time.Now().After(b.deadline) {
		return errorf(pos, "template rendering exceeded its deadline")
	}
	return nil
}

func (b *budget) iterate(pos Pos) error {
	b.iterations++
	if b.iterations > b.maxIterations {
		return errorf(pos, "template exceeded the limit of %d loop iterations", b.maxIterations)
	}
	return nil
}

func (b *budget) write(n int, pos Pos) error {
	b.output += int64(n)
	if b.output > b.maxOutput {
		return errorf(pos, "template output exceeded the limit of %d bytes", b.maxOutput)
	}
	return nil
}

// seed derives the deterministic random source of SPEC section 10.2.4.
func (r *renderer) seed(name string) {
	if r.env.Opts.Nondeterministic {
		r.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
		return
	}
	h := sha256.Sum256([]byte(r.env.Opts.RandomSeed + "\x00" + name))
	r.rnd = rand.New(rand.NewSource(int64(binary.BigEndian.Uint64(h[:8]))))
}

func sortedKeysOf[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// installGlobals adds the callables Jinja exposes without an import.
func installGlobals(e *Environment) {
	e.Globals["range"] = funcValue{"range", func(args []any, _ map[string]any) (any, error) {
		var start, stop, step int64 = 0, 0, 1
		switch len(args) {
		case 1:
			stop, _ = asInt(args[0])
		case 2:
			start, _ = asInt(args[0])
			stop, _ = asInt(args[1])
		case 3:
			start, _ = asInt(args[0])
			stop, _ = asInt(args[1])
			step, _ = asInt(args[2])
		default:
			return nil, fmt.Errorf("range() takes one to three arguments")
		}
		if step == 0 {
			return nil, fmt.Errorf("range() step cannot be zero")
		}
		// The result is materialised, so it is bounded by the same limit
		// that bounds a loop.
		n := (stop - start + step - sign(step)) / step
		if n < 0 {
			n = 0
		}
		if n > e.Opts.MaxIterations {
			return nil, fmt.Errorf("range() would produce %d items, past the limit of %d", n, e.Opts.MaxIterations)
		}
		out := make([]any, 0, n)
		for i := start; (step > 0 && i < stop) || (step < 0 && i > stop); i += step {
			out = append(out, i)
		}
		return out, nil
	}}

	e.Globals["dict"] = funcValue{"dict", func(args []any, kwargs map[string]any) (any, error) {
		m := value.NewMap(len(kwargs))
		if len(args) == 1 {
			if src, ok := args[0].(*value.Map); ok {
				for _, en := range src.Entries() {
					m.Set(en.Key, en.Val)
				}
			}
		}
		for _, k := range sortedKeysOf(kwargs) {
			m.Set(k, kwargs[k])
		}
		return m, nil
	}}

	e.Globals["namespace"] = funcValue{"namespace", func(args []any, kwargs map[string]any) (any, error) {
		ns := newNamespace()
		if len(args) == 1 {
			if src, ok := args[0].(*value.Map); ok {
				for _, en := range src.Entries() {
					ns.m.Set(en.Key, en.Val)
				}
			}
		}
		for _, k := range sortedKeysOf(kwargs) {
			ns.m.Set(k, kwargs[k])
		}
		return ns, nil
	}}

	e.Globals["lipsum"] = funcValue{"lipsum", func([]any, map[string]any) (any, error) {
		return nil, fmt.Errorf("lipsum() is not supported")
	}}
}

func sign(n int64) int64 {
	if n < 0 {
		return -1
	}
	return 1
}
