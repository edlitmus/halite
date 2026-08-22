// Package render runs an SLS file through its renderer pipeline: a
// template stage followed by a serializer stage, named by a shebang-style
// first line and defaulting to `#!jinja|yaml`.
//
// The reason this is a package rather than two calls at each call site is
// the source map. A YAML error in a heavily templated SLS file must report
// the position in the .sls file the operator wrote, not the position in
// the rendered output. Salt reports the rendered position or nothing at
// all, and diagnosing a templated highstate failure is consequently a
// well-known misery. Fixing it is a stated goal of SPEC section 10.1.4.
package render

import (
	"fmt"
	"path"
	"strings"

	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
	"github.com/edlitmus/halite/internal/yaml"
)

// Support states whether a renderer ships, is bridged, or requires
// migration.
type Support int

const (
	// Supported means the stage is compiled in.
	Supported Support = iota
	// Bridged means the stage runs in a separate process. SPEC section 24.
	Bridged
	// Unsupported means a tree using it must migrate. SPEC section 28.4.
	Unsupported
)

// StageInfo describes one renderer name.
type StageInfo struct {
	Name    string
	Support Support
	// Kind is "template" or "serializer".
	Kind string
	Note string
}

// Stages is the renderer table of SPEC section 10. It backs `lint`, the
// migration report, and the pipeline itself, so that all three agree
// about what is supported.
var Stages = map[string]StageInfo{
	"jinja":     {"jinja", Supported, "template", "the default template stage"},
	"yaml":      {"yaml", Supported, "serializer", "the default serializer stage"},
	"json":      {"json", Supported, "serializer", ""},
	"text":      {"text", Supported, "serializer", "passthrough, no parsing"},
	"gpg":       {"gpg", Supported, "data", "decrypts PGP values through the system gpg binary; SPEC section 12.6"},
	"crypt":     {"crypt", Bridged, "data", "native encrypted pillar; SPEC section 12.5"},
	"exec":      {"exec", Bridged, "template", "delegates to a bridged process; SPEC section 10.3"},
	"mako":      {"mako", Unsupported, "template", "rare in practice; rewrite as jinja"},
	"wempy":     {"wempy", Unsupported, "template", "effectively unused"},
	"genshi":    {"genshi", Unsupported, "template", "effectively unused"},
	"cheetah":   {"cheetah", Unsupported, "template", "effectively unused"},
	"py":        {"py", Unsupported, "serializer", "requires Python; SPEC section 28.4"},
	"pydsl":     {"pydsl", Unsupported, "serializer", "requires Python; SPEC section 28.4"},
	"pyobjects": {"pyobjects", Unsupported, "serializer", "requires Python; SPEC section 28.4"},
	"yamlex":    {"yamlex", Unsupported, "serializer", "Salt-specific; use the state aggregate option"},
	"stateconf": {"stateconf", Unsupported, "serializer", "rare; use include and extend"},
}

// DefaultPipeline is what an SLS file without a shebang line gets.
var DefaultPipeline = []string{"jinja", "yaml"}

// Options configure one render.
type Options struct {
	// File is the path for diagnostics.
	File string
	// SLS is the dotted SLS name, exposed to templates as `sls`.
	SLS string
	// Env is the environment name, exposed as both `env` and `saltenv`.
	Env string
	// PillarEnv is exposed as `pillarenv`.
	PillarEnv string
	// NodeID is exposed as `id`.
	NodeID string
	// JobID seeds the deterministic random source together with NodeID.
	JobID string

	Grains *value.Map
	Pillar *value.Map
	// Config is the effective configuration with secrets redacted,
	// exposed as `opts`.
	Config *value.Map

	// Salt is the execution module dispatcher bound to `salt`. On the hub
	// this is the restricted set of SPEC section 25.5.
	Salt template.Dispatcher
	// Loader resolves include, import, and extends.
	Loader template.Loader

	// Undefined selects strict or permissive name resolution.
	Undefined template.UndefinedMode
	// OnUndefined reports each permissive resolution.
	OnUndefined func(name string, pos template.Pos)

	// YAMLBool11 enables YAML 1.1's extra boolean spellings. Default true.
	YAMLBool11 *bool
	// AllowDuplicateKeys downgrades a duplicate mapping key to a warning.
	AllowDuplicateKeys bool
	// Nondeterministic restores Salt's unseeded random behaviour.
	Nondeterministic bool

	// GPG configures the gpg renderer of SPEC section 12.6.
	GPG GPGOptions

	// Extra adds names to the template context, which orchestration and
	// reactor rendering use for `data` and `tag`.
	Extra map[string]any

	// TemplateOptions overrides the engine limits. Zero means defaults.
	TemplateOptions *template.Options
}

