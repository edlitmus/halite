package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// `git.latest` under `--test` writes nothing, and still predicts the move.
//
// It used to fetch, on the reasoning that "a fetch is a network call that
// changes nothing in the working tree". True of the working tree, false of
// the repository: this test measures it, because that is the only way the
// claim could have been checked in the first place. Against the real git, a
// `fetch --tags` writes seven files under `.git`, moves the
// remote-tracking refs, and creates a tag that was not there.
//
// No root, no network, no live gate: a bare repository and a clone in a
// directory the test owns, and a `file://` remote. That is the shape
// `openssl_cert`'s round trip has, and the reason it runs wherever the
// suite does.
func TestGitLatestUnderTestModeWritesNothing(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on this machine, and this test drives the real one")
	}

	dir := t.TempDir()
	origin := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")
	clone := filepath.Join(dir, "clone")

	run := func(cwd string, argv ...string) string {
		t.Helper()
		cmd := exec.Command(git, argv...)
		cmd.Dir = cwd
		// A committer identity and no global configuration, so the test
		// does not depend on whose machine it is running on.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=halite", "GIT_AUTHOR_EMAIL=halite@example",
			"GIT_COMMITTER_NAME=halite", "GIT_COMMITTER_EMAIL=halite@example",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(argv, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run(dir, "init", "--quiet", "--bare", origin)
	// The bare repository's HEAD has to name the branch that will exist,
	// or the clone below comes out empty with a warning rather than an
	// error -- which is how the first attempt at this test measured
	// nothing at all.
	run(origin, "symbolic-ref", "HEAD", "refs/heads/main")
	run(dir, "init", "--quiet", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "f")
	run(work, "commit", "--quiet", "-m", "one")
	run(work, "remote", "add", "origin", origin)
	run(work, "push", "--quiet", "origin", "main")
	run(dir, "clone", "--quiet", origin, clone)

	// The remote moves on, with a tag, which is what the fetch used to
	// bring down.
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "commit", "--quiet", "-am", "two")
	run(work, "tag", "v9")
	run(work, "push", "--quiet", "origin", "main", "--tags")
	head := run(work, "rev-parse", "HEAD")

	before := gitDirSnapshot(t, clone)
	res, err := New().States.Call(testCtx(t), "git.latest",
		value.MapOf("name", origin, "target", clone, "rev", "main"))
	if err != nil {
		t.Fatalf("git.latest: %v", err)
	}

	// It predicted the move, which is the half that must not be lost.
	if res.Result != nil {
		t.Errorf("a dry run reported a result rather than a prediction: %+v", res)
	}
	if !res.HasChanges() {
		t.Errorf("the dry run predicted no change, and the remote is two commits ahead: %s", res.Comment)
	}
	if !strings.Contains(res.Comment, head[:7]) {
		t.Errorf("the prediction does not name the commit it would move to (%s): %s", head[:7], res.Comment)
	}

	// And wrote nothing.
	after := gitDirSnapshot(t, clone)
	if diff := snapshotDiff(before, after); len(diff) > 0 {
		t.Errorf("a dry run wrote %d file(s) under .git: %s", len(diff), strings.Join(diff, ", "))
	}
	if tags := run(clone, "tag", "-l"); tags != "" {
		t.Errorf("a dry run brought down the tag %q", tags)
	}

	// The same call outside test mode does move it, or the test above
	// would pass against a state that does nothing at all.
	applied, err := New().States.Call(realCtx(t), "git.latest",
		value.MapOf("name", origin, "target", clone, "rev", "main"))
	if err != nil {
		t.Fatalf("git.latest: %v", err)
	}
	if !applied.Succeeded() || !applied.HasChanges() {
		t.Fatalf("the real run did not move the checkout: %+v", applied)
	}
	if got := run(clone, "rev-parse", "HEAD"); got != head {
		t.Errorf("after the real run HEAD is %s, want %s", got, head)
	}
}

// gitDirSnapshot records every file under .git with its size and mode, for
// a before-and-after comparison.
func gitDirSnapshot(t *testing.T, repo string) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(repo, ".git")
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = info.Mode().String() + ":" + strconv.FormatInt(info.Size(), 10)
		return nil
	}); err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no files, so this test is comparing nothing", root)
	}
	return out
}

func snapshotDiff(before, after map[string]string) []string {
	var diff []string
	for path, state := range after {
		if before[path] != state {
			diff = append(diff, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			diff = append(diff, "removed:"+path)
		}
	}
	sort.Strings(diff)
	return diff
}
