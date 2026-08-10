package sls

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/yamlite"
)

// Loader resolves and loads SLS files from a state tree, following
// include: directives (deduplicated and cycle-safe) and producing a single
// sorted execution plan. Included states run before the including file's
// own states, matching Salt.
type Loader struct {
	Root    string // state tree root; required for includes and dotted names
	Grains  map[string]any
	Pillar  map[string]any
	Mine    map[string]any
	visited map[string]bool
}

func (l *Loader) templateData() TemplateData {
	return TemplateData{Grains: l.Grains, Pillar: l.Pillar, Mine: l.Mine}
}

func (l *Loader) init() {
	if l.visited == nil {
		l.visited = map[string]bool{}
	}
}

// ResolveName maps a dotted SLS name to a file under root: "web.nginx"
// becomes <root>/web/nginx.sls or <root>/web/nginx/init.sls. The pillar
// tree uses the same layout.
func ResolveName(root, name string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("sls name %q requires a root", name)
	}
	parts := strings.Split(name, ".")
	base := filepath.Join(append([]string{root}, parts...)...)
	for _, cand := range []string{base + ".sls", filepath.Join(base, "init.sls")} {
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("sls %q not found under %s", name, root)
}

func (l *Loader) resolveName(name string) (string, error) {
	return ResolveName(l.Root, name)
}

// LoadPath loads a single SLS file (plus its includes) and returns the
// sorted plan.
func (l *Loader) LoadPath(path string) ([]State, error) {
	l.init()
	states, err := l.loadFile(path)
	if err != nil {
		return nil, err
	}
	return sortStates(states)
}

// LoadNames loads dotted SLS names in order and returns the sorted plan.
func (l *Loader) LoadNames(names []string) ([]State, error) {
	l.init()
	var out []State
	for _, n := range names {
		p, err := l.resolveName(n)
		if err != nil {
			return nil, err
		}
		states, err := l.loadFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, states...)
	}
	return sortStates(out)
}

// LoadTop reads <root>/top.sls, matches its targets against grains, and
// loads the matched SLS names (highstate).
func (l *Loader) LoadTop() ([]State, error) {
	l.init()
	topPath := filepath.Join(l.Root, "top.sls")
	b, err := os.ReadFile(topPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", topPath, err)
	}
	rendered, err := Render("top.sls", string(b), l.templateData())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", topPath, err)
	}
	tree, err := yamlite.Parse(rendered)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", topPath, err)
	}
	names, err := MatchTop(tree, l.Grains)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", topPath, err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s: no targets matched this host", topPath)
	}
	return l.LoadNames(names)
}

// loadFile reads, renders, and compiles one SLS file, recursing into
// includes (which are emitted first). A file is loaded at most once.
func (l *Loader) loadFile(path string) ([]State, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if l.visited[abs] {
		return nil, nil
	}
	l.visited[abs] = true

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	rendered, err := Render(filepath.Base(path), string(b), l.templateData())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	tree, err := yamlite.Parse(rendered)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	states, includes, err := compileTree(tree)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(abs)
	for i := range states {
		states[i].Dir = dir
		states[i].Src = path
	}
	var out []State
	for _, inc := range includes {
		p, err := l.resolveName(inc)
		if err != nil {
			return nil, fmt.Errorf("%s: include: %w", path, err)
		}
		incStates, err := l.loadFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, incStates...)
	}
	return append(out, states...), nil
}
