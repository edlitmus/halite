package modules

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPipSpecSplitsNameFromConstraint(t *testing.T) {
	cases := []struct {
		raw  string
		name string
	}{
		{"django", "django"},
		{"django==4.2", "django"},
		{"django>=4.2,<5", "django"},
		{"django!=4.1", "django"},
		{"django~=4.2", "django"},
		{"requests[security]", "requests"},
		{"pytest ; python_version > '3.9'", "pytest"},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := parsePipSpec(tc.raw); got.name != tc.name || got.full != tc.raw {
				t.Fatalf("want name %q from %q, got %+v", tc.name, tc.raw, got)
			}
		})
	}
}

func TestPipNamesFoldTheWayPipFoldsThem(t *testing.T) {
	for _, spelling := range []string{"Zope.Interface", "zope_interface", "zope-interface", "ZOPE-INTERFACE"} {
		if got := normalizePipName(spelling); got != "zope-interface" {
			t.Fatalf("%q should fold to zope-interface, got %q", spelling, got)
		}
	}
}

func TestFreezeOutputIsRead(t *testing.T) {
	frozen := `# a comment
Django==4.2.1
requests==2.31.0
-e git+https://example.com/app#egg=app
zope.interface==6.0

`
	installed := pipInstalledSet(frozen)
	if installed["django"] != "4.2.1" {
		t.Fatalf("want django 4.2.1, got %q", installed["django"])
	}
	if installed["zope-interface"] != "6.0" {
		t.Fatalf("the name should fold, got %q", installed["zope-interface"])
	}
	if _, ok := installed["requests"]; !ok {
		t.Fatal("requests should be listed")
	}
	if len(installed) != 3 {
		t.Fatalf("comments, blanks and -e lines are not packages: %v", installed)
	}
}

func TestPipDrift(t *testing.T) {
	installed := map[string]string{"django": "4.2.1", "requests": "2.31.0"}
	cases := []struct {
		name    string
		want    string
		drifted bool
	}{
		{"already installed", "django", false},
		{"pin matches", "django==4.2.1", false},
		{"pin differs", "django==4.2.9", true},
		{"missing", "flask", true},
		{"range is pip's to judge", "django>=4.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pipDrift([]pipSpec{parsePipSpec(tc.want)}, installed)
			if (len(got) > 0) != tc.drifted {
				t.Fatalf("want drift=%v for %q, got %v", tc.drifted, tc.want, got)
			}
		})
	}
}

func TestPipChangesComparesTwoFreezes(t *testing.T) {
	before := map[string]string{"django": "4.2.1", "old": "1.0"}
	after := map[string]string{"django": "4.2.9", "new": "2.0"}

	changes := pipChanges(before, after)
	if changes["django"] != "4.2.1 -> 4.2.9" {
		t.Fatalf("want the version move, got %q", changes["django"])
	}
	if changes["new"] != "installed 2.0" {
		t.Fatalf("want the new package, got %q", changes["new"])
	}
	if changes["old"] != "uninstalled" {
		t.Fatalf("want the removal, got %q", changes["old"])
	}
	if len(pipChanges(before, before)) != 0 {
		t.Fatal("two identical freezes are no change")
	}
}

func TestPipCommandFindsAVirtualenv(t *testing.T) {
	venv := t.TempDir()
	bin := filepath.Join(venv, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	pipPath := filepath.Join(bin, "pip")
	if err := os.WriteFile(pipPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := pipCommand(map[string]any{"bin_env": venv})
	if err != nil || len(got) != 1 || got[0] != pipPath {
		t.Fatalf("want the venv's own pip, got %v (%v)", got, err)
	}
	if _, err := pipCommand(map[string]any{"bin_env": t.TempDir()}); err == nil {
		t.Fatal("a directory that is not a virtualenv should be reported")
	}
	if _, err := pipCommand(map[string]any{"bin_env": filepath.Join(venv, "absent")}); err == nil {
		t.Fatal("a bin_env that does not exist should be reported")
	}
}

func TestPipStatesNeedSomethingToInstall(t *testing.T) {
	venv := t.TempDir()
	bin := filepath.Join(venv, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "pip"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"bin_env": venv, "name": ""}

	if r := pipInstalled(&Ctx{}, "", args); r.Ok {
		t.Fatal("a pip state naming nothing should fail")
	}
	args["requirements"] = filepath.Join(venv, "absent.txt")
	if r := pipInstalled(&Ctx{}, "", args); r.Ok {
		t.Fatal("a requirements file that is not there should fail")
	}
}
