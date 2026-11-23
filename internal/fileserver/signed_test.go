package fileserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/treesign"
)

// This node's own roots under `state_signature_local`: the same rules as
// the hub's tree, read from the local directory.

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func signLocal(t *testing.T, roots *Roots, dir string, key *ecdsa.PrivateKey) {
	t.Helper()
	entries, err := roots.TreeEntries("base", "sha256")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := treesign.Encode(&treesign.Manifest{Env: "base", Issued: treesign.FormatTime(time.Now()),
		HashType: "sha256", Files: entries})
	if err != nil {
		t.Fatal(err)
	}
	line, err := treesign.SignatureLine(key, raw)
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, map[string]string{treesign.ManifestPath: string(raw), treesign.SignaturePath: line + "\n"})
}

func guardedLocal(t *testing.T, files map[string]string, sign bool) (*Fetcher, string) {
	t.Helper()
	dir := t.TempDir()
	writeTree(t, dir, files)
	roots := NewRoots(map[string][]string{"base": {dir}})
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if sign {
		signLocal(t, roots, dir, key)
	}
	f := NewFetcher(roots)
	f.Guard = &TreeGuard{Mode: SignatureRequired, Keys: []jobsign.SignerKey{{Name: "ci", Key: &key.PublicKey}},
		Records: treesign.Store{Dir: t.TempDir()}, Objects: filepath.Join(t.TempDir(), "objects")}
	return f.ForRun(), dir
}

var localFiles = map[string]string{
	"top.sls":       "base:\n  '*':\n    - app\n",
	"app/init.sls":  "x:\n  test.nop: []\n",
	"app/conf.j2":   "port {{ 80 }}\n",
	"app/files/a":   "a\n",
	"app/files/b/c": "c\n",
}

func TestASignedLocalTreeIsReadThroughItsManifest(t *testing.T) {
	f, dir := guardedLocal(t, localFiles, true)
	if body, _, err := f.Source("base", "app"); err != nil || !strings.Contains(string(body), "test.nop") {
		t.Fatalf("Source: %q, %v", body, err)
	}
	if body, _, err := f.Templates("base").Load("app/conf.j2"); err != nil || !strings.Contains(body, "port") {
		t.Fatalf("Templates: %q, %v", body, err)
	}
	p, err := f.Fetch("base", "salt://app/files/a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(p, dir) {
		t.Errorf("Fetch returned the file in place, %s, rather than the verified copy", p)
	}
	writeTree(t, dir, map[string]string{"app/files/new": "unsigned\n"})
	under, err := f.ListUnder("base", "salt://app/files")
	if err != nil || strings.Join(under, ",") != "a,b/c" {
		t.Fatalf("ListUnder: %v, %v", under, err)
	}
}

func TestAChangedLocalFileIsRefused(t *testing.T) {
	f, dir := guardedLocal(t, localFiles, true)
	if _, err := f.Fetch("base", "salt://app/files/a"); err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, map[string]string{"app/files/a": "tampered\n", "app/init.sls": "rm:\n  cmd.run: []\n"})
	if _, err := f.Fetch("base", "salt://app/files/a"); treesign.ReasonOf(err) != treesign.ReasonDigest {
		t.Fatalf("a changed file whose verified copy was already here: %v", err)
	}
	if _, _, err := f.Source("base", "app"); treesign.ReasonOf(err) != treesign.ReasonDigest {
		t.Fatalf("a changed state: %v", err)
	}
	writeTree(t, dir, map[string]string{"evil.sls": "x\n"})
	if _, _, err := f.Source("base", "evil"); err == nil {
		t.Fatal("a state the signer never published was found")
	}
}

func TestAnUnsignedLocalTreeAppliesNothingUnderTheRequirement(t *testing.T) {
	f, _ := guardedLocal(t, localFiles, false)
	if _, _, err := f.Source("base", "app"); treesign.ReasonOf(err) != treesign.ReasonMissing {
		t.Fatalf("got %v", err)
	}
	// And a Fetcher with no guard is what it always was.
	plain := NewFetcher(f.Roots)
	if _, _, err := plain.Source("base", "app"); err != nil {
		t.Fatalf("no guard: %v", err)
	}
}

func TestParseSignatureModeRefusesWhatItCannotRead(t *testing.T) {
	for raw, want := range map[any]SignatureMode{true: SignatureRequired, false: SignatureOff,
		"warn": SignatureWarn, "true": SignatureRequired, "false": SignatureOff, " WARN ": SignatureWarn} {
		got, err := ParseSignatureMode(raw, true)
		if err != nil || got != want {
			t.Errorf("%v: %v, %v", raw, got, err)
		}
	}
	for _, raw := range []any{"yes", "warning", 1, []any{"warn"}} {
		if _, err := ParseSignatureMode(raw, true); err == nil {
			t.Errorf("%v was accepted", raw)
		}
	}
}

func TestReservedProblemNamesWhatDoesNotBelong(t *testing.T) {
	dir := t.TempDir()
	roots := NewRoots(map[string][]string{"base": {dir}})
	if err := roots.ReservedProblem("base"); err != nil {
		t.Fatalf("no _halite/: %v", err)
	}
	writeTree(t, dir, map[string]string{"_halite/manifest.json": "{}", "_halite/manifest.sig": "x"})
	if err := roots.ReservedProblem("base"); err != nil {
		t.Fatalf("the two files: %v", err)
	}
	writeTree(t, dir, map[string]string{"_halite/manifest.json.orig": "{}"})
	if err := roots.ReservedProblem("base"); err == nil || !strings.Contains(err.Error(), "manifest.json.orig") {
		t.Fatalf("an extra file: %v", err)
	}
	// A shadowed later root is still checked.
	other := t.TempDir()
	writeTree(t, other, map[string]string{"_halite/x": "x"})
	roots = NewRoots(map[string][]string{"base": {t.TempDir(), other}})
	if err := roots.ReservedProblem("base"); err == nil || !strings.Contains(err.Error(), "holds x") {
		t.Fatalf("a later root: %v", err)
	}
	// And _halite as a file.
	flat := t.TempDir()
	writeTree(t, flat, map[string]string{"_halite": "x"})
	if err := NewRoots(map[string][]string{"base": {flat}}).ReservedProblem("base"); err == nil {
		t.Fatal("a _halite file was accepted")
	}
}

func TestAHideRuleOverTheManifestIsReported(t *testing.T) {
	roots := NewRoots(nil)
	roots.IgnoreGlobs = []string{"_*"}
	if !roots.HidesManifest() {
		t.Error("`_*` hides _halite/ and was not reported")
	}
	roots.IgnoreGlobs = []string{"*.swp"}
	if roots.HidesManifest() {
		t.Error("`*.swp` was reported")
	}
}

// Under warn the local listing is the roots' own, as without the feature.
func TestWarnModeListsTheLocalRootsAsTheyAre(t *testing.T) {
	f, dir := guardedLocal(t, localFiles, true)
	counted := 0
	f.Guard.Mode = SignatureWarn
	f.Guard.Refused = func(treesign.Reason) { counted++ }
	writeTree(t, dir, map[string]string{"app/files/new": "unsigned\n"})
	under, err := f.ListUnder("base", "salt://app/files")
	if err != nil || strings.Join(under, ",") != "a,b/c,new" {
		t.Fatalf("ListUnder: %v, %v", under, err)
	}
	if counted != 1 {
		t.Errorf("counted %d", counted)
	}
}
