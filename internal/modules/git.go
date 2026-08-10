package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	register("git.latest", gitLatest)
}

// gitLatest clones a repository, or fast-forwards an existing checkout to
// the tip of a branch, tag, or commit.
//
//	/usr/local/src/app:
//	  git.latest:
//	    - name: https://github.com/example/app.git
//	    - target: /usr/local/src/app
//	    - rev: main
//	    - depth: 1
//
// It refuses to touch a checkout with local modifications unless force is
// set: silently discarding someone's work in progress is not a change a
// configuration run should make on its own.
func gitLatest(c *Ctx, id string, args map[string]any) Result {
	url := Str(args, "name", id)
	target := Str(args, "target", "")
	rev := Str(args, "rev", "")
	depth := Str(args, "depth", "")
	force := Bool(args, "force", false)

	if target == "" {
		return resFail("git.latest requires a target directory")
	}
	// These become git arguments; a value that looks like an option (say, a
	// pillar-templated `--upload-pack=...` url) must not be parsed as one.
	for _, arg := range []struct{ name, value string }{
		{"name", url}, {"rev", rev}, {"depth", depth},
	} {
		if strings.HasPrefix(arg.value, "-") {
			return resFail("git.latest %s %q must not begin with '-'", arg.name, arg.value)
		}
	}
	if !has("git") {
		return resFail("git is not installed")
	}

	if !isGitRepo(target) {
		if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
			return resFail("%s exists and is not a git repository", target)
		}
		if c.Test {
			return resWould(fmt.Sprintf("would clone %s into %s", url, target))
		}
		return cloneRepo(url, target, rev, depth)
	}

	if remote := gitOutput(target, "config", "--get", "remote.origin.url"); remote != "" && remote != url {
		return resFail("%s tracks %s, not %s", target, remote, url)
	}
	if !force {
		if dirty := gitOutput(target, "status", "--porcelain"); dirty != "" {
			return resFail("%s has local modifications; set force: true to discard them", target)
		}
	}

	if _, errOut, rc, err := runIn("", "git", "-C", target, "fetch", "--tags", "origin"); err != nil || rc != 0 {
		return resFail("fetch %s: %s", url, cmdError(errOut, err))
	}
	wanted := rev
	if wanted == "" {
		wanted = defaultBranch(target)
	}
	before := gitOutput(target, "rev-parse", "HEAD")
	after := resolveRev(target, wanted)
	if after == "" {
		return resFail("%s does not resolve to a commit in %s", wanted, target)
	}
	if before == after {
		return resOK(fmt.Sprintf("%s is up to date at %s", target, short(before)))
	}
	if c.Test {
		return resWould(fmt.Sprintf("would update %s from %s to %s", target, short(before), short(after)))
	}

	checkout := []string{"-C", target, "checkout"}
	if force {
		checkout = append(checkout, "--force")
	}
	checkout = append(checkout, wanted)
	if _, errOut, rc, err := runIn("", "git", checkout...); err != nil || rc != 0 {
		return resFail("checkout %s: %s", wanted, cmdError(errOut, err))
	}
	// A detached tag or commit has nothing to fast-forward; only a branch
	// checkout needs the remote's tip pulled in.
	if isBranch(target, wanted) {
		reset := []string{"-C", target, "reset", "--hard", "origin/" + wanted}
		if _, errOut, rc, err := runIn("", "git", reset...); err != nil || rc != 0 {
			return resFail("update %s: %s", target, cmdError(errOut, err))
		}
	}
	return resChanged(
		fmt.Sprintf("updated %s to %s", target, short(after)),
		map[string]string{"revision": short(before) + " => " + short(after)})
}

func cloneRepo(url, target, rev, depth string) Result {
	argv := []string{"clone"}
	if depth != "" {
		argv = append(argv, "--depth", depth)
	}
	if rev != "" {
		argv = append(argv, "--branch", rev)
	}
	argv = append(argv, url, target)

	if _, errOut, rc, err := runIn("", "git", argv...); err != nil || rc != 0 {
		return resFail("clone %s: %s", url, cmdError(errOut, err))
	}
	head := short(gitOutput(target, "rev-parse", "HEAD"))
	return resChanged(
		fmt.Sprintf("cloned %s into %s at %s", url, target, head),
		map[string]string{"cloned": url, "revision": head})
}

func isGitRepo(target string) bool {
	info, err := os.Stat(filepath.Join(target, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

// gitOutput runs a git command in a repository and returns trimmed stdout,
// or an empty string if it failed. Callers use it for queries where a
// failure and an empty answer mean the same thing.
func gitOutput(target string, args ...string) string {
	argv := append([]string{"-C", target}, args...)
	out, _, rc, err := runIn("", "git", argv...)
	if err != nil || rc != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// defaultBranch is the branch origin's HEAD points at, falling back to the
// currently checked-out branch.
func defaultBranch(target string) string {
	if ref := gitOutput(target, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); ref != "" {
		return strings.TrimPrefix(ref, "origin/")
	}
	if branch := gitOutput(target, "rev-parse", "--abbrev-ref", "HEAD"); branch != "" && branch != "HEAD" {
		return branch
	}
	return "main"
}

// resolveRev finds the commit a revision names, preferring the remote's
// view of a branch over a stale local one.
func resolveRev(target, rev string) string {
	if commit := gitOutput(target, "rev-parse", "--verify", "origin/"+rev+"^{commit}"); commit != "" {
		return commit
	}
	return gitOutput(target, "rev-parse", "--verify", rev+"^{commit}")
}

func isBranch(target, rev string) bool {
	return gitOutput(target, "rev-parse", "--verify", "refs/remotes/origin/"+rev) != ""
}

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}
