package builtin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The read-only half of the Chocolatey provider, against the real
// binary. Nothing here installs, removes, pins or upgrades anything: a
// test that changed the machine it runs on is not a test anyone will run
// twice.
//
// It skips where choco is absent, which is most machines. The skip is
// the honest outcome there -- the alternative is a provider whose output
// parsing has never met the program it parses.
func TestChocolateyReadsARealInstallation(t *testing.T) {
	if os.Getenv("HALITE_CHOCO_LIVE") != "1" {
		t.Skip("set HALITE_CHOCO_LIVE=1 to read this machine's Chocolatey")
	}
	c := &exec.Context{Ctx: context.Background(), Runner: &exec.OSRunner{}}
	p := chocoProvider{}
	if !p.Available(c) {
		t.Skip("no choco on this machine")
	}

	if got := chocoMajor(c); got < 1 {
		t.Errorf("chocoMajor = %d; the version could not be read", got)
	}

	pkgs, err := p.ListPkgs(c)
	if err != nil {
		t.Fatalf("ListPkgs: %v", err)
	}
	if pkgs.Len() == 0 {
		t.Error("no installed packages; chocolatey itself is always one")
	}
	// Every entry is a name and a version, not a stray summary line.
	for _, e := range pkgs.Entries() {
		if e.Key == "" || value.KeyString(e.Val) == "" {
			t.Errorf("entry %q = %v", e.Key, e.Val)
		}
	}
	if _, ok := pkgs.Get("chocolatey"); !ok {
		t.Errorf("chocolatey is not in its own package list: %v", pkgs.Keys())
	}

	up, err := p.ListUpgrades(c, false)
	if err != nil {
		t.Fatalf("ListUpgrades: %v", err)
	}
	for _, e := range up.Entries() {
		if value.KeyString(e.Val) == "" {
			t.Errorf("%s has an empty available version", e.Key)
		}
	}

	repos, err := p.ListRepos(c)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if repos.Len() == 0 {
		t.Error("no sources; a working choco has at least one")
	}
	for _, e := range repos.Entries() {
		m, ok := e.Val.(*value.Map)
		if !ok {
			t.Fatalf("source %s is %T", e.Key, e.Val)
		}
		if url, _ := m.Get("url"); value.KeyString(url) == "" {
			t.Errorf("source %s has no url", e.Key)
		}
		if _, ok := m.Get("enabled"); !ok {
			t.Errorf("source %s does not say whether it is enabled", e.Key)
		}
	}

	if _, err := p.ListHolds(c); err != nil {
		t.Errorf("ListHolds: %v", err)
	}

	// A name the community repository certainly has, and one it does not.
	if v, err := p.LatestVersion(c, "7zip"); err != nil || v == "" {
		t.Errorf("LatestVersion(7zip) = %q, %v", v, err)
	}
	if v, err := p.LatestVersion(c, "halite-no-such-package-9f3a"); err != nil || v != "" {
		t.Errorf("LatestVersion of a missing package = %q, %v; want an empty string", v, err)
	}
}

// The repository provider's read half, against the real source list.
//
// Read-only for the same reason as everything above it: adding a source
// changes where the machine installs software from, and needs
// administrator rights besides. What is checked is that the parser meets
// the format the program actually prints — one pipe-separated record per
// line under --limit-output, which is the machine-readable mode and the
// only one whose shape is stable.
func TestChocolateyReadsTheRealSourceList(t *testing.T) {
	if os.Getenv("HALITE_CHOCO_LIVE") != "1" {
		t.Skip("set HALITE_CHOCO_LIVE=1 to read this machine's Chocolatey sources")
	}
	c := &exec.Context{Ctx: context.Background(), Runner: &exec.OSRunner{}}
	p := chocoRepoProvider{}
	if !p.Available(c) {
		t.Skip("no choco on this machine")
	}

	all, err := p.List(c)
	if err != nil {
		t.Fatalf("listing the sources: %v", err)
	}
	if all.Len() == 0 {
		t.Fatal("no sources at all; a working Chocolatey has the community one")
	}
	for _, e := range all.Entries() {
		m, ok := e.Val.(*value.Map)
		if !ok {
			t.Fatalf("%v is %#v, not a mapping", e.Key, e.Val)
		}
		url := repoStr(m, "baseurl", "")
		if url == "" {
			t.Errorf("the source %v has no url; the wrong field was read", e.Key)
		}
		if _, has := m.Get("enabled"); !has {
			t.Errorf("the source %v has no enabled state", e.Key)
		}
	}

	// And Get finds one by name, case-insensitively, because Chocolatey
	// does not care about the case of a source name and neither should a
	// state that names one.
	first := fmt.Sprint(all.Entries()[0].Key)
	got, err := p.Get(c, strings.ToUpper(first))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Errorf("%q was not found when named in a different case", first)
	}

	// A source that is not there is nothing rather than an error, which
	// is what `pkgrepo.absent` converging depends on.
	got, err = p.Get(c, "halite-no-such-source")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("a source that does not exist was found: %v", got)
	}
}
