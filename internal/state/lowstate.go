package state

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// Chunk is one executable unit of the low state: a single state module
// function with its resolved arguments.
type Chunk struct {
	// ID is Salt's __id__: the state ID as written.
	ID string
	// SLS is Salt's __sls__.
	SLS string
	// Env is Salt's __env__.
	Env string
	// State is the state module, such as "file".
	State string
	// Fun is the function, such as "managed".
	Fun string
	// Name is the target the function acts on, defaulting to the ID.
	Name string
	// Args are the module arguments, with requisites and per-state options
	// removed.
	Args *value.Map
	// Reqs are this chunk's requisites, in declaration order.
	Reqs []Req
	// Opts are the per-state options of SPEC section 11.7.
	Opts Options
	// DeclOrder is the position of the owning declaration in the high
	// state, which is the tiebreak for unconstrained states.
	DeclOrder int
	// SeqOrder disambiguates chunks expanded from the same declaration.
	SeqOrder int
	// RunNum is the final execution position, filled in by ordering.
	RunNum int
	Pos    value.Pos
}

// Key is Salt's `state_|-id_|-name_|-function` low state key. It is ugly
// and it is load-bearing: every dashboard, returner, and report in an
// estate parses it. SPEC section 11.8.
func (c *Chunk) Key() string {
	return fmt.Sprintf("%s_|-%s_|-%s_|-%s", c.State, c.ID, c.Name, c.Fun)
}

// Func is the dotted `module.function`.
func (c *Chunk) Func() string { return c.State + "." + c.Fun }

// Describe renders the chunk for a diagnostic.
func (c *Chunk) Describe() string {
	return fmt.Sprintf("%s (%s in %s)", c.Func(), c.ID, c.SLS)
}

// OrderMode selects how an explicit `order` option is interpreted.
type OrderMode int

const (
	// OrderNone means no explicit order was given.
	OrderNone OrderMode = iota
	// OrderExplicit places the chunk at a numbered position.
	OrderExplicit
	// OrderLast places the chunk after every unnumbered one.
	OrderLast
)

// Retry is the retry option of SPEC section 11.7.
type Retry struct {
	Attempts int
	Interval time.Duration
	// Until is the result the retry loop waits for, defaulting to true.
	Until bool
	Splay time.Duration
}

// Options are the per-state options every state module honours, as
// distinct from the arguments a particular module takes.
type Options struct {
	// Unless and OnlyIf gate execution. Each accepts a string command, a
	// list of commands, or the structured `{fun: module.function, args:
	// [...]}` form that avoids a shell entirely.
	Unless []any
	OnlyIf []any
	// Creates skips the state when the named path or paths exist.
	Creates []string
	// CheckCmd runs after the state and decides its result.
	CheckCmd []string

	Retry *Retry

	Parallel bool

	OrderMode  OrderMode
	OrderValue int

	// FailHard aborts the run on this state's failure. Nil means inherit
	// the global setting.
	FailHard *bool

	ReloadModules bool
	ReloadGrains  bool
	ReloadPillar  bool

	RunAs         string
	RunAsPassword string
	Umask         string
	Timeout       time.Duration

	// FireEvent fires an event on completion; true uses the default tag,
	// a string uses that tag.
	FireEvent any

	// Aggregate merges compatible chunks of the same module into one
	// call, which is what makes a hundred pkg.installed states one
	// package manager invocation.
	Aggregate bool
}

// optionNames are the argument names that configure the state runner
// rather than the module. Everything else is passed to the module.
var optionNames = map[string]bool{
	"unless": true, "onlyif": true, "creates": true, "check_cmd": true,
	"retry": true, "parallel": true, "order": true, "failhard": true,
	"reload_modules": true, "reload_grains": true, "reload_pillar": true,
	"runas": true, "runas_password": true, "umask": true, "timeout": true,
	"fire_event": true, "aggregate": true,
	// `names` drives expansion and never reaches the module.
	"names": true,
}

