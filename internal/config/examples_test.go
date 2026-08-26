package config

import (
	"os"
	"path/filepath"
	"regexp"
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

// Most of what an example teaches is commented out — the settings an
// operator uncomments when they need them. Those never reach Load, so
// the check above never sees them, and a typo in one ships as
// documentation of a setting that does not exist.
//
// Every commented key is therefore held to the same standard as a live
// one: it has to be a real key, and it has to belong to the program the
// file is for. `pillar_roots` was marked hub-only while every masterless
// node read it, which the generated reference then taught.
func TestCommentedExampleKeysAreRealKeysForTheirProgram(t *testing.T) {
	dir := filepath.Join("..", "..", "contrib", "examples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	// A top-level key, live or commented: nothing before it but an
	// optional comment marker, then one lowercase identifier and a
	// colon. Indented lines are values, and prose does not look like
	// this.
	key := regexp.MustCompile(`^(?:#\s?)?([a-z][a-z0-9_]*):(?:\s|$)`)

	checked := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		role, ok := roleForExample(e.Name())
		if !ok {
			continue
		}
		allowed := map[string]bool{}
		for _, k := range KeysFor(role) {
			allowed[k.Name] = true
		}
		known := map[string]bool{}
		for _, r := range []Role{Node, Hub, API} {
			for _, k := range KeysFor(r) {
				known[k.Name] = true
			}
		}

		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			m := key.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			checked++
			switch {
			case !known[m[1]]:
				t.Errorf("%s:%d teaches %q, which is not a configuration key",
					e.Name(), i+1, m[1])
			case !allowed[m[1]]:
				// A key from another program, which an operator would
				// uncomment and find silently ignored. An example that
				// deliberately shows one — the hub setting a relay's
				// upstream needs — belongs in that program's file.
				t.Errorf("%s:%d teaches %q, which %s does not read",
					e.Name(), i+1, m[1], e.Name())
			}
		}
	}
	if checked == 0 {
		t.Error("no example keys were read; this check has stopped checking anything")
	}
	t.Logf("checked %d example keys", checked)
}
