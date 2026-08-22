package state

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// ErrNotFound is what a Loader returns for an SLS name that does not
// resolve in an environment.
var ErrNotFound = errors.New("sls not found")

// Loader resolves SLS names to source. The file server supplies one on a
// node; a directory loader serves `--local` and the test suite.
type Loader interface {
	// Source resolves a dotted SLS name in an environment to its bytes
	// and the path to report in diagnostics.
	Source(env, sls string) (src []byte, path string, err error)
	// Envs lists the environments this loader serves, in a stable order.
	Envs() []string
	// Templates returns the loader that resolves include, import, and
	// extends *inside* a template, which addresses files by path rather
	// than by dotted SLS name.
	Templates(env string) template.Loader
}

// Config is everything the compiler needs that is not the tree itself.
type Config struct {
	// Env is the environment to compile. Empty means "base".
	Env string
	// PillarEnv is exposed to templates as `pillarenv`.
	PillarEnv string
	NodeID    string
	JobID     string

	Grains *value.Map
	Pillar *value.Map
	// ConfigValues is the effective configuration, redacted, bound to
	// `opts` in a template.
	ConfigValues *value.Map

	// Salt is the execution module dispatcher bound to `salt`.
	Salt template.Dispatcher

	// Undefined selects strict or permissive template name resolution.
	Undefined template.UndefinedMode
	// OnUndefined reports each permissive resolution.
	OnUndefined func(name string, pos template.Pos)

	// YAMLBool11 enables YAML 1.1's extra boolean spellings.
	YAMLBool11 *bool
	// GPG configures the gpg renderer of SPEC section 12.6.
	GPG render.GPGOptions

	Nodegroups target.Nodegroups

	// TopMergeStrategy is `merge`, `same`, or `merge_all`. SPEC section
	// 11.2 step 1.
	TopMergeStrategy string

	// StateAllowlist and StateDenylist restrict which SLS names a run may
	// include.
	StateAllowlist []string
	StateDenylist  []string

	// Test marks the run as a test run, which reaches templates through
	// `opts` and reaches modules through the execution context.
	Test bool
}

// Compiler turns a tree into an ordered low state.
type Compiler struct {
	Loader   Loader
	Registry *signature.Registry
	Config   Config
}

// Compiled is the result of a compilation.
type Compiled struct {
	// High is the assembled declaration structure.
	High *HighState
	// Low is the ordered list of executable chunks.
	Low []*Chunk
	// SLS lists the SLS names that contributed, in resolution order.
	SLS []string
	// Diags holds every error and warning found, together.
	Diags Diags
	// RenderWarnings are the lint-level findings from rendering.
	RenderWarnings []render.Warning
}

// Err reports the compilation errors as one error, or nil.
func (c *Compiled) Err() error { return c.Diags.Err() }

func (c *Compiler) env() string {
	if c.Config.Env != "" {
		return c.Config.Env
	}
	return "base"
}

// CompileSLS compiles a named set of SLS files, which is what
// `state.apply <sls>` and `state.sls` do.
func (c *Compiler) CompileSLS(names []string) *Compiled {
	out := &Compiled{High: NewHighState()}
	c.compileNames(out, c.env(), names)
	c.finish(out)
	return out
}

// CompileHighstate resolves the top file and compiles everything it
// matches, which is what `state.apply` with no arguments does.
func (c *Compiler) CompileHighstate() *Compiled {
	out := &Compiled{High: NewHighState()}
	matched, diags := c.resolveTop()
	out.Diags = append(out.Diags, diags...)
	for _, env := range sortedEnvKeys(matched) {
		c.compileNames(out, env, matched[env])
	}
	c.finish(out)
	return out
}

func sortedEnvKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	// The default environment compiles first, so its declaration order
	// leads the run.
	for i, e := range out {
		if e == "base" && i != 0 {
			out = append([]string{"base"}, append(out[:i:i], out[i+1:]...)...)
			break
		}
	}
	return out
}

// compileNames renders and includes an ordered list of SLS names.
func (c *Compiler) compileNames(out *Compiled, env string, names []string) {
	seen := map[string]bool{}
	var extends []pendingExtend
	var excludes []excludeRef

	for _, name := range names {
		c.loadSLS(out, env, name, seen, nil, &extends, &excludes)
	}

	c.applyExtends(out, extends)
	c.applyExcludes(out, excludes)
}

type pendingExtend struct {
	from string
	env  string
	m    *value.Map
}