// Result is what a render produced.
type Result struct {
	// Value is the parsed data, for a serializer stage.
	Value any
	// Text is the rendered text before the serializer stage ran.
	Text string
	// Pipeline is the stage list that was used.
	Pipeline []string
	// Warnings are lint-level findings that did not stop the render.
	Warnings []Warning
}

// Warning is a diagnostic that did not stop the render.
type Warning struct {
	File string
	Line int
	Col  int
	Msg  string
}

func (w Warning) String() string {
	if w.Line == 0 {
		return fmt.Sprintf("%s: %s", w.File, w.Msg)
	}
	return fmt.Sprintf("%s:%d:%d: %s", w.File, w.Line, w.Col, w.Msg)
}

// ParsePipeline reads the shebang-style first line and returns the stage
// list and the body with that line removed. A file without one gets the
// default pipeline.
func ParsePipeline(src string) ([]string, string) {
	if !strings.HasPrefix(src, "#!") {
		return DefaultPipeline, src
	}
	line := src
	rest := ""
	if i := strings.IndexByte(src, '\n'); i >= 0 {
		line, rest = src[:i], src[i+1:]
	}
	spec := strings.TrimSpace(line[2:])
	if spec == "" {
		return DefaultPipeline, rest
	}
	var stages []string
	for _, part := range strings.Split(spec, "|") {
		if p := strings.TrimSpace(part); p != "" {
			stages = append(stages, p)
		}
	}
	if len(stages) == 0 {
		return DefaultPipeline, rest
	}
	// The body keeps a blank first line so that every subsequent line
	// number still matches the file on disk. A diagnostic that is off by
	// one because of the shebang is exactly the kind of paper cut this
	// package exists to remove.
	return stages, "\n" + rest
}

// checkStages refuses a pipeline this build cannot run, whatever the
// position of the offending stage.
//
// Position matters as much as membership. A template stage transforms
// text and a serializer turns text into data, so a template named after
// a serializer has nothing to work on; a data stage transforms parsed
// data, so one named before a serializer has nothing either. Salt's
// `#!yaml|gpg` is the second kind and is the ordinary spelling for an
// encrypted pillar file.
func checkStages(stages []string, file string) error {
	serialized := false
	for _, stage := range stages {
		info, known := Stages[stage]
		if !known {
			return fmt.Errorf("%s: unknown renderer %q; the supported set is in SPEC section 10", file, stage)
		}
		switch info.Support {
		case Unsupported:
			return fmt.Errorf("%s: the %s renderer is not supported: %s", file, stage, info.Note)
		case Bridged:
			return fmt.Errorf("%s: the %s renderer runs as a bridged extension, which is not available in this build: %s", file, stage, info.Note)
		}
		switch info.Kind {
		case "template":
			if serialized {
				return fmt.Errorf("%s: the %s renderer transforms text and is named after a serializer, which leaves it nothing to transform", file, stage)
			}
		case "serializer":
			if serialized {
				return fmt.Errorf("%s: the pipeline names two serializers; only the first would have anything to parse", file)
			}
			serialized = true
		case "data":
			if !serialized {
				return fmt.Errorf("%s: the %s renderer transforms parsed data and is named before any serializer, which leaves it nothing to transform", file, stage)
			}
		}
	}
	return nil
}

