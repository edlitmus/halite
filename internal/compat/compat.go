// Package compat audits an existing Salt state or pillar tree and reports
// what halite can use as written, what has to be translated, and what it
// does not support at all.
//
// The report is mechanical, not advisory: every finding names a file, a
// line, and the construct that halite's renderer (Go text/template), its
// YAML subset (yamlite), or its module set does not accept. Findings carry
// one of three severities — an error is something halite will not do as
// written, a warning is something it loads and then ignores, and a note is
// a supported construct with a caveat.
package compat

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/extmod"
	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/yamlite"
)

// Severity ranks a finding by what halite actually does with the construct.
type Severity string

const (
	// SevError marks a construct halite will not run as written: it fails to
	// load, or it loads and does something different from Salt.
	SevError Severity = "error"
	// SevWarn marks a construct halite loads and then ignores.
	SevWarn Severity = "warn"
	// SevInfo marks a supported construct with a caveat worth knowing.
	SevInfo Severity = "info"
)

// Finding is one incompatibility, anchored to a file and (where the check
// is line-based) a line.
type Finding struct {
	File     string   `json:"file"`
	Line     int      `json:"line,omitempty"`
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	Hint     string   `json:"hint,omitempty"`
}

// Use records one "module.function" declaration and whether halite has it.
type Use struct {
	Name      string `json:"name"`
	Supported bool   `json:"supported"`
	External  bool   `json:"external,omitempty"`
	Count     int    `json:"count,omitempty"`
}

// FileReport is the outcome for one SLS file.
type FileReport struct {
	Path string `json:"path"` // relative to the tree root
	Kind string `json:"kind"` // state, top, pillar, pillar-top
	// Approximate is set when the file could only be analysed after its
	// Jinja and block scalars were stripped: the state inventory below is
	// what the file appears to declare, not what Salt would render.
	Approximate bool      `json:"approximate,omitempty"`
	Findings    []Finding `json:"findings,omitempty"`
	Uses        []Use     `json:"uses,omitempty"`
	// Matched is the SLS names a top file selects for the scanning host.
	Matched []string `json:"matched,omitempty"`
}

// TreeReport is the outcome for one root.
type TreeReport struct {
	Root  string       `json:"root"`
	Kind  string       `json:"kind"` // state or pillar
	Files []FileReport `json:"files"`
	// Findings that belong to the tree rather than to one SLS file: a Salt
	// extension directory, a Python module, a missing top file.
	Findings []Finding `json:"findings,omitempty"`
}

// Report is the whole audit: one entry per root that was scanned.
type Report struct {
	Trees  []TreeReport   `json:"trees"`
	Grains map[string]any `json:"-"`
}

// Totals counts a report by severity.
type Totals struct {
	Files       int `json:"files"`
	WithFinding int `json:"files_with_findings"`
	Approximate int `json:"files_approximate"`
	Errors      int `json:"errors"`
	Warnings    int `json:"warnings"`
	Notes       int `json:"notes"`
}

// Totals sums the report.
func (r Report) Totals() Totals {
	var t Totals
	count := func(fs []Finding) {
		for _, f := range fs {
			switch f.Severity {
			case SevError:
				t.Errors++
			case SevWarn:
				t.Warnings++
			default:
				t.Notes++
			}
		}
	}
	for _, tree := range r.Trees {
		count(tree.Findings)
		for _, f := range tree.Files {
			t.Files++
			if len(f.Findings) > 0 {
				t.WithFinding++
			}
			if f.Approximate {
				t.Approximate++
			}
			count(f.Findings)
		}
	}
	return t
}

