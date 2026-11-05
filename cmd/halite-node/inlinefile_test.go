package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A name that starts with two dots is a name. `..data` and
// `..2024_11_05_00_00_00.1` are what a Kubernetes ConfigMap volume puts
// in every directory it mounts, and a tree copied from one carries
// them; the prefix test refused them, and with them the whole run
// (issue 614). A `..` element is still a way out of the tree, and is
// still refused, as is an element of only dots, which Windows reads as
// the directory that holds it.
func TestAnInlineFileNamedWithTwoLeadingDotsIsWritten(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{"..data", "conf/..2024_11_05_00_00_00.1/app.conf", "a/..b/c.sls"} {
		if err := writeInlineFile(dir, rel, "x"); err != nil {
			t.Errorf("%q was refused: %v", rel, err)
			continue
		}
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || string(got) != "x" {
			t.Errorf("%q was not written inside the tree: %v", rel, err)
		}
	}

	for _, rel := range []string{"..", "../escape.sls", "a/../../escape.sls", "/etc/escape.sls", "", "a/..", "a/../..",
		// Only dots: Windows strips them and names the directory.
		"...", "a/..../b.sls"} {
		err := writeInlineFile(dir, rel, "x")
		if err == nil {
			t.Errorf("%q was written; it is not a path inside the tree", rel)
			continue
		}
		if !strings.Contains(err.Error(), "not a path inside the tree") {
			t.Errorf("%q was refused for the wrong reason: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.sls")); err == nil {
		t.Error("a file was written beside the tree")
	}
}
