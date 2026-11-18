package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The hook, driven by git rather than by calling its parts.
//
// A hook that has never been invoked by a real `git push` is an untested
// hook: everything interesting about it is in what git hands it and what git
// does with its exit status. These tests build the tool, point
// `core.hooksPath` at the committed `.githooks`, and push for real between
// two local repositories.
func TestThePrePushHookRefusesAnUnacknowledgedPush(t *testing.T) {
	env := newHookRepo(t)

	// A commit that cites the section the branch itself added.
	env.write("note.go", "package x\n\n// the reason is 9.1\n")
	env.git("add", "note.go")
	env.git("commit", "-qm", "cite it")

	out, err := env.push()
	if err == nil {
		t.Fatalf("the push was allowed with no acknowledgement:\n%s", out)
	}
	for _, want := range []string{"note.go:3", "9.1", ackEnv} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, out)
		}
	}
}

func TestThePrePushHookAllowsAnAcknowledgedPush(t *testing.T) {
	env := newHookRepo(t)
	env.write("note.go", "package x\n\n// the reason is 9.1\n")
	env.git("add", "note.go")
	env.git("commit", "-qm", "cite it")

	env.extraEnv = append(env.extraEnv, ackEnv+"=9.1")
	if out, err := env.push(); err != nil {
		t.Fatalf("an acknowledged push was refused: %v\n%s", err, out)
	}
}

func TestThePrePushHookIsSilentWithNoReferences(t *testing.T) {
	// The common push. If this asked anything, everybody would learn
	// --no-verify instead.
	env := newHookRepo(t)
	env.write("note.go", "package x\n\n// nothing cited here\n")
	env.git("add", "note.go")
	env.git("commit", "-qm", "no citation")

	out, err := env.push()
	if err != nil {
		t.Fatalf("a push with no references was refused: %v\n%s", err, out)
	}
	if strings.Contains(out, "reference") {
		t.Errorf("the hook had something to say about an empty list:\n%s", out)
	}
}

func TestThePrePushHookLetsNoVerifyThrough(t *testing.T) {
	// Documented as the escape, so it is tested as one. A bypass that
	// turned out not to work would be found at the worst moment.
	env := newHookRepo(t)
	env.write("note.go", "package x\n\n// the reason is 9.1\n")
	env.git("add", "note.go")
	env.git("commit", "-qm", "cite it")

	if out, err := env.push("--no-verify"); err != nil {
		t.Fatalf("--no-verify was refused: %v\n%s", err, out)
	}
}

// hookRepo is a pair of repositories with the hook installed.
type hookRepo struct {
	t        *testing.T
	dir      string
	extraEnv []string
}

func newHookRepo(t *testing.T) *hookRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine, so the hook cannot be driven by one")
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	hooks := filepath.Join(root, ".githooks")
	if _, err := os.Stat(filepath.Join(hooks, "pre-push")); err != nil {
		t.Fatalf("the committed hook is not where this test expects it: %v", err)
	}

	tmp := t.TempDir()
	bin := filepath.Join(tmp, "ledger")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join(root, "tools", "ledger")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the tool: %v\n%s", err, out)
	}

	env := &hookRepo{t: t, dir: filepath.Join(tmp, "work")}
	env.extraEnv = []string{
		"LEDGER_CMD=" + bin,
		// The branch is measured against this rather than origin/main,
		// which a throwaway repository does not have.
		"LEDGER_BASE=main",
	}

	remote := filepath.Join(tmp, "remote.git")
	mustRun(t, tmp, nil, "git", "init", "-q", "--bare", remote)
	mustRun(t, tmp, nil, "git", "init", "-q", "-b", "main", env.dir)
	env.git("config", "user.email", "t@example.invalid")
	env.git("config", "user.name", "t")
	env.git("config", "core.hooksPath", hooks)
	env.git("remote", "add", "origin", remote)

	// A base with one section, committed on main, so that a citation on
	// the branch has something to resolve to.
	//
	// Chapter 9, which the real ledger does not have. A fixture numbered
	// 5.1 resolves against the real ledger -- `Branch coverage` -- so every
	// run of the tool on this repository listed four references to a
	// section these tests have nothing to do with. Found by the hook
	// refusing its own branch's push and printing them at the top.
	env.write("docs/DIVERGENCE.md", "## 9. Chapter\n### 9.1 A section to cite\nbody\n")
	env.git("add", "-A")
	env.git("commit", "-qm", "base")
	env.git("push", "-q", "origin", "main")
	env.git("checkout", "-q", "-b", "work")
	return env
}

func (h *hookRepo) write(name, body string) {
	h.t.Helper()
	path := filepath.Join(h.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		h.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		h.t.Fatalf("write %s: %v", name, err)
	}
}

func (h *hookRepo) git(args ...string) {
	h.t.Helper()
	mustRun(h.t, h.dir, h.extraEnv, "git", args...)
}

// push returns the combined output rather than failing, because a refused
// push is the expected result in most of these tests.
func (h *hookRepo) push(args ...string) (string, error) {
	h.t.Helper()
	cmd := exec.Command("git", append([]string{"push"}, append(args, "origin", "work")...)...)
	cmd.Dir = h.dir
	cmd.Env = append(os.Environ(), h.extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustRun(t *testing.T, dir string, extraEnv []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}