// ModuleUsage returns every declared "module.function" with its occurrence
// count, unsupported ones first and then by descending count.
func (r Report) ModuleUsage() []Use {
	byName := map[string]Use{}
	for _, tree := range r.Trees {
		for _, f := range tree.Files {
			for _, u := range f.Uses {
				cur, ok := byName[u.Name]
				if !ok {
					cur = Use{Name: u.Name, Supported: u.Supported, External: u.External}
				}
				cur.Count++
				byName[u.Name] = cur
			}
		}
	}
	out := make([]Use, 0, len(byName))
	for _, u := range byName {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Supported != out[j].Supported {
			return !out[i].Supported
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Kind selects the semantics a tree is read with: state files declare
// states, pillar files are plain data.
type Kind int

const (
	KindState Kind = iota
	KindPillar
)

func (k Kind) String() string {
	if k == KindPillar {
		return "pillar"
	}
	return "state"
}

// Scanner audits trees against one host's view: SLS files are rendered
// with these grains and pillar, the way `halite apply` would render them.
type Scanner struct {
	Grains map[string]any
	Pillar map[string]any
}

// ScanTree walks root and reports on every SLS file under it. A missing
// root is an error; anything else that goes wrong with a single file
// becomes a finding rather than an error.
func (s *Scanner) ScanTree(root string, kind Kind) (TreeReport, error) {
	tr := TreeReport{Root: root, Kind: kind.String()}
	info, err := os.Stat(root)
	if err != nil {
		return tr, err
	}
	if !info.IsDir() {
		return tr, fmt.Errorf("%s is not a directory", root)
	}

	external := externalModules(root)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			tr.Findings = append(tr.Findings, Finding{
				File: relTo(root, path), Severity: SevWarn, Code: "unreadable",
				Message: err.Error(),
			})
			return nil
		}
		rel := relTo(root, path)
		if d.IsDir() {
			return s.enterDir(&tr, rel, d.Name())
		}
		if f, ok := nonSLSFinding(rel, d); ok {
			tr.Findings = append(tr.Findings, f)
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".sls") {
			return nil
		}
		tr.Files = append(tr.Files, s.scanFile(path, rel, kind, root, external))
		return nil
	})
	if walkErr != nil {
		return tr, walkErr
	}
	if kind == KindState && !hasFile(tr.Files, "top.sls") {
		tr.Findings = append(tr.Findings, Finding{
			File: "top.sls", Severity: SevWarn, Code: "no-top",
			Message: "no top.sls at the tree root",
			Hint:    "halite reads <root>/top.sls for a highstate; without one only named SLS files can be applied",
		})
	}
	sort.Slice(tr.Files, func(i, j int) bool { return tr.Files[i].Path < tr.Files[j].Path })
	return tr, nil
}

// enterDir decides whether to descend into a directory, reporting the Salt
// extension directories halite has no equivalent for.
func (s *Scanner) enterDir(tr *TreeReport, rel, name string) error {
	if rel == "." {
		return nil
	}
	if strings.HasPrefix(name, ".") {
		return fs.SkipDir
	}
	// Salt loads Python from _modules, _states, _grains and friends. halite
	// has one such directory, _modules, and it holds executables.
	if !strings.Contains(rel, string(filepath.Separator)) && strings.HasPrefix(name, "_") {
		if name == extmod.DirName {
			return nil
		}
		tr.Findings = append(tr.Findings, Finding{
			File: rel, Severity: SevError, Code: "salt-extension-dir",
			Message: fmt.Sprintf("%s/ holds Salt's Python extensions", name),
			Hint:    "halite loads no Python; the only extension point is _modules/, an executable per module that speaks JSON (docs/external-modules.md)",
		})
		return fs.SkipDir
	}
	return nil
}

// nonSLSFinding reports files that are neither SLS nor payload: Python
// extensions, Jinja templates, and _modules entries that cannot be run.
func nonSLSFinding(rel string, d fs.DirEntry) (Finding, bool) {
	inExtMod := strings.HasPrefix(rel, extmod.DirName+string(filepath.Separator))
	switch {
	case strings.HasSuffix(rel, ".py") && inExtMod:
		return Finding{
			File: rel, Severity: SevError, Code: "python-module",
			Message: "Python execution module",
			Hint:    "halite runs _modules/<name> as an executable with JSON on stdin and stdout; a Python file works only with a #! line and the exec bit",
		}, true
	case strings.HasSuffix(rel, ".jinja"), strings.HasSuffix(rel, ".j2"):
		return Finding{
			File: rel, Severity: SevError, Code: "jinja-template-file",
			Message: "Jinja template file",
			Hint:    "file.managed renders sources with Go text/template when given 'template: true'; translate the file and drop the Jinja syntax",
		}, true
	case inExtMod && !strings.Contains(strings.TrimPrefix(rel, extmod.DirName+string(filepath.Separator)), string(filepath.Separator)):
		if info, err := d.Info(); err == nil && info.Mode().Perm()&0o111 == 0 {
			return Finding{
				File: rel, Severity: SevWarn, Code: "module-not-executable",
				Message: "external module is not executable",
				Hint:    "chmod +x; halite runs the file directly",
			}, true
		}
	}
	return Finding{}, false
}

