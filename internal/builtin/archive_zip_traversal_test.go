package builtin

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeZipEntries writes a zip whose entries are named exactly as given,
// which archive/zip allows whatever the name holds.
func writeZipEntries(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// A zip entry that names a place outside the destination is refused by
// name, and nothing is written there.
//
// The tar path has had this test since the archive module began
// (TestArchiveExtractRefusesTraversal); zip had only the planted-symlink
// case, so code scanning's go/zipslip alert on extractZip (alert 45,
// 2026-11-20) rested on a check nothing had shown for zip. The target here
// is a real neighbour of the destination, so "nothing escaped" is a file
// that would exist if it had.
func TestAZipEntryOutsideTheDestinationIsRefused(t *testing.T) {
	cases := map[string]string{
		"parent":   "../outside.txt",
		"deeper":   "inner/../../outside.txt",
		"absolute": "/outside.txt",
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "evil.zip")
			writeZipEntries(t, src, map[string]string{"good.txt": "fine\n", entry: "escaped\n"})
			dest := filepath.Join(dir, "out")

			_, err := extractArchive(src, dest, false, false)
			if err == nil {
				t.Fatalf("an entry named %q was accepted", entry)
			}
			if !strings.Contains(err.Error(), "escapes the destination") &&
				!strings.Contains(err.Error(), "absolute") {
				t.Errorf("the refusal does not say why: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "outside.txt")); err == nil {
				t.Fatalf("an entry named %q was written outside the destination", entry)
			}
		})
	}
}
