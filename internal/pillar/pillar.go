// Package pillar loads targeted, host-specific data from a pillar tree and
// makes it available to state templates as {{ .Pillar.x }}.
//
// The tree mirrors the state tree: <root>/top.sls maps target patterns to
// pillar SLS names (same matcher as a state top file), and each named file
// contributes a data tree. Files are rendered with grains in scope, may
// include: other pillar files, and are deep-merged in declaration order —
// later files win on conflicting leaves.
package pillar

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/yamlite"
)

// PermissionWarning returns a non-empty message when the pillar tree is
// readable beyond its owner. halite does not encrypt pillar data — its
// confidentiality is the directory mode — so a tree every account on the
// host can read is worth saying out loud. It is a warning, not an error:
// running as a non-root user with a dedicated group is a legitimate setup.
//
// Callers report it; this package does not write to stderr. Windows modes
// do not carry unix permission bits, so it is a no-op there.
func PermissionWarning(root string) string {
	if runtime.GOOS == "windows" || root == "" {
		return ""
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "" // a missing tree is not a leak; Load reports it if it matters
	}
	mode := info.Mode().Perm()
	if mode&0o077 == 0 {
		return ""
	}
	return fmt.Sprintf(
		"warning: pillar tree %s is mode %04o; anyone on this host can read it (chmod 0700)",
		root, mode)
}

// Loader reads a pillar tree for one host.
type Loader struct {
	Root    string // pillar tree root
	Grains  map[string]any
	visited map[string]bool
}

// Load returns the merged pillar for this host. Pillar data is optional: a
// missing root or top file yields an empty pillar rather than an error.
func (l *Loader) Load() (map[string]any, error) {
	l.visited = map[string]bool{}
	out := map[string]any{}
	if l.Root == "" {
		return out, nil
	}
	topPath := filepath.Join(l.Root, "top.sls")
	b, err := os.ReadFile(topPath)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("read %s: %w", topPath, err)
	}
	rendered, err := sls.Render("top.sls", string(b), sls.TemplateData{Grains: l.Grains})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", topPath, err)
	}
	tree, err := yamlite.Parse(rendered)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", topPath, err)
	}
	names, err := sls.MatchTop(tree, l.Grains)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", topPath, err)
	}
	for _, name := range names {
		path, err := sls.ResolveName(l.Root, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", topPath, err)
		}
		data, err := l.loadFile(path)
		if err != nil {
			return nil, err
		}
		Merge(out, data)
	}
	return out, nil
}

// loadFile renders and parses one pillar file, merging its includes first.
// A file is loaded at most once, so include cycles terminate.
func (l *Loader) loadFile(path string) (map[string]any, error) {
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
	rendered, err := sls.Render(filepath.Base(path), string(b), sls.TemplateData{Grains: l.Grains})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	tree, err := yamlite.Parse(rendered)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m, ok := tree.(*yamlite.Map)
	if !ok {
		return nil, fmt.Errorf("%s: top level of a pillar file must be a mapping", path)
	}

	out := map[string]any{}
	for _, name := range includeNames(m) {
		incPath, err := sls.ResolveName(l.Root, name)
		if err != nil {
			return nil, fmt.Errorf("%s: include: %w", path, err)
		}
		data, err := l.loadFile(incPath)
		if err != nil {
			return nil, err
		}
		Merge(out, data)
	}
	for _, k := range m.Keys {
		if k == "include" {
			continue
		}
		out[k] = plain(m.Vals[k])
	}
	return out, nil
}

func includeNames(m *yamlite.Map) []string {
	list, ok := m.Vals["include"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Merge deep-merges src into dst: nested mappings are merged key by key,
// every other value (including lists) is replaced wholesale.
func Merge(dst, src map[string]any) {
	for k, v := range src {
		sub, srcIsMap := v.(map[string]any)
		existing, dstIsMap := dst[k].(map[string]any)
		if srcIsMap && dstIsMap {
			Merge(existing, sub)
			continue
		}
		dst[k] = v
	}
}

// plain converts a yamlite tree into plain Go maps and slices so that
// text/template can walk it with {{ .Pillar.a.b }}.
func plain(v any) any {
	switch t := v.(type) {
	case *yamlite.Map:
		m := make(map[string]any, len(t.Keys))
		for _, k := range t.Keys {
			m[k] = plain(t.Vals[k])
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = plain(item)
		}
		return out
	}
	return v
}