// IsOptionArg reports whether an argument name configures the runner.
func IsOptionArg(name string) bool { return optionNames[name] }

// buildChunks turns one declaration into its low chunks, expanding `names`
// into one chunk per name.
func buildChunks(d *Decl, diags *Diags) []*Chunk {
	var out []*Chunk
	for _, f := range d.Funcs {
		out = append(out, buildChunksForFunc(d, f, diags)...)
	}
	return out
}

func buildChunksForFunc(d *Decl, f *FuncDecl, diags *Diags) []*Chunk {
	base := &Chunk{
		ID: d.ID, SLS: d.SLS, Env: d.Env,
		State: f.State, Fun: f.Fun,
		Args:      value.NewMap(f.Args.Len()),
		DeclOrder: d.Order,
		Pos:       f.Pos,
	}

	names := extractNames(f.Args, d, diags)
	base.Name = d.ID
	if v, ok := f.Args.Get("name"); ok {
		s, ok := v.(string)
		if !ok {
			// A name that is not a string is almost always an unquoted
			// value the YAML parser coerced, so the message says so.
			diags.Add(argPos(f.Args, "name"), d.SLS, d.ID,
				"name must be a string, found %s; quote it if the value looks like a number or a boolean",
				value.TypeName(v))
		} else {
			base.Name = s
		}
	}

	// Split the declared arguments into requisites, runner options, and
	// module arguments.
	for _, e := range f.Args.Entries() {
		name := value.KeyString(e.Key)
		switch {
		case name == "name" || name == "names":
			continue
		case IsRequisiteArg(name):
			continue
		case optionNames[name]:
			continue
		default:
			base.Args.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
		}
	}
	base.Opts = parseOptions(f, d, diags)
	base.Reqs = collectRequisites(f, d, diags)

	if len(names) == 0 {
		base.Args.Set("name", base.Name)
		return []*Chunk{base}
	}

	// `names` becomes one low chunk per name, with `name` set and the ID
	// suffixed so each chunk is addressable. SPEC section 11.2 step 7.
	out := make([]*Chunk, 0, len(names))
	for i, n := range names {
		c := &Chunk{
			ID: d.ID, SLS: d.SLS, Env: d.Env,
			State: f.State, Fun: f.Fun,
			Args:      value.Deep(base.Args).(*value.Map),
			Opts:      base.Opts,
			DeclOrder: d.Order,
			SeqOrder:  i,
			Pos:       f.Pos,
		}
		c.Reqs = make([]Req, len(base.Reqs))
		copy(c.Reqs, base.Reqs)

		switch t := n.(type) {
		case *value.Map:
			// A names entry may carry arguments for that name alone.
			if t.Len() != 1 {
				diags.Add(c.Pos, d.SLS, d.ID, "a names entry mapping must have exactly one key")
				continue
			}
			e := t.Entries()[0]
			c.Name = value.KeyString(e.Key)
			applyPerNameArgs(c, e.Val, d, diags)
		default:
			c.Name = value.KeyString(n)
		}
		c.Args.Set("name", c.Name)
		out = append(out, c)
	}
	return out
}

// applyPerNameArgs sets the arguments attached to one entry of a `names`
// list. Salt writes them two ways and a real tree uses the second:
//
//   - names:
//   - web1: {port: 80}          a mapping
//   - /usr/local/bin/x:         a list, spelled like a declaration
//   - source: salt://x
//
// Only the mapping was handled, and the list was dropped without a word.
// On `file.managed` that meant the expanded chunks had no `source` at
// all, so a tree that copies seven scripts into place would have written
// seven empty files.
func applyPerNameArgs(c *Chunk, v any, d *Decl, diags *Diags) {
	switch sub := v.(type) {
	case nil:
		// `- name:` with nothing under it is the name alone.
	case *value.Map:
		for _, se := range sub.Entries() {
			c.Args.SetAt(se.Key, se.Val, se.KeyPos, se.ValPos)
		}
	case []any:
		for _, item := range sub {
			m, ok := item.(*value.Map)
			if !ok {
				diags.Add(c.Pos, d.SLS, d.ID,
					"a names entry's arguments must be `key: value` pairs, found %s", value.TypeName(item))
				continue
			}
			for _, se := range m.Entries() {
				c.Args.SetAt(se.Key, se.Val, se.KeyPos, se.ValPos)
			}
		}
	default:
		diags.Add(c.Pos, d.SLS, d.ID,
			"a names entry's arguments must be a mapping or a list of them, found %s", value.TypeName(v))
	}
}