// Render runs a source through its pipeline.
func Render(src []byte, opts Options) (Result, error) {
	stages, body := ParsePipeline(string(src))
	res := Result{Pipeline: stages}

	// Every stage is checked before any of them runs. A serializer ends
	// the pipeline by returning, so a stage named after one used to be
	// dropped without a word: `#!yaml|gpg` ran yaml, never reached the
	// gpg check, and delivered a pillar whose encrypted values were
	// still ciphertext. Silently rendering a file as something other
	// than what its first line asks for is the worst available outcome.
	if err := checkStages(stages, opts.File); err != nil {
		return res, err
	}

	current := body
	var rendered *template.Result
	serialized := false

	for _, stage := range stages {
		switch stage {
		case "jinja":
			out, err := renderJinja(current, opts, &res)
			if err != nil {
				return res, err
			}
			rendered = out
			current = out.Output

		case "yaml":
			res.Text = current
			v, err := parseYAML(current, opts, rendered, &res)
			if err != nil {
				return res, err
			}
			res.Value, serialized = v, true

		case "json":
			res.Text = current
			v, err := parseJSON(current, opts)
			if err != nil {
				return res, err
			}
			res.Value, serialized = v, true

		case "text":
			res.Text = current
			res.Value, serialized = current, true

		case "gpg":
			v, err := decryptGPG(res.Value, opts)
			if err != nil {
				return res, fmt.Errorf("%s: %w", opts.File, err)
			}
			res.Value = v

		default:
			return res, fmt.Errorf("%s: renderer %q has no implementation in this build", opts.File, stage)
		}
	}

	if serialized {
		return res, nil
	}
	res.Text = current
	res.Value = current
	return res, nil
}