// loadSLS renders one SLS and, depth-first, everything it includes.
//
// The cycle detector reports the path rather than a recursion depth,
// because an include cycle in a large tree is otherwise found by reading.
func (c *Compiler) loadSLS(out *Compiled, env, name string, seen map[string]bool, stack []string, extends *[]pendingExtend, excludes *[]excludeRef) {
	key := env + "|" + name

	// The cycle check runs before the visited check, because the visited
	// set would otherwise absorb the second arrival and hide the cycle.
	//
	// A cycle is reported rather than fatal. SPEC section 11.2 says step 3
	// "reports the cycle path", where steps 4 and 5 say plainly that a
	// duplicate ID and a dangling extend "is an error"; and mutual
	// includes exist in trees that work today, because Salt's own visited
	// set absorbs them silently. Making the cycle visible without breaking
	// those trees is the useful half of the change.
	for i, s := range stack {
		if s == key {
			cyclePath := append(append([]string{}, stack[i:]...), key)
			out.Diags.Warn(value.Pos{File: name}, name, "",
				"include cycle: %s; the second arrival is skipped", strings.Join(stripEnv(cyclePath), " -> "))
			return
		}
	}
	if seen[key] {
		return
	}
	if !c.allowedSLS(name) {
		out.Diags.Add(value.Pos{File: name}, name, "",
			"sls %q is excluded by state_allowlist or state_denylist", name)
		return
	}
	seen[key] = true

	src, filePath, err := c.Loader.Source(env, name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			out.Diags.Add(value.Pos{File: name}, name, "",
				"sls %q was not found in environment %q", name, env)
			return
		}
		out.Diags.Add(value.Pos{File: name}, name, "", "reading sls %q: %v", name, err)
		return
	}

	res, err := render.Render(src, c.renderOptions(env, name, filePath))
	out.RenderWarnings = append(out.RenderWarnings, res.Warnings...)
	if err != nil {
		out.Diags.Add(value.Pos{File: filePath}, name, "", "%v", err)
		return
	}

	content := parseSLS(res.Value, name, env, &out.Diags)
	out.SLS = append(out.SLS, name)

	// Includes are expanded depth-first, so an included file's states are
	// declared before the file that included them. Salt does the same, and
	// unconstrained ordering depends on it.
	for _, inc := range content.Includes {
		incEnv := inc.Env
		if incEnv == "" {
			incEnv = env
		}
		c.loadSLS(out, incEnv, inc.Name, seen, append(stack, key), extends, excludes)
	}

	for _, d := range content.Decls {
		out.High.add(d, &out.Diags)
	}
	if content.Extends != nil {
		*extends = append(*extends, pendingExtend{from: name, env: env, m: content.Extends})
	}
	*excludes = append(*excludes, content.Excludes...)
}

func stripEnv(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		if j := strings.Index(k, "|"); j >= 0 {
			out[i] = k[j+1:]
			continue
		}
		out[i] = k
	}
	return out
}

func (c *Compiler) renderOptions(env, sls, filePath string) render.Options {
	return render.Options{
		File:        filePath,
		SLS:         sls,
		Env:         env,
		PillarEnv:   c.Config.PillarEnv,
		NodeID:      c.Config.NodeID,
		JobID:       c.Config.JobID,
		Grains:      c.Config.Grains,
		Pillar:      c.Config.Pillar,
		Config:      c.Config.ConfigValues,
		Salt:        c.Config.Salt,
		Loader:      c.Loader.Templates(env),
		Undefined:   c.Config.Undefined,
		OnUndefined: c.Config.OnUndefined,
		YAMLBool11:  c.Config.YAMLBool11,
		GPG:         c.Config.GPG,
	}
}

// allowedSLS applies state_allowlist and state_denylist.
func (c *Compiler) allowedSLS(name string) bool {
	for _, pattern := range c.Config.StateDenylist {
		if globOK(pattern, name) {
			return false
		}
	}
	if len(c.Config.StateAllowlist) == 0 {
		return true
	}
	for _, pattern := range c.Config.StateAllowlist {
		if globOK(pattern, name) {
			return true
		}
	}
	return false
}

