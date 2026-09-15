package builtin

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// git.latest drives the real git binary, so one of these drives a real
// repository. The shared newCtx installs a RecordingRunner, which returns
// success without running anything -- useful for asking which flags were
// built, and useless for asking what git did with them. Both questions
// are worth answering, so both are asked, and each test says which runner
// it is using and why.

// newRunningCtx is a context whose commands actually run.
func newRunningCtx() *exec.Context {
	return &exec.Context{
		Ctx:    context.Background(),
		Grains: value.MapOf("os", "Ubuntu", "os_family", "Debian"),
		Pillar: value.NewMap(0),
		Config: value.NewMap(0),
		NodeID: "test.node",
		Env:    "base",
		Runner: &exec.OSRunner{},
	}
}

func skipWithoutGit(t *testing.T) {
	t.Helper()
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("no git on this machine")
	}
}

// runGit runs git for the test's own setup, which is separate from the
// state under test.
func runGit(t *testing.T, dir string, argv ...string) string {
	t.Helper()
	cmd := osexec.Command("git", argv...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(argv, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// A tag that no branch reaches, pushed after the clone, is the case
// fetch_tags exists for: a plain fetch brings down only the tags
// reachable from the branches it fetched. Checked against the git on this
// machine before any of it was written -- `git fetch origin` leaves such
// a tag absent and `git fetch --tags origin` brings it down.
func TestGitLatestFetchTags(t *testing.T) {
	skipWithoutGit(t)

	for _, tc := range []struct {
		name      string
		fetchTags any
		wantTag   bool
	}{
		{"true", true, true},
		{"false", false, false},
		{"unset, which is true as Salt has it", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			remote := filepath.Join(dir, "remote")
			work := filepath.Join(dir, "work")
			target := filepath.Join(dir, "target")

			if err := os.MkdirAll(remote, 0o755); err != nil {
				t.Fatal(err)
			}
			runGit(t, remote, "init", "-q", "--bare", ".")
			runGit(t, dir, "clone", "-q", remote, work)
			if err := os.WriteFile(filepath.Join(work, "f"), []byte("one\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, work, "add", "f")
			runGit(t, work, "commit", "-qm", "one")
			runGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")

			r := New()
			first := value.MapOf("name", remote, "target", target, "rev", "main")
			res, err := r.States.Call(newRunningCtx(), "git.latest", first)
			if err != nil {
				t.Fatalf("the first run failed: %v", err)
			}
			if !res.Succeeded() {
				t.Fatalf("the first run did not succeed: %s", res.Comment)
			}
			if tags := runGit(t, target, "tag"); tags != "" {
				t.Fatalf("a tag exists before one was created: %q", tags)
			}

			// A tag on a commit that no branch points at, created after
			// the checkout already existed.
			runGit(t, work, "checkout", "-q", "--detach")
			if err := os.WriteFile(filepath.Join(work, "f"), []byte("two\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, work, "commit", "-qam", "two")
			runGit(t, work, "tag", "unreachable")
			runGit(t, work, "push", "-q", "origin", "unreachable")

			second := value.MapOf("name", remote, "target", target, "rev", "main")
			if tc.fetchTags != nil {
				second.Set("fetch_tags", tc.fetchTags)
			}
			if _, err := r.States.Call(newRunningCtx(), "git.latest", second); err != nil {
				t.Fatalf("the second run failed: %v", err)
			}

			got := runGit(t, target, "tag") == "unreachable"
			if got != tc.wantTag {
				t.Errorf("the unreachable tag present = %v, want %v", got, tc.wantTag)
			}
		})
	}
}

// And the flag itself, recorded rather than run, so that the argument
// vector is asserted on every platform including the ones with no git.
func TestGitLatestPassesTagsToFetch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fetchTags any
		want      bool
	}{
		{"unset", nil, true},
		{"true", true, true},
		{"false", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// A directory that looks like a checkout, so the state fetches
			// rather than clones.
			if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			args := value.MapOf("name", "u", "target", dir, "rev", "main")
			if tc.fetchTags != nil {
				args.Set("fetch_tags", tc.fetchTags)
			}

			runner := &exec.RecordingRunner{}
			c := newRunningCtx()
			c.Runner = runner
			c.Lookup = func(string) string { return "/usr/bin/git" }

			r := New()
			if _, err := r.States.Call(c, "git.latest", args); err != nil {
				t.Fatalf("%v", err)
			}

			var fetch string
			for _, cmd := range runner.RanCommands() {
				if strings.Contains(cmd, " fetch ") {
					fetch = cmd
				}
			}
			if fetch == "" {
				t.Fatalf("no fetch was run; commands were %v", runner.RanCommands())
			}
			if got := strings.Contains(fetch, "--tags"); got != tc.want {
				t.Errorf("fetch was %q; --tags present = %v, want %v", fetch, got, tc.want)
			}
		})
	}
}
