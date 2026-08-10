package modules

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// originRepo builds a real repository with one commit on a branch named
// main, and returns its path. git.latest shells out to git, so testing it
// against anything but a real repository would test the mock instead.
func originRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "test@example.invalid")
	gitIn(t, dir, "config", "user.name", "halite test")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "README")
	gitIn(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestGitLatestClonesThenStaysPut(t *testing.T) {
	origin := originRepo(t)
	target := filepath.Join(t.TempDir(), "checkout")

	res := gitLatest(&Ctx{}, origin, map[string]any{"target": target, "rev": "main"})
	if !res.Ok || !res.Changed {
		t.Fatalf("clone: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(target, "README")); err != nil {
		t.Fatalf("clone produced no working tree: %v", err)
	}

	again := gitLatest(&Ctx{}, origin, map[string]any{"target": target, "rev": "main"})
	if !again.Ok || again.Changed {
		t.Errorf("second run changed something: %+v", again)
	}
}

func TestGitLatestFastForwardsToNewCommits(t *testing.T) {
	origin := originRepo(t)
	target := filepath.Join(t.TempDir(), "checkout")
	if res := gitLatest(&Ctx{}, origin, map[string]any{"target": target, "rev": "main"}); !res.Ok {
		t.Fatalf("clone: %+v", res)
	}

	if err := os.WriteFile(filepath.Join(origin, "README"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, origin, "commit", "-q", "-am", "second")

	dry := gitLatest(&Ctx{Test: true}, origin, map[string]any{"target": target, "rev": "main"})
	if !dry.Ok || !dry.Changed {
		t.Fatalf("test mode should report a pending update: %+v", dry)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "README")); string(got) != "one\n" {
		t.Error("test mode updated the working tree")
	}

	res := gitLatest(&Ctx{}, origin, map[string]any{"target": target, "rev": "main"})
	if !res.Ok || !res.Changed {
		t.Fatalf("update: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(target, "README"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two\n" {
		t.Errorf("README = %q, want the new commit's content", got)
	}
}

func TestGitLatestRefusesToDiscardLocalChanges(t *testing.T) {
	origin := originRepo(t)
	target := filepath.Join(t.TempDir(), "checkout")
	if res := gitLatest(&Ctx{}, origin, map[string]any{"target": target, "rev": "main"}); !res.Ok {
		t.Fatalf("clone: %+v", res)
	}
	if err := os.WriteFile(filepath.Join(target, "README"), []byte("work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := gitLatest(&Ctx{}, origin, map[string]any{"target": target, "rev": "main"})
	if res.Ok {
		t.Fatal("a dirty checkout must not be updated silently")
	}
	if !strings.Contains(res.Comment, "force") {
		t.Errorf("comment should mention force: %q", res.Comment)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "README")); string(got) != "work in progress\n" {
		t.Error("local modifications were discarded")
	}
}

func TestGitLatestRefusesADifferentRemote(t *testing.T) {
	origin := originRepo(t)
	other := originRepo(t)
	target := filepath.Join(t.TempDir(), "checkout")
	if res := gitLatest(&Ctx{}, origin, map[string]any{"target": target, "rev": "main"}); !res.Ok {
		t.Fatalf("clone: %+v", res)
	}

	res := gitLatest(&Ctx{}, other, map[string]any{"target": target, "rev": "main"})
	if res.Ok {
		t.Fatal("a checkout of a different repository must not be repointed silently")
	}
}

func TestGitLatestRefusesANonRepositoryDirectory(t *testing.T) {
	origin := originRepo(t)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "somebody-elses-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := gitLatest(&Ctx{}, origin, map[string]any{"target": target})
	if res.Ok {
		t.Fatal("a non-empty, non-repository target must be refused")
	}
}

// Values that reach git as arguments must not be parsable as options.
func TestGitLatestRejectsOptionLookingValues(t *testing.T) {
	target := t.TempDir()
	cases := []map[string]any{
		{"name": "--upload-pack=/tmp/evil", "target": target},
		{"name": "https://example.com/r.git", "target": target, "rev": "-q"},
		{"name": "https://example.com/r.git", "target": target, "depth": "-1x"},
	}
	for _, args := range cases {
		r := gitLatest(&Ctx{}, "repo", args)
		if r.Ok {
			t.Errorf("args %v: accepted, want refusal", args)
		}
		if !strings.Contains(r.Comment, "must not begin with '-'") {
			t.Errorf("args %v: comment %q, want the option-injection refusal", args, r.Comment)
		}
	}
}
