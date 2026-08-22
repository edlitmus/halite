// Package migrate audits an existing Salt tree and reports what halite can
// compile as written, what has to be translated, and what it does not
// support at all.
//
// It produces a report, not a rewritten tree, because a tool that silently
// rewrites a production state tree is not trustworthy. SPEC section 28.5.
//
// The report is meant to run in CI against the existing tree from day one
// of the project, so the compatibility target is measured continuously
// rather than guessed.
package migrate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/regexcompat"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// Category names a kind of finding, and is what the effort estimate counts.
type Category string

const (
	// CatRenderer is an SLS file whose renderer pipeline is unsupported.
	CatRenderer Category = "renderer"
	// CatModule is a module or function that does not ship.
	CatModule Category = "module"
	// CatRegex is a pattern using a construct RE2 lacks.
	CatRegex Category = "regex"
	// CatYAML is a duplicate key, a Python tag, a YAML 1.1 coercion, or
	// tab indentation.
	CatYAML Category = "yaml"
	// CatUndefined is a name that would fail under strict undefined.
	CatUndefined Category = "undefined"
	// CatCustomModule is a Python file under _modules/ and friends.
	CatCustomModule Category = "custom_module"
	// CatPillarGrain is a pillar top entry targeting an untrusted grain.
	CatPillarGrain Category = "pillar_grain"
	// CatConfig is a configuration key that needs translation or review.
	CatConfig Category = "config"
	// CatACL is a Salt authorization key that needs a drafted RBAC rule.
	CatACL Category = "acl"
	// CatParse is a file that would not parse at all.
	CatParse Category = "parse"
	// CatState is a state declaration this build cannot run.
	CatState Category = "state"
)

// Severity says whether a finding blocks a migration.
type Severity string

const (
	// Blocking means the tree cannot run until it is addressed.
	Blocking Severity = "blocking"
	// Review means it will run but the behaviour may differ.
	Review Severity = "review"
	// Note is informational.
	Note Severity = "note"
)

// Finding is one item in the report.
type Finding struct {
	Category Category
	Severity Severity
	File     string
	Line     int
	Col      int
	// Subject is the construct: a renderer name, a module.function, a
	// regex snippet, a configuration key.
	Subject string
	Msg     string
	// Action is what to do about it.
	Action string
}

func (f Finding) String() string {
	loc := f.File
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	s := fmt.Sprintf("%-8s %-14s %s: %s", f.Severity, f.Category, loc, f.Msg)
	if f.Action != "" {
		s += "\n           " + f.Action
	}
	return s
}

// Report is the whole audit.
type Report struct {
	Root string
	// Files counted by kind.
	SLSFiles    int
	PillarFiles int
	Findings    []Finding
	// Renderers counts how many files used each renderer pipeline.
	Renderers map[string]int
	// Modules counts every module.function referenced, whether or not it
	// ships.
	Modules map[string]int
	// CustomModules lists the Python extension directories found.
	CustomModules []string
	// DefaultShell records that the audit assumed cmd_default_shell, so
	// the report can say the tree depends on it.
	DefaultShell bool
	// ShellLines counts the cmd states whose name reads as a shell line,
	// whether or not they were reported. They are only work to do when
	// `cmd_default_shell` is off, and they are worth counting either
	// way: with it on, the tree depends on the setting staying on.
	ShellLines int
}

// Options control an audit.
type Options struct {
	// Root is the tree to audit.
	Root string
	// PillarRoot is audited with pillar rules if it is separate from Root.
	PillarRoot string
	// ConfigFiles are Salt configuration files to translate.
	ConfigFiles []string
	// Registry is the set of module functions this build ships. An empty
	// registry reports module usage without judging it, which is what the
	// phase 0 skeleton does before the modules exist.
	Registry *signature.Registry
	// StateRegistry is the set of state functions this build ships. A nil
	// registry skips the declaration audit rather than reporting every
	// state as unknown.
	StateRegistry *signature.Registry
	// DefaultShell says the nodes applying this tree will set
	// `cmd_default_shell`, which is the transition of SPEC 15.2. With it
	// the shell lines a tree carries are not work to do; without it they
	// are the most common thing an unconverted tree gets wrong.
	DefaultShell bool
	// TrustedGrains is the pillar targeting allowlist to check against.
	// Empty means SPEC section 12.4's default.
	TrustedGrains []string
}

