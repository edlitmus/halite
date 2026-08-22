package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The example configurations in contrib/examples are documentation that
// executes, which is the only kind that stays true. Each is loaded as the
// program it is written for, and a warning is a failure: halite reports
// an unrecognised key rather than ignoring it, so an example carrying one
// teaches a setting that does not exist.
func TestExampleConfigurationsLoadCleanly(t *testing.T) {
	dir := filepath.Join("..", "..", "contrib", "examples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	found := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		found++
		t.Run(e.Name(), func(t *testing.T) {
			role, ok := roleForExample(e.Name())
			if !ok {
				t.Fatalf("%s does not say which program it is for; "+
					"name it node-*.yaml, hub*.yaml, or api*.yaml", e.Name())
			}
			cfg, err := Load(role, LoadOptions{Path: filepath.Join(dir, e.Name())})
			if err != nil {
				t.Fatalf("%v", err)
			}
			for _, w := range cfg.Warnings {
				t.Errorf("%v", w)
			}
		})
	}
	if found == 0 {
		t.Error("there are no example configurations; this check has stopped checking anything")
	}
}

func roleForExample(name string) (Role, bool) {
	switch {
	case strings.HasPrefix(name, "node"):
		return Node, true
	case strings.HasPrefix(name, "hub"):
		return Hub, true
	case strings.HasPrefix(name, "api"):
		return API, true
	}
	return 0, false
}
