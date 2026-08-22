package template

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// scope is one level of the variable chain. A for body, a with block, a
// macro invocation, and an included template each get one.
type scope struct {
	vars   map[string]any
	parent *scope
}

func newScope(parent *scope) *scope {
	return &scope{vars: map[string]any{}, parent: parent}
}

func (s *scope) lookup(name string) (any, bool) {
	for cur := s; cur != nil; cur = cur.parent {
		if v, ok := cur.vars[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func (s *scope) set(name string, v any) { s.vars[name] = v }

type renderer struct {
	env    *Environment
	opts   Options
	budget *budget
	out    *strings.Builder
	scope  *scope
	rnd    *rand.Rand

	// Template inheritance state.
	blocks map[string][]*BlockNode
	tpl    *Template

	depth        int
	includeDepth int

	srcMap  []SourceMapEntry
	outLine int
	lastPos Pos
}

func newRenderer(env *Environment, b *budget) *renderer {
	return &renderer{
		env:     env,
		opts:    env.Opts,
		budget:  b,
		out:     &strings.Builder{},
		blocks:  map[string][]*BlockNode{},
		outLine: 1,
	}
}

// sub creates a renderer that shares the budget, environment, and random
// source but writes to its own buffer. Macros, includes, and block
// captures use it.
func (r *renderer) sub() *renderer {
	c := newRenderer(r.env, r.budget)
	c.rnd = r.rnd
	c.blocks = r.blocks
	c.depth = r.depth
	c.includeDepth = r.includeDepth
	c.tpl = r.tpl
	return c
}

// undefinedError converts an undefined value into an error under strict
// mode, or reports it and returns nil under permissive mode.
func (r *renderer) undefinedError(u Undefined, pos Pos) error {
	if r.opts.Undefined == Permissive {
		if r.opts.OnUndefined != nil {
			at := u.Pos
			if at.Line == 0 {
				at = pos
			}
			r.opts.OnUndefined(u.Name, at)
		}
		return nil
	}
	at := u.Pos
	if at.Line == 0 {
		at = pos
	}
	return errorf(at, "%s; use `| default(...)`, `is defined`, or `.get(name, fallback)` to make it optional", u.describe())
}

// ---- template inheritance ----

// renderRoot resolves the inheritance chain and renders the root ancestor.
func (r *renderer) renderRoot(t *Template) error {
	chain := []*Template{t}
	cur := t
	for {
		ext := extendsOf(cur)
		if ext == nil {
			break
		}
		if len(chain) > r.opts.MaxIncludeDepth {
			return errorf(ext.Pos(), "template inheritance deeper than %d levels", r.opts.MaxIncludeDepth)
		}
		nameVal, err := r.eval(ext.Name)
		if err != nil {
			return err
		}
		name, err := r.toStrErr(nameVal, ext.Pos())
		if err != nil {
			return err
		}
		parent, err := r.env.load(name)
		if err != nil {
			return errorf(ext.Pos(), "extends %q: %v", name, err)
		}
		chain = append(chain, parent)
		cur = parent
	}

	// The most-derived template's blocks come first, so that a block
	// lookup finds the override and super() walks toward the root.
	for _, tpl := range chain {
		for _, b := range collectBlocks(tpl.Body) {
			r.blocks[b.Name] = append(r.blocks[b.Name], b)
		}
	}

	// A child template's own top-level output is discarded; its
	// definitions still run, because a child commonly sets variables and
	// defines macros that its blocks use.
	for i := 0; i < len(chain)-1; i++ {
		if err := r.runDefinitions(chain[i].Body); err != nil {
			return err
		}
	}

	root := chain[len(chain)-1]
	r.tpl = root
	return r.renderNodes(root.Body)
}

func extendsOf(t *Template) *ExtendsNode {
	for _, n := range t.Body {
		if e, ok := n.(*ExtendsNode); ok {
			return e
		}
	}
	return nil
}

func collectBlocks(body []Node) []*BlockNode {
	var out []*BlockNode
	var walk func([]Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			switch t := n.(type) {
			case *BlockNode:
				out = append(out, t)
				walk(t.Body)
			case *IfNode:
				for _, b := range t.Blocks {
					walk(b)
				}
				walk(t.Else)
			case *ForNode:
				walk(t.Body)
				walk(t.Else)
			case *WithNode:
				walk(t.Body)
			case *FilterNode:
				walk(t.Body)
			case *AutoescapeNode:
				walk(t.Body)
			}
		}
	}
	walk(body)
	return out
}

// runDefinitions executes only the statements of a child template that
// establish names, discarding any output.
func (r *renderer) runDefinitions(body []Node) error {
	for _, n := range body {
		switch n.(type) {
		case *SetNode, *MacroNode, *ImportNode, *FromImportNode:
			sub := r.sub()
			sub.scope = r.scope
			if err := sub.renderNode(n); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- statements ----

func (r *renderer) renderNodes(nodes []Node) error {
	for _, n := range nodes {
		if err := r.renderNode(n); err != nil {
			return err
		}
	}
	return nil
}

// writeText writes a literal span from the template. Because the text is
// copied verbatim, its lines and the output's lines advance together, so
// each one gets its own source map entry. That is what lets a YAML error
// forty lines into a rendered loop still name the line the operator wrote.
func (r *renderer) writeText(s string, pos Pos) error {
	if s == "" {
		return nil
	}
	if err := r.budget.write(len(s), pos); err != nil {
		return err
	}
	if pos.Line != 0 {
		lines := strings.Count(s, "\n")
		for k := 0; k <= lines; k++ {
			r.srcMap = append(r.srcMap, SourceMapEntry{
				OutLine: r.outLine + k,
				Pos:     Pos{File: pos.File, Line: pos.Line + k, Col: 1},
			})
		}
		r.lastPos = Pos{}
	}
	r.out.WriteString(s)
	r.outLine += strings.Count(s, "\n")
	return nil
}

func (r *renderer) write(s string, pos Pos) error {
	if s == "" {
		return nil
	}
	if err := r.budget.write(len(s), pos); err != nil {
		return err
	}
	if pos.Line != 0 && pos != r.lastPos {
		r.srcMap = append(r.srcMap, SourceMapEntry{OutLine: r.outLine, Pos: pos})
		r.lastPos = pos
	}
	r.out.WriteString(s)
	r.outLine += strings.Count(s, "\n")
	return nil
}

func (r *renderer) renderNode(n Node) error {
	if err := r.budget.tick(n.Pos()); err != nil {
		return err
	}
	r.depth++
	if r.depth > r.opts.MaxDepth {
		r.depth--
		return errorf(n.Pos(), "template nesting deeper than %d levels", r.opts.MaxDepth)
	}
	defer func() { r.depth-- }()

	switch t := n.(type) {
	case *TextNode:
		return r.writeText(t.Text, t.Pos())

	case *OutputNode:
		v, err := r.eval(t.Expr)
		if err != nil {
			return err
		}
		s, err := r.toStrErr(v, t.Pos())
		if err != nil {
			return err
		}
		return r.write(s, t.Pos())

	case *IfNode:
		for i, cond := range t.Conds {
			v, err := r.eval(cond)
			if err != nil {
				return err
			}
			if truthy(v) {
				return r.renderNodes(t.Blocks[i])
			}
		}
		return r.renderNodes(t.Else)

	case *ForNode:
		return r.renderFor(t)

	case *SetNode:
		return r.renderSet(t)

	case *MacroNode:
		r.scope.set(t.Name, r.makeMacro(t))
		return nil

	case *CallNode:
		return r.renderCall(t)

	case *IncludeNode:
		return r.renderInclude(t)

	case *ImportNode:
		return r.renderImport(t)

	case *FromImportNode:
		return r.renderFromImport(t)

	case *ExtendsNode:
		// Handled by renderRoot before any body runs.
		return nil

	case *BlockNode:
		return r.renderBlock(t)

	case *FilterNode:
		sub := r.sub()
		sub.scope = newScope(r.scope)
		if err := sub.renderNodes(t.Body); err != nil {
			return err
		}
		v, err := r.evalFilterOn(t.Filter, sub.out.String())
		if err != nil {
			return err
		}
		s, err := r.toStrErr(v, t.Pos())
		if err != nil {
			return err
		}
		return r.write(s, t.Pos())

	case *DoNode:
		_, err := r.eval(t.Expr)
		return err

	case *WithNode:
		// Every value is evaluated in the enclosing scope before any of
		// the names is bound, so `{% with x=1, y=x %}` gives y the outer
		// x and not the one this statement is about to introduce.
		// Binding as it went made each value see the ones before it,
		// which reads plausibly and is not what Jinja does.
		values := make([]any, len(t.Targets))
		for i := range t.Targets {
			v, err := r.eval(t.Values[i])
			if err != nil {
				return err
			}
			values[i] = v
		}
		saved := r.scope
		r.scope = newScope(saved)
		defer func() { r.scope = saved }()
		for i, target := range t.Targets {
			r.scope.set(target, values[i])
		}
		return r.renderNodes(t.Body)

	case *AutoescapeNode:
		return r.renderNodes(t.Body)
	}
	return errorf(n.Pos(), "unhandled node %T", n)
}

func (r *renderer) renderFor(t *ForNode) error {
	iterVal, err := r.eval(t.Iter)
	if err != nil {
		return err
	}

	var run func(items any, depth int) error
	run = func(iterable any, depth int) error {
		items, err := r.iterate(iterable, t.Pos())
		if err != nil {
			return err
		}

		// The inline `if` filter is applied before the loop runs, so that
		// loop.length and loop.last count only the items that survive it.
		if t.Cond != nil {
			filtered := make([]any, 0, len(items))
			saved := r.scope
			r.scope = newScope(saved)
			for _, item := range items {
				if err := r.bindTargets(t.Targets, item, t.Pos()); err != nil {
					r.scope = saved
					return err
				}
				keep, err := r.eval(t.Cond)
				if err != nil {
					r.scope = saved
					return err
				}
				if truthy(keep) {
					filtered = append(filtered, item)
				}
			}
			r.scope = saved
			items = filtered
		}

		if len(items) == 0 {
			return r.renderNodes(t.Else)
		}

		saved := r.scope
		defer func() { r.scope = saved }()

		loop := &LoopInfo{Length: len(items), Items: items, Depth0: depth}
		if t.Recursive {
			loop.recurse = func(inner any) (string, error) {
				sub := r.sub()
				sub.scope = r.scope
				if err := sub.renderForRecursive(t, inner, depth+1); err != nil {
					return "", err
				}
				return sub.out.String(), nil
			}
		}

		for i, item := range items {
			if err := r.budget.iterate(t.Pos()); err != nil {
				return err
			}
			loop.Index0 = i
			r.scope = newScope(saved)
			r.scope.set("loop", loop)
			if err := r.bindTargets(t.Targets, item, t.Pos()); err != nil {
				return err
			}
			if err := r.renderNodes(t.Body); err != nil {
				return err
			}
		}
		return nil
	}

	return run(iterVal, 0)
}

// renderForRecursive re-enters a recursive loop's body for loop().
func (r *renderer) renderForRecursive(t *ForNode, iterable any, depth int) error {
	items, err := r.iterate(iterable, t.Pos())
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return r.renderNodes(t.Else)
	}
	if depth > r.opts.MaxDepth {
		return errorf(t.Pos(), "recursive loop deeper than %d levels", r.opts.MaxDepth)
	}
	saved := r.scope
	defer func() { r.scope = saved }()

	loop := &LoopInfo{Length: len(items), Items: items, Depth0: depth}
	loop.recurse = func(inner any) (string, error) {
		sub := r.sub()
		sub.scope = r.scope
		if err := sub.renderForRecursive(t, inner, depth+1); err != nil {
			return "", err
		}
		return sub.out.String(), nil
	}
	for i, item := range items {
		if err := r.budget.iterate(t.Pos()); err != nil {
			return err
		}
		loop.Index0 = i
		r.scope = newScope(saved)
		r.scope.set("loop", loop)
		if err := r.bindTargets(t.Targets, item, t.Pos()); err != nil {
			return err
		}
		if err := r.renderNodes(t.Body); err != nil {
			return err
		}
	}
	return nil
}

// bindTargets assigns a loop item to one or more target names, unpacking a
// sequence when there is more than one target.
func (r *renderer) bindTargets(targets []string, item any, pos Pos) error {
	item = untuple(item)
	if len(targets) == 1 {
		r.scope.set(targets[0], item)
		return nil
	}
	parts, ok := item.([]any)
	if !ok {
		return errorf(pos, "cannot unpack %s into %d loop variables", typeName(item), len(targets))
	}
	if len(parts) != len(targets) {
		return errorf(pos, "cannot unpack %d values into %d loop variables", len(parts), len(targets))
	}
	for i, name := range targets {
		r.scope.set(name, parts[i])
	}
	return nil
}

func (r *renderer) renderSet(t *SetNode) error {
	if t.Namespace != "" {
		nsVal, ok := r.scope.lookup(t.Namespace)
		if !ok {
			return errorf(t.Pos(), "%s is undefined; a namespace must be created with namespace() before assigning to it", t.Namespace)
		}
		ns, ok := nsVal.(*Namespace)
		if !ok {
			return errorf(t.Pos(), "%s is not a namespace", t.Namespace)
		}
		v, err := r.eval(t.Value)
		if err != nil {
			return err
		}
		ns.m.Set(t.Attr, v)
		return nil
	}

	var v any
	if t.Value != nil {
		var err error
		if v, err = r.eval(t.Value); err != nil {
			return err
		}
	} else {
		sub := r.sub()
		sub.scope = newScope(r.scope)
		if err := sub.renderNodes(t.Body); err != nil {
			return err
		}
		v = sub.out.String()
		if t.Filter != nil {
			var err error
			if v, err = r.evalFilterOn(t.Filter, v); err != nil {
				return err
			}
		}
	}

	// `{% set %}` always assigns in the current scope, never in an
	// enclosing one. A loop body is a fresh scope per iteration, so an
	// assignment there does not survive to the next iteration and does not
	// escape the loop; `{% set x = 1 %}` inside a for is invisible after
	// `{% endfor %}`. That is Jinja's rule, and it is the whole reason
	// Jinja has `namespace()`: without it there is no way to carry a value
	// out of a loop, and a tree that relied on halite's leaking assignment
	// would have produced a different result under Salt.
	if len(t.Targets) == 1 {
		r.scope.set(t.Targets[0], v)
		return nil
	}
	parts, ok := untuple(v).([]any)
	if !ok || len(parts) != len(t.Targets) {
		return errorf(t.Pos(), "cannot unpack %s into %d names", typeName(v), len(t.Targets))
	}
	for i, name := range t.Targets {
		r.scope.set(name, parts[i])
	}
	return nil
}

// evalFilterOn re-evaluates a parsed filter chain with a supplied value in
// place of its innermost operand, which is how block `set` and `filter`
// pipe a rendered body through filters.
func (r *renderer) evalFilterOn(chain Expr, v any) (any, error) {
	f, ok := chain.(*FilterExpr)
	if !ok {
		return v, nil
	}
	inner := v
	if _, isLit := f.Obj.(*LiteralExpr); !isLit {
		var err error
		if inner, err = r.evalFilterOn(f.Obj, v); err != nil {
			return nil, err
		}
	}
	return r.applyFilter(f, inner)
}

func (r *renderer) renderBlock(t *BlockNode) error {
	defs := r.blocks[t.Name]
	if len(defs) == 0 {
		return r.renderNodes(t.Body)
	}
	return r.renderBlockAt(t.Name, defs, 0)
}

// renderBlockAt renders one definition in a block's override chain and
// binds a super() that reaches the *next* one.
//
// The index is a parameter rather than renderer state because super() is a
// closure: with shared state, the super bound while rendering the parent's
// body still pointed at the parent, so a three-level chain rendered the
// same definition forever. Each level gets its own index, so each super()
// walks exactly one step toward the root.
func (r *renderer) renderBlockAt(name string, defs []*BlockNode, idx int) error {
	if idx >= len(defs) {
		return nil
	}
	saved := r.scope
	r.scope = newScope(saved)
	defer func() { r.scope = saved }()

	scope := r.scope
	r.scope.set("super", funcValue{"super", func([]any, map[string]any) (any, error) {
		if idx+1 >= len(defs) {
			return "", nil
		}
		sub := r.sub()
		sub.scope = scope
		if err := sub.renderBlockAt(name, defs, idx+1); err != nil {
			return nil, err
		}
		return sub.out.String(), nil
	}})

	return r.renderNodes(defs[idx].Body)
}

func (r *renderer) renderInclude(t *IncludeNode) error {
	names, err := r.templateNames(t.Name, t.Pos())
	if err != nil {
		return err
	}
	if r.includeDepth >= r.opts.MaxIncludeDepth {
		return errorf(t.Pos(), "include and import nesting deeper than %d levels", r.opts.MaxIncludeDepth)
	}

	var lastErr error
	for _, name := range names {
		tpl, err := r.env.load(name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				lastErr = err
				continue
			}
			return errorf(t.Pos(), "include %q: %v", name, err)
		}
		sub := r.sub()
		sub.includeDepth = r.includeDepth + 1
		if t.WithContext {
			sub.scope = newScope(r.scope)
		} else {
			sub.scope = newScope(nil)
			for k, v := range r.env.Globals {
				sub.scope.set(k, v)
			}
		}
		if err := sub.renderRoot(tpl); err != nil {
			return err
		}
		return r.write(sub.out.String(), t.Pos())
	}

	if t.IgnoreMissing {
		return nil
	}
	return errorf(t.Pos(), "include %s: %v", strings.Join(names, ", "), lastErr)
}

// templateNames resolves the operand of include, import, or extends, which
// may be a single name or a list of candidates.
func (r *renderer) templateNames(e Expr, pos Pos) ([]string, error) {
	v, err := r.eval(e)
	if err != nil {
		return nil, err
	}
	switch t := v.(type) {
	case string:
		return []string{t}, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, errorf(pos, "a template name must be a string, found %s", typeName(item))
			}
			out = append(out, s)
		}
		return out, nil
	}
	s, err := r.toStrErr(v, pos)
	if err != nil {
		return nil, err
	}
	return []string{s}, nil
}

// importModule renders a template for its definitions and returns them as
// a mapping, which is what `{% import 'x' as y %}` binds.
func (r *renderer) importModule(e Expr, withContext bool, pos Pos) (*value.Map, error) {
	names, err := r.templateNames(e, pos)
	if err != nil {
		return nil, err
	}
	if r.includeDepth >= r.opts.MaxIncludeDepth {
		return nil, errorf(pos, "include and import nesting deeper than %d levels", r.opts.MaxIncludeDepth)
	}
	var lastErr error
	for _, name := range names {
		tpl, err := r.env.load(name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				lastErr = err
				continue
			}
			return nil, errorf(pos, "import %q: %v", name, err)
		}
		sub := r.sub()
		sub.includeDepth = r.includeDepth + 1
		if withContext {
			sub.scope = newScope(r.scope)
		} else {
			sub.scope = newScope(nil)
			for k, v := range r.env.Globals {
				sub.scope.set(k, v)
			}
		}
		if err := sub.renderRoot(tpl); err != nil {
			return nil, err
		}
		m := value.NewMap(len(sub.scope.vars))
		for _, k := range sortedKeysOf(sub.scope.vars) {
			m.Set(k, sub.scope.vars[k])
		}
		return m, nil
	}
	return nil, errorf(pos, "import %s: %v", strings.Join(names, ", "), lastErr)
}

