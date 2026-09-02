package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadNode writes a node configuration and loads it.
func loadNode(t *testing.T, body string) *Config {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "node.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(Node, LoadOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A setting that does nothing has to say so.
//
// Twelve keys parsed, validated and changed nothing, and `IsKnownKey`
// accepted every one — so an operator who set `job_cache` or
// `startup_states` got silence, which from a configuration loader reads
// as agreement. The waiver that excused them named a phase that had
// already shipped.
func TestAnInertSettingIsReported(t *testing.T) {
	cfg := loadNode(t, "job_cache: redis\nquiesce: true\nhub: h.example\n")

	warnings := cfg.InertWarnings()
	if len(warnings) != 2 {
		t.Fatalf("expected two inert settings, got %d: %+v", len(warnings), warnings)
	}
	// Sorted, so the report does not depend on map order.
	if warnings[0].Setting != "job_cache" || warnings[1].Setting != "quiesce" {
		t.Errorf("warnings are not in order: %+v", warnings)
	}
	for _, w := range warnings {
		if w.Effect == "" {
			t.Errorf("%s: no effect given, so the warning says only that "+
				"something is wrong", w.Setting)
		}
		if w.Section == "" {
			t.Errorf("%s: no SPEC section, so a reader cannot find what they "+
				"asked for", w.Setting)
		}
	}
}

// A setting this build honours is not reported, or the warning becomes
// noise and stops being read.
func TestASettingThatWorksIsNotReported(t *testing.T) {
	cfg := loadNode(t, "hub: h.example\nlog_level: debug\n")
	if w := cfg.InertWarnings(); len(w) != 0 {
		t.Errorf("a working configuration reported %d inert settings: %+v", len(w), w)
	}
}

// Every inert key is a declared setting, and every one has an effect
// worth printing.
func TestEveryInertKeyIsDeclaredAndExplained(t *testing.T) {
	for name, effect := range InertKeys {
		if sectionOf(name) == "" {
			t.Errorf("%q is inert and not in the key table, so it has no "+
				"SPEC section to cite", name)
		}
		if strings.TrimSpace(effect) == "" {
			t.Errorf("%q has no effect recorded", name)
		}
		if _, both := UnreadKeys[name]; both {
			t.Errorf("%q is in both InertKeys and UnreadKeys; one place has "+
				"to be the answer", name)
		}
	}
}