// DefaultTrustedGrains is the allowlist of SPEC section 12.4.
var DefaultTrustedGrains = []string{
	"id", "os", "os_family", "osrelease", "kernel", "cpuarch", "virtual", "fips_mode",
}

// customModuleDirs are the Salt extension directories whose contents
// cannot be ported without conversion. SPEC section 24.6.
var customModuleDirs = []string{
	"_modules", "_states", "_grains", "_beacons", "_returners",
	"_pillar", "_runners", "_renderers", "_utils", "_engines",
	"_proxy", "_output", "_sdb", "_tops", "_queues", "_serializers",
	"_executors", "_thorium", "_tokens", "_wrapper", "_roster", "_matchers",
	"_auth", "_cache", "_fileserver", "_log_handlers", "_wheel",
}

// Run audits a tree.
func Run(opts Options) (*Report, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("migrate: a tree to audit is required")
	}
	rep := &Report{
		Root:         opts.Root,
		Renderers:    map[string]int{},
		Modules:      map[string]int{},
		DefaultShell: opts.DefaultShell,
	}
	trusted := opts.TrustedGrains
	if len(trusted) == 0 {
		trusted = DefaultTrustedGrains
	}

	if err := auditTree(rep, opts, opts.Root, false, trusted); err != nil {
		return nil, err
	}
	if opts.PillarRoot != "" && opts.PillarRoot != opts.Root {
		if err := auditTree(rep, opts, opts.PillarRoot, true, trusted); err != nil {
			return nil, err
		}
	}
	for _, cf := range opts.ConfigFiles {
		auditConfig(rep, cf)
	}

	sort.SliceStable(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return rep, nil
}

func auditTree(rep *Report, opts Options, root string, isPillar bool, trusted []string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			base := d.Name()
			for _, cd := range customModuleDirs {
				if base == cd {
					auditCustomModuleDir(rep, root, path, rel)
					return filepath.SkipDir
				}
			}
			return nil
		}
		if filepath.Ext(path) != ".sls" {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isPillar {
			rep.PillarFiles++
		} else {
			rep.SLSFiles++
		}
		auditSLS(rep, opts, rel, src, isPillar, trusted)
		return nil
	})
}

func auditCustomModuleDir(rep *Report, root, path, rel string) {
	rep.CustomModules = append(rep.CustomModules, rel)
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	kind := filepath.Base(path)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".py" {
			continue
		}
		rep.Findings = append(rep.Findings, Finding{
			Category: CatCustomModule,
			Severity: Blocking,
			File:     filepath.ToSlash(filepath.Join(rel, e.Name())),
			Subject:  kind,
			Msg: fmt.Sprintf("a Python %s cannot be loaded; halite has no in-process plugin loading",
				strings.TrimPrefix(kind, "_")),
			Action: "Port it to a signed, pinned, out-of-process bridged extension. SPEC sections 24 and 24.6.",
		})
	}
}