func (r *renderer) renderImport(t *ImportNode) error {
	m, err := r.importModule(t.Name, t.WithContext, t.Pos())
	if err != nil {
		return err
	}
	r.scope.set(t.As, m)
	return nil
}

func (r *renderer) renderFromImport(t *FromImportNode) error {
	m, err := r.importModule(t.Name, t.WithContext, t.Pos())
	if err != nil {
		return err
	}
	for _, im := range t.Names {
		v, ok := m.Get(im.Name)
		if !ok {
			return errorf(t.Pos(), "the imported template does not define %q", im.Name)
		}
		r.scope.set(im.As, v)
	}
	return nil
}

// callerKey carries the {% call %} body into the macro being invoked. It
// is not a valid template identifier, so no template can collide with it.
const callerKey = "\x00caller"

func (r *renderer) renderCall(t *CallNode) error {
	call, ok := t.Call.(*CallExpr)
	if !ok {
		return errorf(t.Pos(), "{%% call %%} must name a macro invocation")
	}
	// The caller closes over the scope at the call site, and is bound
	// inside the macro's scope rather than the caller's, because a macro
	// body resolves names against where the macro was defined.
	caller := &Macro{
		Name:   "caller",
		Params: t.Params,
		Body:   t.Body,
		scope:  r.scope,
		r:      r,
	}

	fn, err := r.eval(call.Fn)
	if err != nil {
		return err
	}
	args, kwargs, err := r.evalCallArgs(call)
	if err != nil {
		return err
	}
	kwargs[callerKey] = caller

	v, err := r.callValue(fn, args, kwargs, t.Pos())
	if err != nil {
		return err
	}
	s, err := r.toStrErr(v, t.Pos())
	if err != nil {
		return err
	}
	return r.write(s, t.Pos())
}

