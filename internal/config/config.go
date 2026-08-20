package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// Role names which binary a configuration belongs to.
type Role int

const (
	// Node is `halite-node`.
	Node Role = iota
	// Hub is `halite-hub`.
	Hub
	// API is `halite-api`.
	API
)

func (r Role) String() string {
	switch r {
	case Hub:
		return "hub"
	case API:
		return "api"
	default:
		return "node"
	}
}

// FileName is the default configuration file for a role.
func (r Role) FileName() string { return r.String() + ".yaml" }

// DropInDir is the default drop-in fragment directory for a role.
func (r Role) DropInDir() string { return r.String() + ".d" }

// Config is a loaded, merged, shimmed configuration.
type Config struct {
	Role Role
	// Files lists every file that contributed, in the order they merged.
	Files []string
	// Values is the merged mapping after the compatibility shim ran.
	Values *value.Map
	// Shim records what the compatibility shim translated and refused.
	Shim ShimResult
	// Warnings are the lines the process emits once at start.
	Warnings []string
}

// LoadOptions control a load.
type LoadOptions struct {
	// Path is the primary configuration file. Empty means the default for
	// the role under Root.
	Path string
	// DropInDir overrides the drop-in directory. Empty means the default.
	DropInDir string
	// Root is the configuration root, default /etc/halite.
	Root string
	// Overrides are applied last, above every file, which is how a command
	// line flag beats a file.
	Overrides *value.Map
	// AllowMissing makes an absent primary file produce an empty
	// configuration rather than an error.
	AllowMissing bool
}

// DefaultRoot is where packaged configuration lives. SPEC section 27.3.
const DefaultRoot = "/etc/halite"

// Load reads a configuration: the primary file, then every drop-in
// fragment in lexical order, then the overrides. Later sources deep merge
// over earlier ones, so a drop-in can add a key without restating the
// file it extends.
func Load(role Role, opts LoadOptions) (*Config, error) {
	root := opts.Root
	if root == "" {
		root = DefaultRoot
	}
	path := opts.Path
	if path == "" {
		path = filepath.Join(root, role.FileName())
	}
	dropIn := opts.DropInDir
	if dropIn == "" {
		dropIn = filepath.Join(root, role.DropInDir())
	}

	cfg := &Config{Role: role, Values: value.NewMap(16)}

	merged, err := loadFile(path, opts.AllowMissing)
	if err != nil {
		return nil, err
	}
	if merged != nil {
		cfg.Files = append(cfg.Files, path)
	} else {
		merged = value.NewMap(0)
	}

	fragments, err := dropInFiles(dropIn)
	if err != nil {
		return nil, err
	}
	for _, f := range fragments {
		v, err := loadFile(f, false)
		if err != nil {
			return nil, err
		}
		if v == nil {
			continue
		}
		cfg.Files = append(cfg.Files, f)
		merged = value.Merge(merged, v, value.MergeOpts{Strategy: value.Recurse}).(*value.Map)
	}

	if opts.Overrides != nil && opts.Overrides.Len() > 0 {
		merged = value.Merge(merged, opts.Overrides, value.MergeOpts{Strategy: value.Recurse}).(*value.Map)
	}

	shimmed, res := ApplyShim(merged, func(k string) bool { return IsKnownKey(role, k) })
	cfg.Values = shimmed
	cfg.Shim = res
	cfg.Warnings = res.Warnings()
	if err := res.Err(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func loadFile(path string, allowMissing bool) (*value.Map, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && allowMissing {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	v, _, err := yaml.Parse(b, yaml.DefaultOptions(path))
	if err != nil {
		return nil, err
	}
	if v == nil {
		return value.NewMap(0), nil
	}
	m, ok := v.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("%s: a configuration file must hold a mapping, found %s", path, value.TypeName(v))
	}
	return m, nil
}

func dropInFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".conf") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out, nil
}

