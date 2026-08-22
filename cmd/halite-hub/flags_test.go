package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The halite-hub half of the check in cmd/halite-node. A flag in the
// usage that nothing parses, or one parsed and never documented, is the
// same defect as a setting nothing reads — on the surface an operator
// meets first.
//
// It found `--root`, which was parsed and undocumented, and something
// worse: `--config` meant this program's own configuration in `lint` and
// "a Salt file to translate" in `migrate`. One flag, two meanings, one
// program. The second is `--salt-config` now.
func TestEveryFlagIsDocumentedAndParsed(t *testing.T) {
	var source strings.Builder
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Clean(e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		source.Write(data)
	}

	documented := map[string]bool{}
	for _, f := range regexp.MustCompile(`(?m)^\s+(--[a-z-]+)`).FindAllStringSubmatch(usage, -1) {
		documented[f[1]] = true
	}
	parsed := map[string]bool{}
	for _, f := range regexp.MustCompile(`args\.(?:Flag|Bool)\("([a-z-]+)"`).
		FindAllStringSubmatch(source.String(), -1) {
		parsed["--"+f[1]] = true
	}
	if len(documented) == 0 || len(parsed) == 0 {
		t.Fatal("no flags were found; this test has stopped checking anything")
	}

	for f := range documented {
		if !parsed[f] {
			t.Errorf("%s is in the usage and nothing parses it", f)
		}
	}
	for f := range parsed {
		if !documented[f] {
			t.Errorf("%s is parsed and is in no usage text; an operator cannot find it", f)
		}
	}
}
