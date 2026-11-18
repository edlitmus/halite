package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// What issue 752 was: `renumber` rewrote a citation in a file git tracked and
// left the identical citation in a file it did not, so the untracked one kept
// a number that, after renumbering, belonged to somebody else's section.
//
// Driven through the built tool in a real repository rather than by calling
// touchedLines, because the defect was in what git was asked, not in what was
// done with the answer.
func TestRenumberRewritesACitationInAnUncommittedFile(t *testing.T) {
	r := newLedgerRepo(t)

	// The collision: the base already has a 9.1, and this branch adds its
	// own section with the same number.
	r.write("docs/DIVERGENCE.md", baseLedger+"### 9.1 My new section\nmine\n")
	r.write("tracked.go", "package x\n\n// the reason is 9.1\n")
	r.git("add", "docs/DIVERGENCE.md", "tracked.go")
	r.git("commit", "-qm", "a colliding section, cited")

	// The same citation in a file git has never been told about.
	r.write("untracked.go", "package y\n\n// the reason is 9.1\n")

	if out, err := r.ledger("plan"); err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	} else if !strings.Contains(out, "untracked.go") {
		t.Errorf("plan does not list the untracked citation it is about to move:\n%s", out)
	}

	if out, err := r.ledger("renumber"); err != nil {
		t.Fatalf("renumber: %v\n%s", err, out)
	}

	for _, file := range []string{"tracked.go", "untracked.go"} {
		body := r.read(file)
		if strings.Contains(body, "9.1") {
			t.Errorf("%s still cites 9.1, which is now the base's section:\n%s", file, body)
		}
		if !strings.Contains(body, "9.2") {
			t.Errorf("%s was not moved to 9.2:\n%s", file, body)
		}
	}
}

func TestRenumberLeavesAFileTheBranchDidNotTouchAlone(t *testing.T) {
	// The property the whole design rests on, and the one widening the
	// scope could have broken: main may cite 9.1 meaning *its own* 9.1,
	// and that citation must not move.
	r := newLedgerRepo(t)
	r.write("theirs.go", "package x\n\n// the base's own reason is 9.1\n")
	r.git("add", "theirs.go")
	r.git("commit", "-qm", "a citation that belongs to the base")
	r.git("checkout", "-q", "main")
	r.git("merge", "-q", "work")
	r.git("checkout", "-q", "work")

	r.write("docs/DIVERGENCE.md", baseLedger+"### 9.1 My new section\nmine\n")
	r.git("add", "docs/DIVERGENCE.md")
	r.git("commit", "-qm", "a colliding section")

	if out, err := r.ledger("renumber"); err != nil {
		t.Fatalf("renumber: %v\n%s", err, out)
	}
	if body := r.read("theirs.go"); !strings.Contains(body, "9.1") {
		t.Errorf("the base's own citation was repointed:\n%s", body)
	}
}

func TestAnIgnoredFileIsNotRewritten(t *testing.T) {
	// `--exclude-standard` is what keeps build output and scratch notes
	// out. Without it, widening the scope would have handed a rewriting
	// tool every file in the tree that is not committed.
	r := newLedgerRepo(t)
	r.write(".gitignore", "scratch/\n")
	r.write("docs/DIVERGENCE.md", baseLedger+"### 9.1 My new section\nmine\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "a colliding section, and an ignore rule")
	r.write("scratch/notes.go", "package z\n\n// the reason is 9.1\n")

	if out, err := r.ledger("renumber"); err != nil {
		t.Fatalf("renumber: %v\n%s", err, out)
	}
	if body := r.read("scratch/notes.go"); !strings.Contains(body, "9.1") {
		t.Errorf("an ignored file was rewritten:\n%s", body)
	}
}

func TestABinaryFileIsNotRead(t *testing.T) {
	// An untracked binary has no lines worth offering a line-rewriting
	// tool, and renumber would otherwise read and rewrite it. Asserted by
	// the bytes surviving, because a rewrite of a binary is the kind of
	// damage that is noticed later.
	r := newLedgerRepo(t)
	r.write("docs/DIVERGENCE.md", baseLedger+"### 9.1 My new section\nmine\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "a colliding section")

	blob := []byte("header\x00\x01\x02 the reason is 9.1 \x00trailer")
	if err := os.WriteFile(filepath.Join(r.dir, "thing.bin"), blob, 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	if out, err := r.ledger("renumber"); err != nil {
		t.Fatalf("renumber: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(r.dir, "thing.bin"))
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if string(got) != string(blob) {
		t.Errorf("the binary was rewritten:\n%q\nwant\n%q", got, blob)
	}
}

// baseLedger is what every one of these repositories starts from: a chapter
// and one section, which a branch then collides with.
//
// Chapter 9, which the real ledger does not have, so that a fixture number
// never resolves against it and shows up in this repository's own `cites`
// output. 5.677 and 5.677.1 are both about having learnt that twice.
const baseLedger = "## 9. Chapter\n### 9.1 A base section\nbody\n"

// ledgerRepo is a repository with the tool built and a branch checked out.
type ledgerRepo struct {
	t   *testing.T
	dir string
	bin string
	env []string
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}
	return root
}

func newLedgerRepo(t *testing.T) *ledgerRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine, and these tests are about what git is asked")
	}

	tmp := t.TempDir()
	// `.exe`, or `go build -o` writes one and exec.Command cannot find
	// what it was handed. The hook tests did not need this and so did not
	// show it: git runs the hook under bash, which resolves `ledger` to
	// `ledger.exe` by itself, where exec.Command with an absolute path
	// does not. Found by the Windows leg, on a branch whose own entry
	// said Windows had not been tested (5.678).
	bin := filepath.Join(tmp, "ledger")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join(repoRoot(t), "tools", "ledger")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the tool: %v\n%s", err, out)
	}

	r := &ledgerRepo{
		t:   t,
		dir: filepath.Join(tmp, "work"),
		bin: bin,
		// The branch is measured against main rather than origin/main,
		// which a repository with no remote does not have.
		env: []string{"LEDGER_CMD=" + bin, "LEDGER_BASE=main"},
	}
	mustRun(t, tmp, nil, "git", "init", "-q", "-b", "main", r.dir)
	r.git("config", "user.email", "t@example.invalid")
	r.git("config", "user.name", "t")
	r.write("docs/DIVERGENCE.md", baseLedger)
	r.git("add", "-A")
	r.git("commit", "-qm", "base")
	r.git("checkout", "-q", "-b", "work")
	return r
}

func (r *ledgerRepo) write(name, body string) {
	r.t.Helper()
	path := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

func (r *ledgerRepo) read(name string) string {
	r.t.Helper()
	body, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil {
		r.t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

func (r *ledgerRepo) git(args ...string) {
	r.t.Helper()
	mustRun(r.t, r.dir, r.env, "git", args...)
}

// ledger returns the output rather than failing on it, because some of these
// tests are about a refusal.
func (r *ledgerRepo) ledger(args ...string) (string, error) {
	r.t.Helper()
	cmd := exec.Command(r.bin, args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), r.env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
