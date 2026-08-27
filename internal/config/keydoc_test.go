package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every key has to be documented, and every documented key has to
// exist. Without this a setting added to the table gets a one-line
// meaning and nothing else, which is how a reference becomes a list of
// names an operator still has to read the source to understand.
func TestEveryKeyIsDocumented(t *testing.T) {
	if len(Keys) == 0 {
		t.Fatal("the key table is empty; this check has stopped checking")
	}

	groups := map[string]bool{}
	for _, g := range Groups {
		if groups[g.Name] {
			t.Errorf("two groups are named %q", g.Name)
		}
		groups[g.Name] = true
	}

	seen := map[string]bool{}
	for _, k := range Keys {
		seen[k.Name] = true
		doc, ok := KeyDocs[k.Name]
		if !ok {
			t.Errorf("%s has no entry in KeyDocs", k.Name)
			continue
		}
		if doc.Group == "" {
			t.Errorf("%s is in no group", k.Name)
		} else if !groups[doc.Group] {
			t.Errorf("%s is in group %q, which Groups does not list", k.Name, doc.Group)
		}
		if strings.TrimSpace(doc.Detail) == "" {
			t.Errorf("%s has no detail; the reference would show its one-liner and nothing else", k.Name)
		}
	}
	for name := range KeyDocs {
		if !seen[name] {
			t.Errorf("KeyDocs documents %q, which is not a configuration key", name)
		}
	}
}

// A group nothing is filed under is a heading with no body, which reads
// as a missing section rather than an empty one.
func TestEveryGroupHasKeys(t *testing.T) {
	count := map[string]int{}
	for _, k := range Keys {
		count[KeyDocs[k.Name].Group]++
	}
	for _, g := range Groups {
		if count[g.Name] == 0 {
			t.Errorf("no key is in group %q", g.Name)
		}
		if strings.TrimSpace(g.Intro) == "" {
			t.Errorf("group %q has no introduction", g.Name)
		}
	}
}

// A count written into prose goes stale the first time a setting is
// added, and nobody re-reads the sentence. Any document stating how many
// settings there are is compared with the table.
func TestAnyStatedSettingCountMatchesTheTable(t *testing.T) {
	// The number as it would be written, so a document saying something
	// else is the failure rather than a document saying nothing.
	want := strconv.Itoa(len(Keys))
	pattern := regexp.MustCompile(`\b(\d{2,4}) settings\b`)

	root := filepath.Join("..", "..")
	checked, found := 0, 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "dist", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		for i, line := range strings.Split(string(body), "\n") {
			for _, m := range pattern.FindAllStringSubmatch(line, -1) {
				found++
				if m[1] != want {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s:%d says %s settings; the table holds %s",
						rel, i+1, m[1], want)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no documents were read; this check has stopped checking")
	}
	t.Logf("read %d documents, %d stated counts", checked, found)
}
