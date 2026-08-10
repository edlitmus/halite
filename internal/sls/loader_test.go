package sls

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/yamlite"
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

func TestLoaderIncludes(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pkgs.sls", "tools:\n  pkg.installed:\n    - name: tmux\n")
	write(t, root, "web/init.sls", `include:
  - pkgs
nginx:
  pkg.installed: []
`)
	ld := &Loader{Root: root, Grains: testGrains()}
	states, err := ld.LoadNames([]string{"web"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("got %d states, want 2", len(states))
	}
	if states[0].ID != "tools" || states[1].ID != "nginx" {
		t.Errorf("include order wrong: %s, %s", states[0].ID, states[1].ID)
	}
	if states[0].Src == states[1].Src {
		t.Errorf("source attribution missing")
	}
}

func TestLoaderIncludeDedupAndCycle(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.sls", "include:\n  - b\nsa:\n  cmd.run:\n    - name: echo a\n")
	write(t, root, "b.sls", "include:\n  - a\nsb:\n  cmd.run:\n    - name: echo b\n")
	ld := &Loader{Root: root, Grains: testGrains()}
	states, err := ld.LoadNames([]string{"a", "b"})
	if err != nil {
		t.Fatalf("cycle should be tolerated via dedup: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("got %d states, want 2 (dedup)", len(states))
	}
}

func TestLoaderDuplicateID(t *testing.T) {
	root := t.TempDir()
	write(t, root, "x.sls", "dup:\n  cmd.run:\n    - name: echo 1\n")
	write(t, root, "y.sls", "dup:\n  cmd.run:\n    - name: echo 2\n")
	ld := &Loader{Root: root, Grains: testGrains()}
	if _, err := ld.LoadNames([]string{"x", "y"}); err == nil {
		t.Fatal("expected duplicate state error")
	}
}

func TestTopMatching(t *testing.T) {
	top := `base:
  '*':
    - base
  'os_family:FreeBSD':
    - freebsd.tuning
  'os_family:Debian':
    - debian-only
  'web*':
    - webserver
  'db*':
    - database
`
	tree, err := yamlite.Parse(top)
	if err != nil {
		t.Fatal(err)
	}
	names, err := MatchTop(tree, testGrains())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"base", "freebsd.tuning", "webserver"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func TestPrereqOrdering(t *testing.T) {
	root := t.TempDir()
	write(t, root, "p.sls", `later:
  cmd.run:
    - name: echo later

early:
  cmd.run:
    - name: echo early
    - prereq:
      - cmd: later
`)
	ld := &Loader{Root: root, Grains: testGrains()}
	states, err := ld.LoadNames([]string{"p"})
	if err != nil {
		t.Fatal(err)
	}
	if states[0].ID != "early" || states[1].ID != "later" {
		t.Errorf("prereq should order early before later: %s, %s", states[0].ID, states[1].ID)
	}
}

func TestPillarIsAvailableToStateTemplates(t *testing.T) {
	root := t.TempDir()
	write(t, root, "web.sls", "conf:\n  file.managed:\n    - name: /etc/nginx.conf\n    - contents: \"listen {{ .Pillar.nginx.port }}\"\n")
	ld := &Loader{
		Root:   root,
		Grains: testGrains(),
		Pillar: map[string]any{"nginx": map[string]any{"port": "8080"}},
	}
	states, err := ld.LoadNames([]string{"web"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := states[0].Args["contents"]; got != "listen 8080" {
		t.Errorf("contents = %v, want \"listen 8080\"", got)
	}
}

// A reused Loader must produce the same plan twice, not an empty second
// plan because the include tracker remembered the first call.
func TestLoaderIsReusable(t *testing.T) {
	root := t.TempDir()
	write(t, root, "web.sls", "run:\n  cmd.run:\n    - name: echo hi\n")
	ld := &Loader{Root: root, Grains: testGrains()}
	for i := 0; i < 2; i++ {
		states, err := ld.LoadNames([]string{"web"})
		if err != nil {
			t.Fatalf("load %d: %v", i, err)
		}
		if len(states) != 1 {
			t.Fatalf("load %d: got %d states, want 1", i, len(states))
		}
	}
}