func extractNames(args *value.Map, d *Decl, diags *Diags) []any {
	v, ok := args.Get("names")
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []any:
		return t
	case nil:
		return nil
	default:
		diags.Add(argPos(args, "names"), d.SLS, d.ID, "names must hold a list, found %s", value.TypeName(v))
		return nil
	}
}

func argPos(args *value.Map, name string) value.Pos {
	if e, ok := args.Entry(name); ok {
		return e.KeyPos
	}
	return value.Pos{}
}

// collectRequisites reads one declaration's requisite arguments.
//
// One argument produces one requisite holding every reference in its list,
// because `_any` and `_all` are statements about the whole list.
func collectRequisites(f *FuncDecl, d *Decl, diags *Diags) []Req {
	var out []Req
	for _, e := range f.Args.Entries() {
		name := value.KeyString(e.Key)
		kind, ok := forwardReqs[name]
		if !ok {
			continue
		}
		refs := parseReqList(e.Val, e.ValPos, d.SLS, d.ID, name, diags)
		if len(refs) == 0 {
			continue
		}
		out = append(out, Req{Kind: kind, Refs: refs})
	}
	return out
}

// parseOptions reads the per-state options of SPEC section 11.7.
func parseOptions(f *FuncDecl, d *Decl, diags *Diags) Options {
	var o Options
	get := func(name string) (any, value.Pos, bool) {
		e, ok := f.Args.Entry(name)
		if !ok {
			return nil, value.Pos{}, false
		}
		return e.Val, e.ValPos, true
	}

	if v, pos, ok := get("unless"); ok {
		o.Unless = asAnyList(v, pos, d, "unless", diags)
	}
	if v, pos, ok := get("onlyif"); ok {
		o.OnlyIf = asAnyList(v, pos, d, "onlyif", diags)
	}
	if v, pos, ok := get("creates"); ok {
		o.Creates = asStringList(v, pos, d, "creates", diags)
	}
	if v, pos, ok := get("check_cmd"); ok {
		o.CheckCmd = asStringList(v, pos, d, "check_cmd", diags)
	}
	if v, _, ok := get("parallel"); ok {
		o.Parallel = value.Truthy(v)
	}
	if v, _, ok := get("reload_modules"); ok {
		o.ReloadModules = value.Truthy(v)
	}
	if v, _, ok := get("reload_grains"); ok {
		o.ReloadGrains = value.Truthy(v)
	}
	if v, _, ok := get("reload_pillar"); ok {
		o.ReloadPillar = value.Truthy(v)
	}
	if v, _, ok := get("aggregate"); ok {
		o.Aggregate = value.Truthy(v)
	}
	if v, _, ok := get("fire_event"); ok {
		o.FireEvent = v
	}
	if v, _, ok := get("runas"); ok {
		o.RunAs = value.KeyString(v)
	}
	if v, _, ok := get("runas_password"); ok {
		o.RunAsPassword, _ = v.(string)
	}
	if v, _, ok := get("umask"); ok {
		o.Umask = value.KeyString(v)
	}
	if v, _, ok := get("failhard"); ok {
		b := value.Truthy(v)
		o.FailHard = &b
	}
	if v, pos, ok := get("timeout"); ok {
		if dur, err := asDuration(v); err == nil {
			o.Timeout = dur
		} else {
			diags.Add(pos, d.SLS, d.ID, "timeout: %v", err)
		}
	}
	if v, pos, ok := get("order"); ok {
		switch t := v.(type) {
		case int64:
			o.OrderMode, o.OrderValue = OrderExplicit, int(t)
		case string:
			if strings.EqualFold(t, "last") {
				o.OrderMode = OrderLast
				break
			}
			// Salt gives `first` the order 0, which is ahead of every
			// unnumbered state and of any positive number. Refusing it
			// stopped a tree that Salt compiles.
			if strings.EqualFold(t, "first") {
				o.OrderMode, o.OrderValue = OrderExplicit, 0
				break
			}
			n, err := strconv.Atoi(t)
			if err != nil {
				diags.Add(pos, d.SLS, d.ID, "order must be an integer, `first`, or `last`, found %q", t)
				break
			}
			o.OrderMode, o.OrderValue = OrderExplicit, n
		default:
			diags.Add(pos, d.SLS, d.ID, "order must be an integer, `first`, or `last`, found %s", value.TypeName(v))
		}
	}

	if v, pos, ok := get("retry"); ok {
		o.Retry = parseRetry(v, pos, d, diags)
	}
	return o
}