// LoadSaltConfig reads a Salt node or hub configuration file, in Salt's
// own vocabulary, and reports every key it translated and every key it
// ignored. This is what makes the first step of a migration not require
// rewriting the configuration management for the configuration
// management. SPEC section 27.5.
func LoadSaltConfig(role Role, path string) (*Config, error) {
	return Load(role, LoadOptions{
		Path:      path,
		DropInDir: path + ".d",
	})
}

// ---- typed access ----

// Get resolves a colon-delimited key path.
func (c *Config) Get(path string) (any, bool) {
	return value.Traverse(c.Values, path, ":")
}

// String reads a string setting.
func (c *Config) String(path, def string) string {
	v, ok := c.Get(path)
	if !ok || v == nil {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

// Bool reads a boolean setting.
func (c *Config) Bool(path string, def bool) bool {
	v, ok := c.Get(path)
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		if b, err := strconv.ParseBool(t); err == nil {
			return b
		}
	}
	return def
}

// Int reads an integer setting.
func (c *Config) Int(path string, def int64) int64 {
	v, ok := c.Get(path)
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case string:
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// Duration reads a duration, accepting either a Go duration string or a
// bare number of seconds, which is how Salt writes intervals.
func (c *Config) Duration(path string, def time.Duration) time.Duration {
	v, ok := c.Get(path)
	if !ok || v == nil {
		return def
	}
	switch t := v.(type) {
	case string:
		if d, err := time.ParseDuration(t); err == nil {
			return d
		}
	case int64:
		return time.Duration(t) * time.Second
	case float64:
		return time.Duration(t * float64(time.Second))
	}
	return def
}

// StringSlice reads a list of strings, accepting a bare string as a
// one-item list.
func (c *Config) StringSlice(path string) []string {
	v, ok := c.Get(path)
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
				continue
			}
			out = append(out, value.KeyString(item))
		}
		return out
	}
	return nil
}

// Map reads a nested mapping.
func (c *Config) Map(path string) *value.Map {
	v, ok := c.Get(path)
	if !ok {
		return nil
	}
	m, _ := v.(*value.Map)
	return m
}

// Roots reads a `file_roots` or `pillar_roots` block: environment name to
// an ordered list of directories.
func (c *Config) Roots(path string) map[string][]string {
	m := c.Map(path)
	if m == nil {
		return nil
	}
	out := map[string][]string{}
	for _, e := range m.Entries() {
		env := value.KeyString(e.Key)
		switch t := e.Val.(type) {
		case string:
			out[env] = []string{t}
		case []any:
			var dirs []string
			for _, item := range t {
				if s, ok := item.(string); ok {
					dirs = append(dirs, s)
				}
			}
			out[env] = dirs
		}
	}
	return out
}

// Redacted returns a copy of the values with secret-bearing keys replaced,
// which is what the `opts` template variable is bound to. SPEC section
// 10.2.7 requires the secrets be gone before a template can read them.
func (c *Config) Redacted() *value.Map {
	return redact(value.Deep(c.Values).(*value.Map))
}

// secretKeyParts name a configuration key as secret-bearing when any of
// them appears in it.
var secretKeyParts = []string{
	"password", "passwd", "secret", "token", "key_data", "private_key",
	"credential", "api_key", "shared_secret", "passphrase",
}

func redact(m *value.Map) *value.Map {
	for _, e := range m.Entries() {
		key := strings.ToLower(value.KeyString(e.Key))
		if isSecretKey(key) {
			m.Set(e.Key, "<redacted>")
			continue
		}
		if sub, ok := e.Val.(*value.Map); ok {
			m.Set(e.Key, redact(sub))
		}
	}
	return m
}

func isSecretKey(key string) bool {
	for _, part := range secretKeyParts {
		if strings.Contains(key, part) {
			return true
		}
	}
	// `priv` alone is how Salt spells a private key path in a roster.
	return key == "priv" || key == "priv_passwd"
}
