package grains

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGrains(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grains")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingGrainsFileIsNotAnError(t *testing.T) {
	data, err := LoadCustom(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("a host with no grains file is the normal case: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("want no grains, got %v", data)
	}
}

func TestCustomGrainsAreMergedOverDetectedOnes(t *testing.T) {
	path := writeGrains(t, "role: web\nos_family: Overridden\n")
	g := CollectFrom(path)
	if g["role"] != "web" {
		t.Fatalf("want role=web, got %v", g["role"])
	}
	if g["os_family"] != "Overridden" {
		t.Fatalf("a custom grain should win over the detected one, got %v", g["os_family"])
	}
	if g["arch"] == nil {
		t.Fatal("detected grains should survive the merge")
	}
}

func TestNestedAndListGrainsAreRead(t *testing.T) {
	path := writeGrains(t, "roles:\n  - web\n  - cache\nsite:\n  region: eu-west\n")
	data, err := LoadCustom(path)
	if err != nil {
		t.Fatal(err)
	}
	roles, ok := data["roles"].([]any)
	if !ok || len(roles) != 2 || roles[0] != "web" {
		t.Fatalf("want a two-element list, got %#v", data["roles"])
	}
	site, ok := data["site"].(map[string]any)
	if !ok || site["region"] != "eu-west" {
		t.Fatalf("want a nested mapping, got %#v", data["site"])
	}
}

func TestBrokenGrainsFileDoesNotStopCollection(t *testing.T) {
	path := writeGrains(t, "role: [web, cache]\n") // flow collections are a parse error
	if _, err := LoadCustom(path); err == nil {
		t.Fatal("a malformed grains file should be reported")
	}
	if g := CollectFrom(path); g["arch"] == nil {
		t.Fatal("a broken grains file must not stop the host from reporting facts")
	}
}

func TestSavedGrainsReadBackUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grains")
	want := map[string]any{
		"role":    "web",
		"comment": "a: value # with punctuation",
		"empty":   "",
		"port":    "8080",
	}
	if err := SaveCustom(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCustom(path)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: want %q, got %q", k, v, got[k])
		}
	}
}

func TestSavingAnEmptyMapRemovesTheFile(t *testing.T) {
	path := writeGrains(t, "role: web\n")
	if err := SaveCustom(path, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the last grain removed should leave no file behind")
	}
}

func TestCustomPathFollowsTheEnvironment(t *testing.T) {
	t.Setenv("HALITE_GRAINS", "/tmp/custom-grains")
	if got := CustomPath(); got != "/tmp/custom-grains" {
		t.Fatalf("want the environment override, got %q", got)
	}
}
