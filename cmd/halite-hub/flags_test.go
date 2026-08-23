package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/pki"
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
	// Both usage texts: `keys` has its own, and its flags are
	// documented there rather than repeated in the main one.
	for _, f := range regexp.MustCompile(`(?m)^\s+(--[a-z-]+)`).
		FindAllStringSubmatch(usage+"\n"+keysUsage, -1) {
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

// A file root that holds the hub's own state serves the key store and
// the job cache — every return in the estate — to every enrolled node.
// `file_roots: /srv/halite` beside `state_dir: /srv/halite/state` is an
// easy thing to write, and it happened in this project's own lab.
func TestAFileRootCannotHoldTheHubsOwnState(t *testing.T) {
	base := t.TempDir()
	h := &hubContext{
		files: pki.Files{Dir: filepath.Join(base, "pki")},
	}
	store, err := keystore.Open(filepath.Join(base, "state", "keys"))
	if err != nil {
		t.Fatal(err)
	}
	h.store = store
	h.cfg, err = config.Load(config.Hub, config.LoadOptions{Root: base, AllowMissing: true})
	if err != nil {
		t.Fatal(err)
	}

	overlapping := map[string][]string{"base": {filepath.Join(base, "state")}}
	if err := checkRootsAreNotTheHubsOwn(h, overlapping); err == nil {
		t.Error("a file root holding the key store was accepted")
	}
	// The other direction: the state directory holding the tree is the
	// same mistake wearing a hat.
	inverted := map[string][]string{"base": {base}}
	if err := checkRootsAreNotTheHubsOwn(h, inverted); err == nil {
		t.Error("a file root containing the key store was accepted")
	}
	separate := map[string][]string{"base": {filepath.Join(base, "tree")}}
	if err := checkRootsAreNotTheHubsOwn(h, separate); err != nil {
		t.Errorf("a separate tree was refused: %v", err)
	}
}
