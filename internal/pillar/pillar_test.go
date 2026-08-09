package pillar

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func testGrains() map[string]any {
	return map[string]any{"os_family": "FreeBSD", "host": "web1", "os": "FreeBSD"}
}

func TestMissingTreeYieldsEmptyPillar(t *testing.T) {
	data, err := (&Loader{Root: filepath.Join(t.TempDir(), "absent"), Grains: testGrains()}).Load()
	if err != nil {
		t.Fatalf("a missing pillar tree must not be an error: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %v, want empty pillar", data)
	}
}

func TestTopTargetingSelectsMatchingFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "top.sls", `base:
  '*':
    - common
  'os_family:FreeBSD':
    - bsd
  'db*':
    - database
`)
	write(t, root, "common.sls", "shell: /bin/sh\n")
	write(t, root, "bsd.sls", "shell: /bin/tcsh\n")
	write(t, root, "database.sls", "port: \"5432\"\n")

	data, err := (&Loader{Root: root, Grains: testGrains()}).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := data["shell"]; got != "/bin/tcsh" {
		t.Errorf("later match must win: got %v, want /bin/tcsh", got)
	}
	if _, ok := data["port"]; ok {
		t.Error("host web1 must not match the 'db*' target")
	}
}

func TestNestedMapsDeepMerge(t *testing.T) {
	root := t.TempDir()
	write(t, root, "top.sls", "base:\n  '*':\n    - a\n    - b\n")
	write(t, root, "a.sls", "nginx:\n  port: \"80\"\n  user: www\n")
	write(t, root, "b.sls", "nginx:\n  port: \"8080\"\n")

	data, err := (&Loader{Root: root, Grains: testGrains()}).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	nginx, ok := data["nginx"].(map[string]any)
	if !ok {
		t.Fatalf("nginx is %T, want map", data["nginx"])
	}
	if nginx["port"] != "8080" {
		t.Errorf("port = %v, want 8080 (later file wins)", nginx["port"])
	}
	if nginx["user"] != "www" {
		t.Errorf("user = %v, want www (untouched key survives the merge)", nginx["user"])
	}
}

func TestIncludesLoadFirstAndCyclesTerminate(t *testing.T) {
	root := t.TempDir()
	write(t, root, "top.sls", "base:\n  '*':\n    - a\n")
	write(t, root, "a.sls", "include:\n  - sub/b\nkey: from-a\n")
	write(t, root, "sub/b.sls", "include:\n  - a\nkey: from-b\nonly_in_b: yes\n")

	data, err := (&Loader{Root: root, Grains: testGrains()}).Load()
	if err != nil {
		t.Fatalf("include cycle must terminate: %v", err)
	}
	if data["key"] != "from-a" {
		t.Errorf("key = %v, want from-a (the including file overrides its include)", data["key"])
	}
	if data["only_in_b"] != "yes" {
		t.Errorf("included data missing: %v", data)
	}
}

func TestGrainsAreAvailableInPillarTemplates(t *testing.T) {
	root := t.TempDir()
	write(t, root, "top.sls", "base:\n  '*':\n    - g\n")
	write(t, root, "g.sls", "family: {{ .Grains.os_family }}\n")

	data, err := (&Loader{Root: root, Grains: testGrains()}).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if data["family"] != "FreeBSD" {
		t.Errorf("family = %v, want FreeBSD", data["family"])
	}
}

func TestListsAreReplacedNotMerged(t *testing.T) {
	root := t.TempDir()
	write(t, root, "top.sls", "base:\n  '*':\n    - a\n    - b\n")
	write(t, root, "a.sls", "hosts:\n  - one\n  - two\n")
	write(t, root, "b.sls", "hosts:\n  - three\n")

	data, err := (&Loader{Root: root, Grains: testGrains()}).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	hosts, ok := data["hosts"].([]any)
	if !ok {
		t.Fatalf("hosts is %T, want list", data["hosts"])
	}
	if len(hosts) != 1 || hosts[0] != "three" {
		t.Errorf("hosts = %v, want [three]", hosts)
	}
}
