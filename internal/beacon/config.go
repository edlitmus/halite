package beacon

import (
	"fmt"
	"os"
	"strconv"

	"github.com/edlitmus/halite/internal/yamlite"
)

// config is one parsed beacon entry: a flat mapping of scalars.
type config map[string]string

func (c config) str(key string) string { return c[key] }

func (c config) intOr(key string, fallback int) (int, error) {
	raw, ok := c[key]
	if !ok || raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", key, raw)
	}
	return value, nil
}

// Load reads a beacon config file:
//
//	disk:
//	  - mount: /var
//	    threshold: "90"
//	    interval: 60s
//	service:
//	  - name: nginx
//	    interval: 30s
//	file:
//	  - path: /usr/local/etc/nginx/nginx.conf
//
// Each beacon kind maps to a list, so the same kind can watch several
// things. A missing file is not an error — no beacons simply means none.
func Load(path string) ([]Beacon, error) {
	if path == "" {
		return nil, nil
	}
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
	root, ok := tree.(*yamlite.Map)
	if !ok {
		return nil, fmt.Errorf("%s: top level must be a mapping of beacon kinds", path)
	}

	var beacons []Beacon
	for _, kind := range root.Keys {
		entries, err := parseEntries(root.Vals[kind])
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", path, kind, err)
		}
		for _, entry := range entries {
			beacon, err := build(kind, entry)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", path, kind, err)
			}
			beacons = append(beacons, beacon)
		}
	}
	return beacons, nil
}

func parseEntries(v any) ([]config, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a list of watches")
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("has no watches")
	}
	var out []config
	for _, item := range list {
		body, ok := item.(*yamlite.Map)
		if !ok {
			return nil, fmt.Errorf("each watch must be a mapping")
		}
		entry := config{}
		for _, key := range body.Keys {
			text, ok := body.Vals[key].(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a scalar", key)
			}
			entry[key] = text
		}
		out = append(out, entry)
	}
	return out, nil
}
