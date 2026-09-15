package builtin

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// keysArchive is the shape `shared/salt/pgpkeys.sls` unpacks: a directory
// with a key file inside it.
var keysArchive = map[string]string{"gpgkeys/secring.gpg": "secret"}

// Ownership is enforced on every run, not only on the run that extracts.
// Re-extracting an archive to correct a group would rewrite every file in
// it each time, which is the same non-convergence the x509 states avoid.
func TestArchiveExtractedEnforcesOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing a file's owner needs root")
	}
	if _, err := user.Lookup("nobody"); err != nil {
		t.Skip("this machine has no nobody user")
	}

	dir := t.TempDir()
	archive := filepath.Join(dir, "keys.tar.gz")
	dest := filepath.Join(dir, "out")
	makeTarGz(t, archive, keysArchive)

	r := New()
	args := func(owner string) *value.Map {
		return value.MapOf("name", dest, "source", archive, "user", owner)
	}

	res := run(t, r, "archive.extracted", args("root"), false)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("the first run: ok=%v comment=%q", res.Succeeded(), res.Comment)
	}

	// Nothing to do a second time.
	res = run(t, r, "archive.extracted", args("root"), false)
	if res.HasChanges() {
		t.Errorf("the second run reported a change: %q", res.Comment)
	}

	// A different owner is a change, made without re-extracting.
	extracted := filepath.Join(dest, "gpgkeys", "secring.gpg")
	before, err := os.Stat(extracted)
	if err != nil {
		t.Fatal(err)
	}
	res = run(t, r, "archive.extracted", args("nobody"), false)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("the owner change: ok=%v comment=%q", res.Succeeded(), res.Comment)
	}
	after, err := os.Stat(extracted)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("the archive was re-extracted to change an owner")
	}

	// And it converges.
	res = run(t, r, "archive.extracted", args("nobody"), false)
	if res.HasChanges() {
		t.Errorf("the run after the chown still reports a change: %q", res.Comment)
	}
}

// keep_source is about the cache, not the archive a tree points at. Only
// a fetched source is copied anywhere, so a local path must survive
// keep_source: False -- which is what Salt does, and what the estate's
// tree relies on: it writes keep_source: False against a local
// /etc/salt/gpgkeys.tar.gz that later states still read.
func TestKeepSourceFalseLeavesALocalArchiveAlone(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "keys.tar.gz")
	dest := filepath.Join(dir, "out")
	makeTarGz(t, archive, keysArchive)

	r := New()
	res := run(t, r, "archive.extracted",
		value.MapOf("name", dest, "source", archive, "keep_source", false), false)
	if !res.Succeeded() {
		t.Fatalf("%q", res.Comment)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Errorf("keep_source: False deleted the tree's own archive: %v", err)
	}
}