func parseRetry(v any, pos value.Pos, d *Decl, diags *Diags) *Retry {
	r := &Retry{Attempts: 2, Interval: 30 * time.Second, Until: true}
	m, ok := v.(*value.Map)
	if !ok {
		// `retry: True` uses the defaults.
		if value.Truthy(v) {
			return r
		}
		return nil
	}
	for _, e := range m.Entries() {
		key := value.KeyString(e.Key)
		switch key {
		case "attempts":
			if n, ok := e.Val.(int64); ok {
				r.Attempts = int(n)
				continue
			}
			diags.Add(e.ValPos, d.SLS, d.ID, "retry attempts must be an integer")
		case "interval":
			if dur, err := asDuration(e.Val); err == nil {
				r.Interval = dur
				continue
			}
			diags.Add(e.ValPos, d.SLS, d.ID, "retry interval must be a duration")
		case "splay":
			if dur, err := asDuration(e.Val); err == nil {
				r.Splay = dur
				continue
			}
			diags.Add(e.ValPos, d.SLS, d.ID, "retry splay must be a duration")
		case "until":
			r.Until = value.Truthy(e.Val)
		default:
			diags.Add(e.KeyPos, d.SLS, d.ID, "retry has no option %q; it takes attempts, interval, until, and splay", key)
		}
	}
	if r.Attempts < 1 {
		diags.Add(pos, d.SLS, d.ID, "retry attempts must be at least 1")
	}
	return r
}

func asAnyList(v any, pos value.Pos, d *Decl, name string, diags *Diags) []any {
	switch t := v.(type) {
	case []any:
		return t
	case nil:
		return nil
	default:
		return []any{t}
	}
}

func asStringList(v any, pos value.Pos, d *Decl, name string, diags *Diags) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				diags.Add(pos, d.SLS, d.ID, "%s entries must be strings, found %s", name, value.TypeName(item))
				continue
			}
			out = append(out, s)
		}
		return out
	case nil:
		return nil
	default:
		diags.Add(pos, d.SLS, d.ID, "%s must be a string or a list, found %s", name, value.TypeName(v))
		return nil
	}
}

func asDuration(v any) (time.Duration, error) {
	switch t := v.(type) {
	case int64:
		return time.Duration(t) * time.Second, nil
	case float64:
		return time.Duration(t * float64(time.Second)), nil
	case string:
		if d, err := time.ParseDuration(t); err == nil {
			return d, nil
		}
		if n, err := strconv.ParseFloat(t, 64); err == nil {
			return time.Duration(n * float64(time.Second)), nil
		}
		return 0, fmt.Errorf("%q is not a duration", t)
	}
	return 0, fmt.Errorf("%s is not a duration", value.TypeName(v))
}