func globOK(pattern, s string) bool {
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

// applyExtends runs after every include, over the assembled high state,
// which is the order Salt uses and the order a tree expects.
func (c *Compiler) applyExtends(out *Compiled, extends []pendingExtend) {
	for _, ext := range extends {
		for _, e := range ext.m.Entries() {
			id := value.KeyString(e.Key)
			decl, ok := out.High.Lookup(id)
			if !ok {
				out.Diags.Add(e.KeyPos, ext.from, id,
					"extend names %q, which is not declared anywhere in this run", id)
				continue
			}
			body, ok := e.Val.(*value.Map)
			if !ok {
				out.Diags.Add(e.ValPos, ext.from, id,
					"an extend entry must hold module.function declarations, found %s", value.TypeName(e.Val))
				continue
			}
			for _, fe := range body.Entries() {
				c.extendFunc(out, decl, ext, fe)
			}
		}
	}
}

func (c *Compiler) extendFunc(out *Compiled, decl *Decl, ext pendingExtend, fe value.Entry) {
	key := value.KeyString(fe.Key)
	newDecl := parseFuncDecl(key, fe, ext.from, decl.ID, &out.Diags)
	if newDecl == nil {
		return
	}

	existing, found := decl.Func(newDecl.State)
	if !found {
		// Extending a module the original did not declare adds it, which
		// is how a tree attaches a `service.running` watch to a state
		// declared elsewhere.
		decl.Funcs = append(decl.Funcs, newDecl)
		return
	}

	// A scalar replaces; a list appends. The append is what makes the
	// idiomatic `extend` plus `watch` work, where a site adds one more
	// thing to watch without restating the others.
	for _, ae := range newDecl.Args.Entries() {
		name := value.KeyString(ae.Key)
		cur, has := existing.Args.Get(name)
		if !has {
			existing.Args.SetAt(ae.Key, ae.Val, ae.KeyPos, ae.ValPos)
			continue
		}
		curList, curIsList := cur.([]any)
		newList, newIsList := ae.Val.([]any)
		if curIsList && newIsList {
			merged := make([]any, 0, len(curList)+len(newList))
			merged = append(merged, curList...)
			merged = append(merged, newList...)
			existing.Args.SetAt(ae.Key, merged, ae.KeyPos, ae.ValPos)
			continue
		}
		existing.Args.SetAt(ae.Key, ae.Val, ae.KeyPos, ae.ValPos)
	}
	existing.Flags = append(existing.Flags, newDecl.Flags...)
}

// applyExcludes removes SLS files and state IDs after extend has run.
func (c *Compiler) applyExcludes(out *Compiled, excludes []excludeRef) {
	if len(excludes) == 0 {
		return
	}
	for _, ex := range excludes {
		switch {
		case ex.SLS != "":
			out.High.excludedSLS[ex.SLS] = true
		case ex.ID != "":
			out.High.excludedIDs[ex.ID] = true
		}
	}

	kept := make([]*Decl, 0, len(out.High.decls))
	index := make(map[string]int, len(out.High.decls))
	for _, d := range out.High.decls {
		if out.High.excludedSLS[d.SLS] || out.High.excludedIDs[d.ID] {
			continue
		}
		index[d.ID] = len(kept)
		d.Order = len(kept)
		kept = append(kept, d)
	}
	out.High.decls = kept
	out.High.index = index
}

// finish runs the stages that need the whole high state: chunk expansion,
// requisite resolution, ordering, and validation.
func (c *Compiler) finish(out *Compiled) {
	c.resolveInverseRequisites(out)

	var chunks []*Chunk
	for _, d := range out.High.Decls() {
		chunks = append(chunks, buildChunks(d, &out.Diags)...)
	}

	resolveRequisites(chunks, &out.Diags)
	applyUse(chunks, &out.Diags)
	c.validate(chunks, out)
	out.Low = order(chunks, &out.Diags)
}

// resolveInverseRequisites rewrites every `_in` form into the forward form
// on the target declaration, so that nothing downstream has to know the
// inverse forms exist.
func (c *Compiler) resolveInverseRequisites(out *Compiled) {
	type addition struct {
		targetID    string
		targetState string
		kind        ReqKind
		ref         ReqRef
		pos         value.Pos
		fromSLS     string
		fromID      string
	}
	var additions []addition

	for _, d := range out.High.Decls() {
		for _, f := range d.Funcs {
			for _, e := range f.Args.Entries() {
				name := value.KeyString(e.Key)
				kind, ok := inverseReqs[name]
				if !ok {
					continue
				}
				for _, ref := range parseReqList(e.Val, e.ValPos, d.SLS, d.ID, name, &out.Diags) {
					additions = append(additions, addition{
						targetID:    ref.ID,
						targetState: ref.State,
						kind:        kind,
						// The forward requisite points back at the
						// declaring state.
						ref:     ReqRef{State: f.State, ID: d.ID, Pos: e.ValPos},
						pos:     e.ValPos,
						fromSLS: d.SLS,
						fromID:  d.ID,
					})
				}
			}
		}
	}

	for _, a := range additions {
		if a.targetID == "" {
			out.Diags.Add(a.pos, a.fromSLS, a.fromID,
				"%s_in cannot name an sls; write the forward requisite on the other state", a.kind)
			continue
		}
		targetDecl, ok := out.High.Lookup(a.targetID)
		if !ok {
			out.Diags.Add(a.pos, a.fromSLS, a.fromID,
				"%s_in names %q, which is not declared in this run", a.kind, a.targetID)
			continue
		}
		targetFunc := pickExtendTarget(targetDecl, a.targetState)
		if targetFunc == nil {
			out.Diags.Add(a.pos, a.fromSLS, a.fromID,
				"%s_in names `%s: %s`, but %q does not declare %s",
				a.kind, a.targetState, a.targetID, a.targetID, a.targetState)
			continue
		}
		appendRequisiteArg(targetFunc, a.kind, a.ref)
	}
}

func pickExtendTarget(d *Decl, state string) *FuncDecl {
	if state == "" {
		if len(d.Funcs) == 1 {
			return d.Funcs[0]
		}
		// With more than one module under an ID, an unqualified `_in`
		// attaches to the first, which is what Salt does.
		if len(d.Funcs) > 0 {
			return d.Funcs[0]
		}
		return nil
	}
	f, ok := d.Func(state)
	if !ok {
		return nil
	}
	return f
}

// appendRequisiteArg adds a forward requisite to a function declaration's
// arguments, creating the list if it is not there yet.
func appendRequisiteArg(f *FuncDecl, kind ReqKind, ref ReqRef) {
	name := kind.String()
	entry := value.NewMap(1)
	if ref.State != "" {
		entry.SetAt(ref.State, ref.ID, ref.Pos, ref.Pos)
	} else {
		entry.SetAt("id", ref.ID, ref.Pos, ref.Pos)
	}

	cur, ok := f.Args.Get(name)
	if !ok {
		f.Args.SetAt(name, []any{entry}, ref.Pos, ref.Pos)
		return
	}
	list, ok := cur.([]any)
	if !ok {
		list = []any{cur}
	}
	f.Args.SetAt(name, append(list, entry), ref.Pos, ref.Pos)
}

// validate is SPEC section 11.2 step 10: every state module and function
// exists, every required argument is present, every argument type is
// correct, every requisite target resolves. All errors are collected and
// reported together.
func (c *Compiler) validate(chunks []*Chunk, out *Compiled) {
	if c.Registry == nil {
		return
	}
	for _, ch := range chunks {
		sig, ok := c.Registry.Lookup(ch.Func())
		if !ok {
			out.Diags.Add(ch.Pos, ch.SLS, ch.ID, "%s", c.unknownFunctionMessage(ch))
			continue
		}
		_, errs := sig.Bind(nil, ch.Args)
		for _, err := range errs {
			out.Diags.Add(ch.Pos, ch.SLS, ch.ID, "%v", err)
		}

		// An argument this build accepts and does not act on is a
		// warning at the line that wrote it, so that a tree carrying one
		// compiles and its author still finds out.
		for _, p := range sig.Params {
			if p.Ineffective == "" || !ch.Args.Has(p.Name) {
				continue
			}
			out.Diags.Warn(ch.Pos, ch.SLS, ch.ID,
				"%s: %q has no effect here: %s", ch.Func(), p.Name, p.Ineffective)
		}

		// A prereq target that cannot honestly predict its changes is a
		// warning naming the risk, because a partial prereq is worse than
		// none. SPEC section 11.5.
		for _, req := range ch.Reqs {
			if req.Kind != Prereq {
				continue
			}
			for _, idx := range req.Resolved {
				target := chunks[idx]
				tsig, ok := c.Registry.Lookup(target.Func())
				if ok && tsig.TestMode == signature.TestUnreliable {
					out.Diags.Warn(ch.Pos, ch.SLS, ch.ID,
						"prereq names %s, whose test mode is unreliable; the changes prediction this prereq depends on may be wrong",
						target.Func())
				}
			}
		}
	}
}

// unknownFunctionMessage names a near miss where there is one, because a
// typo in a function name is the most common single compilation error.
func (c *Compiler) unknownFunctionMessage(ch *Chunk) string {
	if fns := c.Registry.Functions(ch.State); len(fns) > 0 {
		var names []string
		for _, f := range fns {
			names = append(names, f.Function)
		}
		return fmt.Sprintf("the %s state module has no function %q; it provides %s",
			ch.State, ch.Fun, strings.Join(names, ", "))
	}
	return fmt.Sprintf("%q is not a state module this build ships; see the tier table in SPEC section 15", ch.State)
}