// ScanFile analyses a single SLS file. root is the tree the file's include
// names and top-file targets are resolved against; it is usually the file's
// own directory when there is no tree to speak of.
func (s *Scanner) ScanFile(path, root string, kind Kind) FileReport {
	return s.scanFile(path, filepath.Base(path), kind, root, externalModules(root))
}

// scanFile analyses one SLS file. Every failure is a finding, so a file
// that cannot be read or parsed still appears in the report.
func (s *Scanner) scanFile(path, rel string, kind Kind, root string, external map[string]bool) FileReport {
	fr := FileReport{Path: rel, Kind: fileKind(rel, kind)}
	raw, err := os.ReadFile(path)
	if err != nil {
		fr.Findings = append(fr.Findings, Finding{Severity: SevError, Code: "unreadable", Message: err.Error()})
		return fr
	}
	src := string(raw)
	fr.Findings = append(fr.Findings, scanText(src)...)

	// A file the text scan has already faulted will not render, or will
	// parse into something the constructs it uses do not mean. Both cases
	// read better from the neutralised text: the errors are reported once,
	// and the structure below them is at least the structure written.
	var tree any
	ok := false
	if hasError(fr.Findings) {
		tree, ok = parseNeutralized(src)
		fr.Approximate = ok
	} else {
		tree, ok = s.renderAndParse(&fr, src, rel)
		if !ok {
			tree, ok = parseNeutralized(src)
			fr.Approximate = ok
		}
	}
	if ok {
		s.structure(&fr, tree, kind, root, external)
	}
	for i := range fr.Findings {
		fr.Findings[i].File = rel
	}
	return fr
}

// renderAndParse runs the real pipeline — Go text/template, then yamlite —
// and reports where it stops.
func (s *Scanner) renderAndParse(fr *FileReport, src, name string) (any, bool) {
	rendered, err := sls.Render(name, src, sls.TemplateData{Grains: s.Grains, Pillar: s.Pillar})
	if err != nil {
		fr.Findings = append(fr.Findings, Finding{
			Severity: SevError, Code: "template-error",
			Message: strings.TrimPrefix(err.Error(), "template: "),
			Hint:    "halite renders SLS files with Go text/template; grains, pillar and mine are {{ .Grains.x }}, {{ .Pillar.x }} and {{ .Mine.x }}",
		})
		return nil, false
	}
	tree, err := yamlite.Parse(rendered)
	if err != nil {
		fr.Findings = append(fr.Findings, Finding{
			Severity: SevError, Code: "yaml-error", Message: err.Error(),
			Hint: "yamlite parses the YAML subset Salt files normally use: ordered mappings, block lists, scalars and comments",
		})
		return nil, false
	}
	return tree, true
}

// externalModules lists the module names provided by <root>/_modules.
func externalModules(root string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(root, extmod.DirName))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if ext := filepath.Ext(name); ext != "" {
			name = strings.TrimSuffix(name, ext)
		}
		out[name] = true
	}
	return out
}

func fileKind(rel string, kind Kind) string {
	top := filepath.Base(rel) == "top.sls"
	switch {
	case kind == KindPillar && top:
		return "pillar-top"
	case kind == KindPillar:
		return "pillar"
	case top:
		return "top"
	}
	return "state"
}

func relTo(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

func hasFile(files []FileReport, name string) bool {
	for _, f := range files {
		if f.Path == name {
			return true
		}
	}
	return false
}

func hasError(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SevError {
			return true
		}
	}
	return false
}