// auditSLS checks one file. It renders and parses without executing, which
// is the same path `lint` takes.
func auditSLS(rep *Report, opts Options, rel string, src []byte, isPillar bool, trusted []string) {
	stages, body := render.ParsePipeline(string(src))
	pipeline := strings.Join(stages, "|")
	rep.Renderers[pipeline]++

	for _, stage := range stages {
		info, known := render.Stages[stage]
		switch {
		case !known:
			rep.Findings = append(rep.Findings, Finding{
				Category: CatRenderer, Severity: Blocking, File: rel, Subject: stage,
				Msg:    fmt.Sprintf("unknown renderer %q", stage),
				Action: "The supported set is in SPEC section 10.",
			})
		case info.Support == render.Unsupported:
			rep.Findings = append(rep.Findings, Finding{
				Category: CatRenderer, Severity: Blocking, File: rel, Subject: stage,
				Msg:    fmt.Sprintf("the %s renderer is not supported: %s", stage, info.Note),
				Action: "Rewrite as jinja|yaml, move the logic into a bridged module, or use the exec renderer. SPEC section 28.4.",
			})
		case info.Support == render.Bridged:
			// Blocking rather than review: no build ships the bridge yet,
			// so a file naming one of these does not render. Calling it
			// review said the tree could be applied once the item was
			// "understood", and it cannot be applied at all.
			rep.Findings = append(rep.Findings, Finding{
				Category: CatRenderer, Severity: Blocking, File: rel, Subject: stage,
				Msg: fmt.Sprintf("the %s renderer runs as a bridged extension, and no build ships the bridge yet: %s",
					stage, info.Note),
				Action: "This file will not render. Decrypt the values into the tree, or wait for the bridge. SPEC section 24.",
			})
		}
	}

	auditTemplate(rep, opts, rel, body)
	auditYAMLText(rep, rel, body)
	// A top file's keys are target expressions, not state IDs.
	if !isPillar && !strings.HasSuffix(rel, "top.sls") {
		auditDeclarations(rep, opts, rel, body)
	}
	if isPillar && strings.HasSuffix(rel, "top.sls") {
		auditPillarTop(rep, rel, body, trusted)
	}
}

// auditTemplate parses the template without rendering it, so that a tree
// referencing pillar values this audit does not have still reports its
// module usage and its regex constructs.
func auditTemplate(rep *Report, opts Options, rel, body string) {
	env := template.NewEnvironment(nil, template.DefaultOptions())
	tpl, err := env.Parse(body, rel)
	if err != nil {
		te, ok := err.(*template.Error)
		f := Finding{Category: CatParse, Severity: Blocking, File: rel, Msg: err.Error()}
		if ok {
			f.Line, f.Col, f.Msg = te.Pos.Line, te.Pos.Col, te.Msg
		}
		rep.Findings = append(rep.Findings, f)
		return
	}

	walkExprs(tpl.Body, func(e template.Expr) {
		auditExpr(rep, opts, rel, e)
	})
}

// auditExpr records module usage and unsupported regex constructs.
func auditExpr(rep *Report, opts Options, rel string, e template.Expr) {
	switch t := e.(type) {
	case *template.ItemExpr:
		// salt['module.function']
		name, ok := saltDispatchName(t)
		if ok {
			recordModule(rep, opts, rel, name, t.Pos().Line)
		}
	case *template.AttrExpr:
		if name, ok := saltAttrChain(t); ok {
			recordModule(rep, opts, rel, name, t.Pos().Line)
		}
	case *template.FilterExpr:
		if !strings.HasPrefix(t.Name, "regex_") && t.Name != "match" {
			return
		}
		for _, a := range t.Args {
			lit, ok := a.(*template.LiteralExpr)
			if !ok {
				continue
			}
			pattern, ok := lit.Val.(string)
			if !ok {
				continue
			}
			for _, c := range regexcompat.Unsupported(pattern) {
				rep.Findings = append(rep.Findings, Finding{
					Category: CatRegex, Severity: Blocking, File: rel, Line: t.Pos().Line,
					Subject: c.Syntax,
					Msg:     fmt.Sprintf("pattern %q uses %s, which RE2 does not support", pattern, c.Name),
					Action:  c.Workaround + ". SPEC section 10.4.",
				})
			}
		}
	}
}

func recordModule(rep *Report, opts Options, rel, name string, line int) {
	rep.Modules[name]++
	if opts.Registry == nil {
		return
	}
	if opts.Registry.Has(name) {
		return
	}
	rep.Findings = append(rep.Findings, Finding{
		Category: CatModule, Severity: Blocking, File: rel, Line: line, Subject: name,
		Msg:    fmt.Sprintf("%s is not a function this build ships", name),
		Action: "Check the tier table in SPEC section 15, or provide it as a bridged extension.",
	})
}

// saltDispatchName recognises salt['module.function'].
func saltDispatchName(t *template.ItemExpr) (string, bool) {
	name, ok := t.Obj.(*template.NameExpr)
	if !ok || name.Name != "salt" {
		return "", false
	}
	lit, ok := t.Index.(*template.LiteralExpr)
	if !ok {
		return "", false
	}
	s, ok := lit.Val.(string)
	if !ok || !strings.Contains(s, ".") {
		return "", false
	}
	return s, true
}

