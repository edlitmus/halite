package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fragmentDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A fragment is a mapping of names to definitions and they merge, so an
// operator can drop one beacon into one file without restating the rest
// — and a node can write its own runtime changes into a file of its own
// without touching what a package manager put there.
func TestDefinitionFragmentsMergeInLexicalOrder(t *testing.T) {
	dir := fragmentDir(t, map[string]string{
		"10-base.yaml":    "diskusage:\n  - /: 85%\n",
		"99-runtime.yaml": "load:\n  - 1m: 2.0\n",
	})
	merged, files, err := LoadDefinitions(dir, "beacons")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || !strings.HasSuffix(files[0], "10-base.yaml") {
		t.Errorf("read %v", files)
	}
	if !merged.Has("diskusage") || !merged.Has("load") {
		t.Errorf("the merge produced %v", merged.StringKeys())
	}
}

// A directory that is not there is not an error: most nodes have none.
func TestAnAbsentDefinitionDirectoryIsNotAnError(t *testing.T) {
	merged, files, err := LoadDefinitions(filepath.Join(t.TempDir(), "nothing"), "beacons")
	if err != nil {
		t.Fatal(err)
	}
	if merged != nil || files != nil {
		t.Errorf("an absent directory produced %v / %v", merged, files)
	}
}

// A fragment written in the shape of the main configuration file is
// refused with the fix in the message.
//
// Read literally it produces a beacon called `beacons`, and the node
// then complains about a name nobody typed. The check is per fragment
// rather than on the merged result, because a wrapped file mixed with
// an unwrapped one merges into something with several keys and the
// wrapper would slip through.
func TestAWrappedFragmentIsRefusedByName(t *testing.T) {
	dir := fragmentDir(t, map[string]string{
		"10-base.yaml":    "diskusage:\n  - /: 85%\n",
		"50-wrapped.yaml": "beacons:\n  load:\n    - 1m: 2.0\n",
	})
	_, _, err := LoadDefinitions(dir, "beacons")
	if err == nil {
		t.Fatal("a wrapped fragment was accepted")
	}
	if !strings.Contains(err.Error(), "50-wrapped.yaml") {
		t.Errorf("the refusal should name the file: %v", err)
	}
	if !strings.Contains(err.Error(), "outdent") {
		t.Errorf("the refusal should say what to do: %v", err)
	}
}

// A key that happens to match the kind but is not a mapping is a
// definition, not a wrapper.
func TestAScalarNamedLikeTheKindIsNotAWrapper(t *testing.T) {
	dir := fragmentDir(t, map[string]string{
		"10.yaml": "schedule: 5\nnightly:\n  function: state.apply\n",
	})
	merged, _, err := LoadDefinitions(dir, "schedule")
	if err != nil {
		t.Fatalf("a scalar was read as a wrapper: %v", err)
	}
	if v, _ := merged.Get("schedule"); v != int64(5) {
		t.Errorf("the value came back as %v", v)
	}
}
