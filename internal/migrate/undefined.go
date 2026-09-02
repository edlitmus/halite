package migrate

import (
	"fmt"
	"sort"

	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/template"
)

// auditUndefined reports every name a template reads that nothing
// defines.
//
// SPEC 28.5 asks the report for "every name that would fail under strict
// undefined, with file and line", and SPEC 10.2.6 makes strict the
// default — Salt renders an empty string instead, which is how a state
// comes to silently do the wrong thing. Until this existed no report
// could tell an estate what that change would cost it, which is the
// input SPEC 33 question 4 is waiting on.
//
// Decided statically rather than by rendering. Rendering would need this
// estate's pillar and grains, which the audit does not have, and would
// report every pillar value as undefined. A name that no scope binds and
// no context supplies fails under strict whatever the data holds, and
// that is exactly the set worth naming.
func auditUndefined(rep *Report, rel string, tpl *template.Template) {
	scope := newNameScope()
	walkForUndefined(rep, rel, tpl.Body, scope)
}

// nameScope is the set of names in view, innermost first.
type nameScope struct {
	bound  []map[string]bool
	global map[string]bool
}

func newNameScope() *nameScope {
	return &nameScope{bound: []map[string]bool{{}}, global: contextNames()}
}

func (s *nameScope) push() { s.bound = append(s.bound, map[string]bool{}) }
func (s *nameScope) pop()  { s.bound = s.bound[:len(s.bound)-1] }

func (s *nameScope) define(names ...string) {
	top := s.bound[len(s.bound)-1]
	for _, n := range names {
		top[n] = true
	}
}

func (s *nameScope) has(name string) bool {
	if s.global[name] {
		return true
	}
	for _, frame := range s.bound {
		if frame[name] {
			return true
		}
	}
	return false
}

// contextNames is every name a template is given without defining it:
// what the renderer puts in the context, and the callables Jinja exposes
// without an import.
//
// Read from the renderer and the template environment rather than
// listed here, because a list here would drift and every name missing
// from it becomes a finding against a tree that is fine.
func contextNames() map[string]bool {
	out := map[string]bool{}
	for _, name := range render.ContextNames() {
		out[name] = true
	}
	env := template.NewEnvironment(nil, template.DefaultOptions())
	for name := range env.Globals {
		out[name] = true
	}
	// A reactor SLS is a `.sls` like any other and nothing in the file
	// says which it is — the same problem the orchestration registry
	// had. Its two extra names are treated as defined everywhere, which
	// costs the detection of a state file that reads an undefined
	// `data`, and buys not reporting every reaction in the tree.
	for _, name := range hub.ReactionContextNames() {
		out[name] = true
	}
	// Bound by the constructs that introduce them rather than by the
	// context: `loop` inside a for, `caller` inside a call block, and
	// `self` for block reuse.
	out["loop"] = true
	out["caller"] = true
	out["self"] = true
	return out
}

// walkForUndefined walks statements, tracking what each construct binds.
func walkForUndefined(rep *Report, rel string, nodes []template.Node, scope *nameScope) {
	for _, n := range nodes {
		switch t := n.(type) {
		case *template.OutputNode:
			checkExpr(rep, rel, t.Expr, scope)

		case *template.IfNode:
			for _, c := range t.Conds {
				checkExpr(rep, rel, c, scope)
			}
			for _, b := range t.Blocks {
				walkForUndefined(rep, rel, b, scope)
			}
			walkForUndefined(rep, rel, t.Else, scope)

		case *template.ForNode:
			// The iterable is evaluated outside the loop's scope, so a
			// target may share a name with what it iterates.
			checkExpr(rep, rel, t.Iter, scope)
			scope.push()
			scope.define(t.Targets...)
			checkExpr(rep, rel, t.Cond, scope)
			walkForUndefined(rep, rel, t.Body, scope)
			scope.pop()
			walkForUndefined(rep, rel, t.Else, scope)

		case *template.SetNode:
			checkExpr(rep, rel, t.Value, scope)
			checkExpr(rep, rel, t.Filter, scope)
			walkForUndefined(rep, rel, t.Body, scope)
			// Defined after its own value, so `{% set x = x %}` reads the
			// outer x, and defined in the enclosing scope because that is
			// where a set is visible.
			if t.Namespace == "" {
				scope.define(t.Targets...)
			}

		case *template.MacroNode:
			scope.define(t.Name)
			scope.push()
			for _, p := range t.Params {
				checkExpr(rep, rel, p.Default, scope)
				scope.define(p.Name)
			}
			walkForUndefined(rep, rel, t.Body, scope)
			scope.pop()

		case *template.CallNode:
			checkExpr(rep, rel, t.Call, scope)
			scope.push()
			for _, p := range t.Params {
				scope.define(p.Name)
			}
			walkForUndefined(rep, rel, t.Body, scope)
			scope.pop()

		case *template.WithNode:
			for _, v := range t.Values {
				checkExpr(rep, rel, v, scope)
			}
			scope.push()
			scope.define(t.Targets...)
			walkForUndefined(rep, rel, t.Body, scope)
			scope.pop()

		case *template.ImportNode:
			checkExpr(rep, rel, t.Name, scope)
			if t.As != "" {
				scope.define(t.As)
			}

		case *template.FromImportNode:
			checkExpr(rep, rel, t.Name, scope)
			scope.define(fromImportNames(t)...)

		case *template.BlockNode:
			walkForUndefined(rep, rel, t.Body, scope)

		case *template.FilterNode:
			checkExpr(rep, rel, t.Filter, scope)
			walkForUndefined(rep, rel, t.Body, scope)

		case *template.AutoescapeNode:
			walkForUndefined(rep, rel, t.Body, scope)

		case *template.DoNode:
			checkExpr(rep, rel, t.Expr, scope)

		case *template.IncludeNode:
			checkExpr(rep, rel, t.Name, scope)
		}
	}
}

// checkExpr reports the bare names an expression reads that nothing has
// defined.
//
// Only the object end of an access is checked: in `pillar.web.port` the
// name is `pillar`, and whether it has a `web` is a question about this
// estate's data rather than about the template.
func checkExpr(rep *Report, rel string, e template.Expr, scope *nameScope) {
	if e == nil {
		return
	}
	seen := map[string]template.Pos{}
	walkExprs([]template.Node{&template.OutputNode{Expr: e}}, func(x template.Expr) {
		name, ok := x.(*template.NameExpr)
		if !ok || scope.has(name.Name) {
			return
		}
		if _, dup := seen[name.Name]; !dup {
			seen[name.Name] = name.Pos()
		}
	})

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pos := seen[n]
		rep.Findings = append(rep.Findings, Finding{
			// Blocking because strict is the default this build renders
			// with, so the file does not compile as written. It is not a
			// prediction about a setting somebody might turn on.
			Category: CatUndefined, Severity: Blocking, File: rel,
			Line: pos.Line, Col: pos.Col, Subject: n,
			Msg: fmt.Sprintf("%q is read and never defined, so this file does not "+
				"render: strict undefined is the default (SPEC 10.2.6), where Salt "+
				"substitutes an empty string", n),
			Action: "Define it, guard it with `is defined`, or run with " +
				"`permissive: true` for a transition, which restores Salt's reading " +
				"and logs each one. SPEC section 28.5.",
		})
	}
}

// fromImportNames is what `{% from "x" import a, b as c %}` binds.
func fromImportNames(t *template.FromImportNode) []string {
	out := make([]string, 0, len(t.Names))
	for _, n := range t.Names {
		if n.As != "" {
			out = append(out, n.As)
			continue
		}
		out = append(out, n.Name)
	}
	return out
}