// saltAttrChain recognises salt.module.function.
func saltAttrChain(t *template.AttrExpr) (string, bool) {
	mid, ok := t.Obj.(*template.AttrExpr)
	if !ok {
		return "", false
	}
	base, ok := mid.Obj.(*template.NameExpr)
	if !ok || base.Name != "salt" {
		return "", false
	}
	return mid.Attr + "." + t.Attr, true
}

// auditYAMLText parses the file with templating stripped, so that the YAML
// hazards are found even when the tree's pillar is not available.
func auditYAMLText(rep *Report, rel, body string) {
	stripped := stripTemplating(body)
	opts := yaml.DefaultOptions(rel)
	opts.AllowDuplicateKeys = true // collect them all rather than stopping
	_, warns, err := yaml.Parse([]byte(stripped), opts)

	for _, w := range warns {
		sev, action := Review, ""
		switch w.Kind {
		case yaml.WarnDuplicateKey:
			// PyYAML's silent last-wins is a frequent and invisible cause
			// of a state that does nothing, so this is not a style note.
			sev = Blocking
			action = "Remove one of the two definitions. SPEC section 10.1.2 makes a duplicate key an error."
		case yaml.WarnBool11:
			action = "Quote the value, or set yaml_bool_11 to false once the tree has been audited. SPEC section 10.1.3."
		case yaml.WarnOctalImplicit:
			action = "Quote the value; an unquoted 0644 is read as the decimal 420. SPEC section 10.1.3."
		case yaml.WarnSexagesimal:
			action = "Quote the value; halite reads it as a string where YAML 1.1 would read a number. SPEC section 10.1.2."
		}
		rep.Findings = append(rep.Findings, Finding{
			Category: CatYAML, Severity: sev, File: rel, Line: w.Pos.Line, Col: w.Pos.Col,
			Subject: w.Kind.String(),
			Msg:     w.Msg,
			Action:  action,
		})
	}
	if err == nil {
		return
	}
	ye, ok := err.(*yaml.Error)
	f := Finding{Category: CatYAML, Severity: Blocking, File: rel, Msg: err.Error()}
	if ok {
		f.Line, f.Col, f.Msg = ye.Pos.Line, ye.Pos.Col, ye.Msg
		if strings.Contains(ye.Msg, "unsupported tag") {
			f.Action = "halite constructs only the nine types of SPEC section 10.1.1; a Python tag is refused by design."
		}
	}
	// With templating stripped the file may legitimately not parse, so
	// only the hazards this audit is sure about are reported as blocking.
	if !isDefiniteYAMLHazard(f.Msg) {
		return
	}
	rep.Findings = append(rep.Findings, f)
}

