// Package config reads the daemon configuration files, so that running
// `halite master` or `halite agent` under an init system does not mean
// keeping a command line in rc.conf.
//
// A file names the same settings the flags do:
//
//	# /usr/local/etc/halite/master.conf
//	addr: ":4506"
//	root: /usr/local/etc/halite/states
//	returner:
//	  - file:/var/log/halite/results.ndjson
//	  - webhook:https://example.com/halite
//
// Precedence is flag, then environment, then this file, then the platform
// default — the order somebody debugging expects, with the most specific
// thing they typed winning.
package config

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/yamlite"
)

// Config is a daemon's settings, keyed by the flag they correspond to. A
// repeatable flag holds several values; every other setting holds one.
type Config struct {
	Path   string
	values map[string][]string
	order  []string
}

// envFor maps a flag to the environment variable that outranks the file.
// Only the settings that already had an environment variable are listed:
// this package does not invent new ones.
var envFor = map[string]string{
	"root":        "HALITE_ROOT",
	"pillar-root": "HALITE_PILLAR_ROOT",
	"pki":         "HALITE_PKI",
	"master":      "HALITE_MASTER",
}

// DefaultPath is where a daemon looks when `-config` is not given.
// A missing file is not an error: the flags alone are a valid way to run.
func DefaultPath(daemon, dir string) string {
	return dir + "/" + daemon + ".conf"
}

// Load reads a configuration file. A missing file yields an empty Config,
// because running entirely from flags has to keep working.
func Load(path string) (*Config, error) {
	cfg := &Config{Path: path, values: map[string][]string{}}
	if path == "" {
		return cfg, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	tree, err := yamlite.Parse(string(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	root, ok := tree.(*yamlite.Map)
	if !ok {
		return nil, fmt.Errorf("%s: a config file is a mapping of settings to values", path)
	}
	for _, key := range root.Keys {
		switch value := root.Vals[key].(type) {
		case string:
			cfg.set(key, value)
		case []any:
			for _, item := range value {
				if s, ok := item.(string); ok {
					cfg.set(key, s)
				}
			}
		case nil:
			cfg.set(key, "")
		default:
			return nil, fmt.Errorf("%s: %s must be a value or a list of values", path, key)
		}
	}
	return cfg, nil
}

func (c *Config) set(key, value string) {
	if _, seen := c.values[key]; !seen {
		c.order = append(c.order, key)
	}
	c.values[key] = append(c.values[key], value)
}

// Keys returns the settings the file named, in the order it named them.
func (c *Config) Keys() []string { return c.order }

// Apply writes the file's settings into the flags that were not given on
// the command line and are not set in the environment. It is called after
// the flags are parsed, so an explicit flag always wins.
//
// A setting the flag set does not know is an error: a typo in a config
// file that silently did nothing would be worse than a daemon that
// refuses to start.
func (c *Config) Apply(fs *flag.FlagSet) error {
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	var unknown []string
	for _, key := range c.order {
		if fs.Lookup(key) == nil {
			unknown = append(unknown, key)
			continue
		}
		if explicit[key] {
			continue // the command line said otherwise
		}
		if env := envFor[key]; env != "" && os.Getenv(env) != "" {
			continue // so did the environment
		}
		for _, value := range c.values[key] {
			if value == "" {
				continue
			}
			if err := fs.Set(key, value); err != nil {
				return fmt.Errorf("%s: %s: %w", c.Path, key, err)
			}
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("%s: no such setting: %s (its name is the flag's, without the dash)",
			c.Path, strings.Join(unknown, ", "))
	}
	return nil
}