func renderJinja(src string, opts Options, res *Result) (*template.Result, error) {
	topts := template.DefaultOptions()
	if opts.TemplateOptions != nil {
		topts = *opts.TemplateOptions
	}
	topts.Undefined = opts.Undefined
	topts.Nondeterministic = opts.Nondeterministic
	topts.RandomSeed = opts.NodeID + "\x00" + opts.JobID

	collected := []Warning{}
	topts.OnUndefined = func(name string, pos template.Pos) {
		collected = append(collected, Warning{
			File: pos.File, Line: pos.Line, Col: pos.Col,
			Msg: fmt.Sprintf("%s is undefined and resolved to the empty string under permissive mode", name),
		})
		if opts.OnUndefined != nil {
			opts.OnUndefined(name, pos)
		}
	}

	env := template.NewEnvironment(opts.Loader, topts)
	tpl, err := env.Parse(src, opts.File)
	if err != nil {
		return nil, err
	}
	out, err := tpl.Render(buildContext(opts))
	res.Warnings = append(res.Warnings, collected...)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// buildContext assembles the names of SPEC section 10.2.7.
func buildContext(opts Options) map[string]any {
	grains := opts.Grains
	if grains == nil {
		grains = value.NewMap(0)
	}
	pillar := opts.Pillar
	if pillar == nil {
		pillar = value.NewMap(0)
	}
	config := opts.Config
	if config == nil {
		config = value.NewMap(0)
	}

	slsPath := strings.ReplaceAll(opts.SLS, ".", "/")
	tplDir := path.Dir(opts.File)
	if tplDir == "." {
		tplDir = ""
	}

	ctx := map[string]any{
		"grains":    grains,
		"pillar":    pillar,
		"opts":      config,
		"env":       opts.Env,
		"saltenv":   opts.Env,
		"pillarenv": opts.PillarEnv,
		"sls":       opts.SLS,
		"id":        opts.NodeID,

		// Path helpers, Salt-compatible.
		"slspath":      slsPath,
		"slsdotpath":   opts.SLS,
		"slscolonpath": strings.ReplaceAll(opts.SLS, ".", ":"),
		"tplpath":      opts.File,
		"tplfile":      opts.File,
		"tpldir":       tplDir,

		"haliteversion": version.Version,
		"saltversion":   version.SaltCompat,

		// The dunder names existing trees reach for.
		"__env__":    opts.Env,
		"__sls__":    opts.SLS,
		"__opts__":   config,
		"__grains__": grains,
		"__pillar__": pillar,
	}

	if opts.Salt != nil {
		ctx["salt"] = template.NewDispatch(opts.Salt)
	}
	for k, v := range opts.Extra {
		ctx[k] = v
	}
	return ctx
}

func parseYAML(src string, opts Options, rendered *template.Result, res *Result) (any, error) {
	yopts := yaml.DefaultOptions(opts.File)
	yopts.Bool11 = true
	if opts.YAMLBool11 != nil {
		yopts.Bool11 = *opts.YAMLBool11
	}
	yopts.AllowDuplicateKeys = opts.AllowDuplicateKeys

	v, warns, err := yaml.Parse([]byte(src), yopts)
	for _, w := range warns {
		res.Warnings = append(res.Warnings, translateWarning(w, rendered, opts.File))
	}
	if err != nil {
		return nil, translateError(err, rendered, src, opts.File)
	}
	return v, nil
}

// translateError rewrites a YAML error's position through the template's
// source map, and shows both the line the operator wrote and the line the
// renderer produced.
func translateError(err error, rendered *template.Result, renderedText, file string) error {
	ye, ok := err.(*yaml.Error)
	if !ok || rendered == nil {
		return err
	}
	pos, found := rendered.MapLine(ye.Pos.Line)
	if !found {
		return err
	}
	renderedLine := nthLine(renderedText, ye.Pos.Line)
	return &Error{
		File:         file,
		SourceLine:   pos.Line,
		SourceCol:    pos.Col,
		RenderedLine: ye.Pos.Line,
		RenderedText: renderedLine,
		Msg:          ye.Msg,
		Cause:        err,
	}
}

func translateWarning(w yaml.Warning, rendered *template.Result, file string) Warning {
	out := Warning{File: file, Line: w.Pos.Line, Col: w.Pos.Col, Msg: w.Msg}
	if rendered == nil {
		return out
	}
	if pos, found := rendered.MapLine(w.Pos.Line); found {
		out.Line, out.Col = pos.Line, pos.Col
	}
	return out
}

// Error is a serializer failure reported at the template position rather
// than the rendered one.
type Error struct {
	File         string
	SourceLine   int
	SourceCol    int
	RenderedLine int
	RenderedText string
	Msg          string
	Cause        error
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d: %s", e.File, e.SourceLine, e.Msg)
	if e.RenderedText != "" {
		fmt.Fprintf(&b, "\n  rendered line %d was: %s", e.RenderedLine, e.RenderedText)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Cause }

func nthLine(s string, n int) string {
	if n <= 0 {
		return ""
	}
	for i, line := range strings.Split(s, "\n") {
		if i+1 == n {
			return line
		}
	}
	return ""
}

func parseJSON(src string, opts Options) (any, error) {
	v, err := value.DecodeJSON([]byte(src))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", opts.File, err)
	}
	return v, nil
}

// Template renders a source through the template stage alone, with no
// pipeline and no serializer.
//
// A `file.managed` source with `template: jinja` is rendered this way
// rather than through Render, because the file being managed is not an
// SLS file: its first line is content, and reading a `#!` there as a
// renderer pipeline would silently deliver something other than the file.
func Template(src []byte, opts Options) (string, []Warning, error) {
	var res Result
	out, err := renderJinja(string(src), opts, &res)
	if err != nil {
		return "", res.Warnings, err
	}
	return out.Output, res.Warnings, nil
}
