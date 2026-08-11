package grains

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/yamlite"
)

// CustomPath returns the file static custom grains are read from:
// $HALITE_GRAINS, or the platform default beside the state tree.
//
// Custom grains are what makes grain targeting usable — halite collects a
// fixed set of facts about the host, and everything a site targets on
// (role, datacentre, tier) has to come from somewhere.
func CustomPath() string {
	if env := os.Getenv("HALITE_GRAINS"); env != "" {
		return env
	}
	switch runtime.GOOS {
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return "/usr/local/etc/halite/grains"
	case "windows":
		return `C:\ProgramData\halite\grains`
	default:
		return "/etc/halite/grains"
	}
}

// LoadCustom reads a static grains file: a plain YAML mapping, the same
// subset SLS files use. A missing file yields no grains and no error —
// most hosts have none.
func LoadCustom(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	tree, err := yamlite.Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m, ok := tree.(*yamlite.Map)
	if !ok {
		return nil, fmt.Errorf("%s: a grains file must be a mapping of names to values", path)
	}
	out := map[string]any{}
	for _, k := range m.Keys {
		out[k] = yamlite.Plain(m.Vals[k])
	}
	return out, nil
}

// SaveCustom writes a static grains file, replacing it atomically. An empty
// map removes the file rather than leaving an empty one behind.
func SaveCustom(path string, data map[string]any) error {
	if len(data) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	var b strings.Builder
	b.WriteString("# halite static grains\n")
	writeMapping(&b, data, "")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// writeMapping emits a mapping in the YAML subset yamlite reads back.
func writeMapping(b *strings.Builder, data map[string]any, indent string) {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch v := data[k].(type) {
		case map[string]any:
			fmt.Fprintf(b, "%s%s:\n", indent, k)
			writeMapping(b, v, indent+"  ")
		case []any:
			fmt.Fprintf(b, "%s%s:\n", indent, k)
			for _, item := range v {
				fmt.Fprintf(b, "%s  - %s\n", indent, quoteScalar(item))
			}
		default:
			fmt.Fprintf(b, "%s%s: %s\n", indent, k, quoteScalar(v))
		}
	}
}

// quoteScalar renders a value so that reading the file back yields the same
// string: anything that would parse as structure gets quoted.
func quoteScalar(v any) string {
	s := fmt.Sprintf("%v", v)
	if s == "" {
		return `""`
	}
	needsQuote := strings.ContainsAny(s, `:#'"`) ||
		strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{") ||
		strings.HasPrefix(s, "&") || strings.HasPrefix(s, "*") ||
		strings.HasPrefix(s, "!") || strings.HasPrefix(s, "-") ||
		s != strings.TrimSpace(s)
	if !needsQuote {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