// ---- macros ----

// Macro is a callable defined by `{% macro %}` or by `{% call %}`.
type Macro struct {
	Name   string
	Params []Param
	Body   []Node
	pos    Pos
	scope  *scope
	r      *renderer
}

func (r *renderer) makeMacro(t *MacroNode) *Macro {
	return &Macro{Name: t.Name, Params: t.Params, Body: t.Body, pos: t.Pos(), scope: r.scope, r: r}
}

// Call renders the macro body with its parameters bound.
func (m *Macro) Call(args []any, kwargs map[string]any) (any, error) {
	sub := m.r.sub()
	// A macro that calls itself is recursion, and the depth limit of SPEC
	// section 10.2.8 has to see it. Nothing counted macro calls, so
	// `{% macro m %}{{ m() }}{% endmacro %}{{ m() }}` ran until the
	// goroutine stack gave out: a template could crash the node rather
	// than get a named error.
	sub.budget.callDepth++
	defer func() { sub.budget.callDepth-- }()
	if sub.budget.callDepth > sub.opts.MaxDepth {
		return nil, errorf(m.pos, "macro %q recursed deeper than %d levels", m.Name, sub.opts.MaxDepth)
	}
	sub.scope = newScope(m.scope)

	if c, ok := kwargs[callerKey]; ok {
		sub.scope.set("caller", c)
	}

	bound := map[string]bool{callerKey: true}
	for i, p := range m.Params {
		switch {
		case i < len(args):
			sub.scope.set(p.Name, args[i])
			bound[p.Name] = true
		default:
			if v, ok := kwargs[p.Name]; ok {
				sub.scope.set(p.Name, v)
				bound[p.Name] = true
				continue
			}
			if p.Default != nil {
				v, err := m.r.eval(p.Default)
				if err != nil {
					return nil, err
				}
				sub.scope.set(p.Name, v)
				bound[p.Name] = true
				continue
			}
			sub.scope.set(p.Name, Undefined{
				Name: p.Name,
				Hint: fmt.Sprintf("macro %s was called without it", m.Name),
			})
		}
	}

	// varargs and kwargs, which Jinja exposes to every macro.
	extra := []any{}
	if len(args) > len(m.Params) {
		extra = args[len(m.Params):]
	}
	sub.scope.set("varargs", extra)
	kw := value.NewMap(len(kwargs))
	for _, k := range sortedKeysOf(kwargs) {
		if !bound[k] {
			kw.Set(k, kwargs[k])
		}
	}
	sub.scope.set("kwargs", kw)

	if err := sub.renderNodes(m.Body); err != nil {
		return nil, err
	}
	return sub.out.String(), nil
}