func isDefiniteYAMLHazard(msg string) bool {
	for _, s := range []string{
		"duplicate mapping key", "unsupported tag", "tab character used for indentation",
		"cannot be used as a key", "not valid UTF-8",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// stripTemplating blanks out template tags while preserving line and
// column positions, so the YAML pass reports positions that still match
// the file on disk.
func stripTemplating(src string) string {
	out := []byte(src)
	expressions := 0
	blank := func(from, to int) {
		for i := from; i < to && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	for i := 0; i < len(src); i++ {
		var closeTag string
		// An expression yields text and a statement yields nothing, so
		// only the expression leaves a mark. Blanking `{{ sls }} create
		// jail:` to spaces moved the key ten columns to the right, which
		// made the file stop parsing and took every finding in it with
		// the file. A placeholder keeps the key where its author put it.
		placeholder := false
		switch {
		case strings.HasPrefix(src[i:], "{{"):
			closeTag, placeholder = "}}", true
		case strings.HasPrefix(src[i:], "{%"):
			closeTag = "%}"
		case strings.HasPrefix(src[i:], "{#"):
			closeTag = "#}"
		default:
			continue
		}
		end := strings.Index(src[i:], closeTag)
		if end < 0 {
			blank(i, len(src))
			break
		}
		width := end + len(closeTag)
		blank(i, i+width)
		if placeholder {
			// Each expression gets its own token. Two state IDs built
			// from different expressions are different keys, and giving
			// them the same placeholder reported a duplicate key that
			// the file does not have.
			token := "x" + strconv.FormatInt(int64(expressions), 36)
			expressions++
			for j := 0; j < len(token) && i+j < len(out) && j < width; j++ {
				if out[i+j] == '\n' {
					break
				}
				out[i+j] = token[j]
			}
		}
		i += end + len(closeTag) - 1
	}
	return string(out)
}

// auditPillarTop reports every pillar top entry targeting a grain that is
// not in the trusted allowlist, so that SPEC section 12.4 can be
// configured deliberately rather than discovered during an incident.
func auditPillarTop(rep *Report, rel, body string, trusted []string) {
	stripped := stripTemplating(body)
	v, _, err := yaml.Parse([]byte(stripped), yaml.DefaultOptions(rel))
	if err != nil {
		return
	}
	top, ok := v.(*value.Map)
	if !ok {
		return
	}
	trustedSet := map[string]bool{}
	for _, g := range trusted {
		trustedSet[g] = true
	}

	for _, envEntry := range top.Entries() {
		targets, ok := envEntry.Val.(*value.Map)
		if !ok {
			continue
		}
		for _, tgt := range targets.Entries() {
			expr := value.KeyString(tgt.Key)
			grain, isGrainTarget := grainTargetName(expr)
			if !isGrainTarget || trustedSet[grain] {
				continue
			}
			rep.Findings = append(rep.Findings, Finding{
				Category: CatPillarGrain, Severity: Review, File: rel,
				Line: tgt.KeyPos.Line, Col: tgt.KeyPos.Col, Subject: grain,
				Msg:    fmt.Sprintf("pillar targets on the grain %q, which a node controls and which is not trusted by default", grain),
				Action: "Add it to pillar_trusted_grains as a recorded decision, or move the attribute to a hub-authoritative node attribute. SPEC section 12.4.",
			})
		}
	}
}

// grainTargetName extracts the grain from a `G@os_family:Debian` or a
// compound expression containing one.
func grainTargetName(expr string) (string, bool) {
	for _, prefix := range []string{"G@", "P@"} {
		i := strings.Index(expr, prefix)
		if i < 0 {
			continue
		}
		rest := expr[i+len(prefix):]
		if j := strings.IndexAny(rest, ":"); j > 0 {
			return rest[:j], true
		}
		if j := strings.IndexAny(rest, " "); j > 0 {
			return rest[:j], true
		}
		return rest, true
	}
	return "", false
}

func auditConfig(rep *Report, path string) {
	role := config.Node
	base := strings.ToLower(filepath.Base(path))
	if strings.Contains(base, "hub") || strings.Contains(base, "master") { // lexicon:allow
		role = config.Hub
	}
	cfg, err := config.Load(role, config.LoadOptions{Path: path, DropInDir: path + ".d"})
	if cfg == nil {
		rep.Findings = append(rep.Findings, Finding{
			Category: CatConfig, Severity: Blocking, File: path,
			Msg: fmt.Sprintf("could not be read: %v", err),
		})
		return
	}
	for _, r := range cfg.Shim.Translated {
		sev := Note
		action := fmt.Sprintf("halite calls it %q. The compatibility shim is removed in %s.", r.Halite, config.ShimRemovalVersion)
		cat := CatConfig
		if r.Halite == "policy" {
			sev, cat = Review, CatACL
			action = "Translated into a draft RBAC rule; review it before it is applied. SPEC section 23.5."
		}
		rep.Findings = append(rep.Findings, Finding{
			Category: cat, Severity: sev, File: path, Subject: r.Salt,
			Msg:    fmt.Sprintf("configuration key %q is the Salt name", r.Salt),
			Action: action,
		})
	}
	for _, r := range cfg.Shim.Refused {
		rep.Findings = append(rep.Findings, Finding{
			Category: CatConfig, Severity: Blocking, File: path, Subject: r.Salt,
			Msg:    fmt.Sprintf("configuration key %q has no halite equivalent", r.Salt),
			Action: r.Reason,
		})
	}
	for _, u := range cfg.Shim.Unknown {
		rep.Findings = append(rep.Findings, Finding{
			Category: CatConfig, Severity: Note, File: path, Subject: u,
			Msg:    fmt.Sprintf("configuration key %q is not recognised and would be ignored", u),
			Action: "Confirm it is obsolete, or open an issue if halite should honour it.",
		})
	}
}

// walkExprs visits every expression in a template body.
func walkExprs(nodes []template.Node, visit func(template.Expr)) {
	var walkExpr func(template.Expr)
	walkExpr = func(e template.Expr) {
		if e == nil {
			return
		}
		visit(e)
		switch t := e.(type) {
		case *template.AttrExpr:
			walkExpr(t.Obj)
		case *template.ItemExpr:
			walkExpr(t.Obj)
			walkExpr(t.Index)
		case *template.SliceExpr:
			walkExpr(t.Obj)
			walkExpr(t.Start)
			walkExpr(t.Stop)
			walkExpr(t.Step)
		case *template.CallExpr:
			walkExpr(t.Fn)
			for _, a := range t.Args {
				walkExpr(a)
			}
			for _, kw := range t.Kwargs {
				walkExpr(kw.Val)
			}
			walkExpr(t.StarArgs)
			walkExpr(t.StarKw)
		case *template.FilterExpr:
			walkExpr(t.Obj)
			for _, a := range t.Args {
				walkExpr(a)
			}
			for _, kw := range t.Kwargs {
				walkExpr(kw.Val)
			}
		case *template.TestExpr:
			walkExpr(t.Obj)
			for _, a := range t.Args {
				walkExpr(a)
			}
		case *template.BinaryExpr:
			walkExpr(t.L)
			walkExpr(t.R)
		case *template.UnaryExpr:
			walkExpr(t.X)
		case *template.CondExpr:
			walkExpr(t.Cond)
			walkExpr(t.True)
			walkExpr(t.False)
		case *template.ListExpr:
			for _, i := range t.Items {
				walkExpr(i)
			}
		case *template.TupleExpr:
			for _, i := range t.Items {
				walkExpr(i)
			}
		case *template.DictExpr:
			for i := range t.Keys {
				walkExpr(t.Keys[i])
				walkExpr(t.Vals[i])
			}
		}
	}

	var walkNodes func([]template.Node)
	walkNodes = func(ns []template.Node) {
		for _, n := range ns {
			switch t := n.(type) {
			case *template.OutputNode:
				walkExpr(t.Expr)
			case *template.IfNode:
				for _, c := range t.Conds {
					walkExpr(c)
				}
				for _, b := range t.Blocks {
					walkNodes(b)
				}
				walkNodes(t.Else)
			case *template.ForNode:
				walkExpr(t.Iter)
				walkExpr(t.Cond)
				walkNodes(t.Body)
				walkNodes(t.Else)
			case *template.SetNode:
				walkExpr(t.Value)
				walkExpr(t.Filter)
				walkNodes(t.Body)
			case *template.MacroNode:
				for _, p := range t.Params {
					walkExpr(p.Default)
				}
				walkNodes(t.Body)
			case *template.CallNode:
				walkExpr(t.Call)
				walkNodes(t.Body)
			case *template.IncludeNode:
				walkExpr(t.Name)
			case *template.ImportNode:
				walkExpr(t.Name)
			case *template.FromImportNode:
				walkExpr(t.Name)
			case *template.ExtendsNode:
				walkExpr(t.Name)
			case *template.BlockNode:
				walkNodes(t.Body)
			case *template.FilterNode:
				walkExpr(t.Filter)
				walkNodes(t.Body)
			case *template.DoNode:
				walkExpr(t.Expr)
			case *template.WithNode:
				for _, v := range t.Values {
					walkExpr(v)
				}
				walkNodes(t.Body)
			case *template.AutoescapeNode:
				walkNodes(t.Body)
			}
		}
	}
	walkNodes(nodes)
}
