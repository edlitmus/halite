package fileserver

import (
	"path/filepath"
	"testing"
)

// The reproduced case: a hide rule written for Salt, where `*` crosses
// `/`, hid only the first level of what it names. `secrets/a.txt` was
// hidden; `secrets/deep/b.txt` and `secrets/deep/er/c.txt` were served
// and listed. Both are hidden now, from serving and from listing, and a
// file neither rule names is still served.
func TestIgnoreGlobsHideNestedFilesAsSaltDoes(t *testing.T) {
	tmp := t.TempDir()
	hidden := []string{
		"secrets/a.txt",
		"secrets/deep/b.txt",
		"secrets/deep/er/c.txt",
		"public/er/d.txt",
		"public/x/er/e.txt",
	}
	shown := []string{"public/x.txt", "public/er.txt", "secretsfile.txt", "top.sls"}
	for _, f := range append(append([]string(nil), hidden...), shown...) {
		write(t, filepath.Join(tmp, filepath.FromSlash(f)), f+"\n")
	}
	r := NewRoots(map[string][]string{"base": {tmp}})
	r.IgnoreGlobs = []string{"secrets/*", "*/er/*"}

	for _, f := range hidden {
		if _, err := r.Resolve("base", f); err == nil {
			t.Errorf("%s was served under file_ignore_glob [secrets/*, */er/*]", f)
		}
		if _, _, err := r.Read("base", f); err == nil {
			t.Errorf("%s was read under file_ignore_glob [secrets/*, */er/*]", f)
		}
	}
	for _, f := range shown {
		if _, err := r.Resolve("base", f); err != nil {
			t.Errorf("%s matches no rule and should be served: %v", f, err)
		}
	}

	listed, err := r.List("base")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range listed {
		got[f] = true
	}
	for _, f := range hidden {
		if got[f] {
			t.Errorf("List returned %s, which file_ignore_glob hides", f)
		}
	}
	for _, f := range shown {
		if !got[f] {
			t.Errorf("List left out %s, which no rule names", f)
		}
	}
}

// A `[` with no `]` is a literal in fnmatch. path.Match called it a bad
// pattern, which the old code read as "no match": the rule hid nothing
// and nobody was told.
func TestAnUnclosedBracketIsALiteral(t *testing.T) {
	tmp := t.TempDir()
	write(t, filepath.Join(tmp, "a[b"), "x\n")
	r := NewRoots(map[string][]string{"base": {tmp}})
	r.IgnoreGlobs = []string{"a[b"}
	if _, err := r.Resolve("base", "a[b"); err == nil {
		t.Error("a[b was served under file_ignore_glob [a[b]")
	}
}