// ---- expressions ----

func (r *renderer) eval(e Expr) (any, error) {
	if err := r.budget.tick(e.Pos()); err != nil {
		return nil, err
	}
	switch t := e.(type) {
	case *LiteralExpr:
		return t.Val, nil

	case *NameExpr:
		if v, ok := r.scope.lookup(t.Name); ok {
			return v, nil
		}
		return Undefined{Name: t.Name, Pos: t.Pos()}, nil

	case *AttrExpr:
		obj, err := r.eval(t.Obj)
		if err != nil {
			return nil, err
		}
		return r.getAttr(obj, t.Attr, t.Pos())

	case *ItemExpr:
		obj, err := r.eval(t.Obj)
		if err != nil {
			return nil, err
		}
		key, err := r.eval(t.Index)
		if err != nil {
			return nil, err
		}
		return r.getItem(obj, key, t.Pos())

	case *SliceExpr:
		return r.evalSlice(t)

	case *CallExpr:
		return r.evalCall(t)

	case *FilterExpr:
		obj, err := r.eval(t.Obj)
		if err != nil {
			return nil, err
		}
		return r.applyFilter(t, obj)

	case *TestExpr:
		return r.applyTest(t)

	case *BinaryExpr:
		return r.evalBinary(t)

	case *UnaryExpr:
		x, err := r.eval(t.X)
		if err != nil {
			return nil, err
		}
		if t.Op == "not" {
			return !truthy(x), nil
		}
		if u, ok := x.(Undefined); ok {
			if err := r.undefinedError(u, t.Pos()); err != nil {
				return nil, err
			}
			return int64(0), nil
		}
		if i, ok := asInt(x); ok {
			return -i, nil
		}
		if f, ok := asFloat(x); ok {
			return -f, nil
		}
		return nil, errorf(t.Pos(), "cannot negate %s", typeName(x))

	case *CondExpr:
		cond, err := r.eval(t.Cond)
		if err != nil {
			return nil, err
		}
		if truthy(cond) {
			return r.eval(t.True)
		}
		if t.False == nil {
			return Undefined{Name: "conditional expression", Pos: t.Pos(),
				Hint: "the condition was false and there is no else branch"}, nil
		}
		return r.eval(t.False)

	case *ListExpr:
		out := make([]any, len(t.Items))
		for i, item := range t.Items {
			v, err := r.eval(item)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil

	case *TupleExpr:
		out := make(Tuple, len(t.Items))
		for i, item := range t.Items {
			v, err := r.eval(item)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil

	case *DictExpr:
		m := value.NewMap(len(t.Keys))
		for i, k := range t.Keys {
			kv, err := r.eval(k)
			if err != nil {
				return nil, err
			}
			vv, err := r.eval(t.Vals[i])
			if err != nil {
				return nil, err
			}
			m.Set(normalizeKey(kv), vv)
		}
		return m, nil
	}
	return nil, errorf(e.Pos(), "unhandled expression %T", e)
}

func (r *renderer) evalBinary(t *BinaryExpr) (any, error) {
	// Short-circuit, so that `x is defined and x.y` is safe.
	if t.Op == "and" || t.Op == "or" {
		l, err := r.eval(t.L)
		if err != nil {
			return nil, err
		}
		lt := truthy(l)
		if t.Op == "and" && !lt {
			return l, nil
		}
		if t.Op == "or" && lt {
			return l, nil
		}
		return r.eval(t.R)
	}

	l, err := r.eval(t.L)
	if err != nil {
		return nil, err
	}
	rv, err := r.eval(t.R)
	if err != nil {
		return nil, err
	}
	return r.binary(t.Op, l, rv, t.Pos())
}

func (r *renderer) evalSlice(t *SliceExpr) (any, error) {
	obj, err := r.eval(t.Obj)
	if err != nil {
		return nil, err
	}
	obj = untuple(obj)
	idx := func(e Expr, def int) (int, error) {
		if e == nil {
			return def, nil
		}
		v, err := r.eval(e)
		if err != nil {
			return 0, err
		}
		n, ok := asInt(v)
		if !ok {
			return 0, errorf(t.Pos(), "a slice bound must be an integer, found %s", typeName(v))
		}
		return int(n), nil
	}

	var length int
	var isStr bool
	var items []any
	var runes []rune
	switch s := obj.(type) {
	case []any:
		items, length = s, len(s)
	case string:
		runes = []rune(s)
		length, isStr = len(runes), true
	case Undefined:
		return nil, r.undefinedError(s, t.Pos())
	default:
		return nil, errorf(t.Pos(), "cannot slice %s", typeName(obj))
	}

	step, err := idx(t.Step, 1)
	if err != nil {
		return nil, err
	}
	if step == 0 {
		return nil, errorf(t.Pos(), "a slice step cannot be zero")
	}
	defStart, defStop := 0, length
	if step < 0 {
		defStart, defStop = length-1, -1
	}
	start, err := idx(t.Start, defStart)
	if err != nil {
		return nil, err
	}
	stop, err := idx(t.Stop, defStop)
	if err != nil {
		return nil, err
	}
	if t.Start != nil && start < 0 {
		start += length
	}
	if t.Stop != nil && stop < 0 {
		stop += length
	}
	clamp := func(n, lo, hi int) int {
		if n < lo {
			return lo
		}
		if n > hi {
			return hi
		}
		return n
	}
	if step > 0 {
		start, stop = clamp(start, 0, length), clamp(stop, 0, length)
	} else {
		start, stop = clamp(start, -1, length-1), clamp(stop, -1, length-1)
	}

	if isStr {
		var b strings.Builder
		for i := start; (step > 0 && i < stop) || (step < 0 && i > stop); i += step {
			b.WriteRune(runes[i])
		}
		return b.String(), nil
	}
	out := []any{}
	for i := start; (step > 0 && i < stop) || (step < 0 && i > stop); i += step {
		out = append(out, items[i])
	}
	return out, nil
}

func (r *renderer) evalCall(t *CallExpr) (any, error) {
	fn, err := r.eval(t.Fn)
	if err != nil {
		return nil, err
	}
	args, kwargs, err := r.evalCallArgs(t)
	if err != nil {
		return nil, err
	}
	return r.callValue(fn, args, kwargs, t.Pos())
}

// evalCallArgs evaluates a call site's positional arguments, keyword
// arguments, and any *args or **kwargs unpacking.
func (r *renderer) evalCallArgs(t *CallExpr) ([]any, map[string]any, error) {
	args := make([]any, 0, len(t.Args))
	for _, a := range t.Args {
		v, err := r.eval(a)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, v)
	}
	if t.StarArgs != nil {
		v, err := r.eval(t.StarArgs)
		if err != nil {
			return nil, nil, err
		}
		extra, ok := v.([]any)
		if !ok {
			return nil, nil, errorf(t.Pos(), "*args must unpack a sequence, found %s", typeName(v))
		}
		args = append(args, extra...)
	}

	kwargs := map[string]any{}
	for _, kw := range t.Kwargs {
		v, err := r.eval(kw.Val)
		if err != nil {
			return nil, nil, err
		}
		kwargs[kw.Name] = v
	}
	if t.StarKw != nil {
		v, err := r.eval(t.StarKw)
		if err != nil {
			return nil, nil, err
		}
		m, ok := v.(*value.Map)
		if !ok {
			return nil, nil, errorf(t.Pos(), "**kwargs must unpack a mapping, found %s", typeName(v))
		}
		for _, e := range m.Entries() {
			kwargs[value.KeyString(e.Key)] = e.Val
		}
	}
	return args, kwargs, nil
}
