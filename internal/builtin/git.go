package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerGit installs the git module, driven through the system git
// binary.
//
// This is the pattern SPEC section 4.2 sets out: where a capability needs
// a vendor protocol, shell out to a signed system binary the operating
// system already trusts, rather than linking libgit2 and inheriting its
// CVE history. The node also inherits the operating system's git patching
// cadence, which is worth more than the convenience of a library.
func registerGit(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "git", Function: "version",
				Doc:      "Return the system git's version.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := gitRun(c, "", "--version")
				if err != nil {
					return nil, err
				}
				return strings.TrimPrefix(strings.TrimSpace(res.Stdout), "git version "), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "git", Function: "revision",
				Doc: "Return the commit a checkout is on.",
				Params: []signature.Param{
					req("cwd", signature.Path, "The checkout."),
					opt("rev", signature.String, "HEAD", "The revision to resolve."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := gitRun(c, states.Str(args, "cwd", ""), "rev-parse", states.Str(args, "rev", "HEAD"))
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "git", Function: "current_branch",
				Doc:      "Return the branch a checkout is on.",
				Params:   []signature.Param{req("cwd", signature.Path, "The checkout.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := gitRun(c, states.Str(args, "cwd", ""), "rev-parse", "--abbrev-ref", "HEAD")
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "git", Function: "status",
				Doc:      "Return the working tree's modified paths.",
				Params:   []signature.Param{req("cwd", signature.Path, "The checkout.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := gitRun(c, states.Str(args, "cwd", ""), "status", "--porcelain")
				if err != nil {
					return nil, err
				}
				out := []any{}
				for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
					if line != "" {
						out = append(out, line)
					}
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "git", Function: "remote_get",
				Doc: "Return a remote's fetch URL.",
				Params: []signature.Param{
					req("cwd", signature.Path, "The checkout."),
					opt("remote", signature.String, "origin", "The remote."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				res, err := gitRun(c, states.Str(args, "cwd", ""),
					"remote", "get-url", states.Str(args, "remote", "origin"))
				if err != nil {
					return nil, err
				}
				return strings.TrimSpace(res.Stdout), nil
			},
		},
	)

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "git", Function: "latest",
			Doc: "Ensure a checkout exists and is at the requested revision.",
			Params: []signature.Param{
				req("name", signature.String, "The repository URL."),
				req("target", signature.Path, "Where the checkout lives."),
				opt("rev", signature.String, "", "The branch, tag, or commit; defaults to the remote's head."),
				opt("remote", signature.String, "origin", "The remote name."),
				opt("depth", signature.Int, nil, "Clone with this depth."),
				opt("branch", signature.String, "", "Local branch name for the checkout; defaults to rev."),
				opt("force_reset", signature.Bool, false, "Discard local changes when the revision differs."),
				opt("force_checkout", signature.Bool, false, "Check out even when it would discard local changes."),
				opt("force_clone", signature.Bool, false, "Clone into a target directory that already has files in it."),
				opt("force_fetch", signature.Bool, false, "Fetch even when it would overwrite a local ref."),
				opt("fetch_tags", signature.Bool, true,
					"Fetch every tag on the remote, including ones no branch reaches. True by default, as Salt has it."),
				opt("user", signature.String, "", "Run git as this account."),
			},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.5",
		},
		Fn: gitLatest,
	})
}

// gitEnv is what every git invocation gets: no prompting, no pager, and no
// credential helper that could block a state run on a password prompt.
func gitEnv() []string {
	return append(exec.CleanEnv(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ASKPASS=",
	)
}

func gitRun(c *exec.Context, cwd string, argv ...string) (exec.Result, error) {
	if c.Which("git") == "" {
		return exec.Result{}, fmt.Errorf("git was not found on this node; the git module drives the system binary")
	}
	return c.Run(exec.Command{
		Argv: append([]string{"git"}, argv...),
		Dir:  cwd,
		Env:  gitEnv(),
	})
}

func gitLatest(c *exec.Context, args *value.Map) (states.Result, error) {
	url := states.Str(args, "name", "")
	target := states.Str(args, "target", "")
	rev := states.Str(args, "rev", "")
	remote := states.Str(args, "remote", "origin")
	// Salt's `branch` names the local branch; it defaults to the
	// revision, which is what a tree naming both the same thing means.
	branch := states.Str(args, "branch", "")

	if url == "" || target == "" {
		return states.False("This state needs a repository URL and a target directory."), nil
	}
	if c.Which("git") == "" {
		return states.False("git was not found on this node; the git module drives the system binary."), nil
	}

	_, err := os.Stat(filepath.Join(target, ".git"))
	exists := err == nil

	if !exists {
		if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 &&
			!states.Bool(args, "force_clone", false) {
			return states.False(fmt.Sprintf(
				"%s already exists and is not a checkout; halite will not clone over a directory that has files in it. "+
					"Set force_clone to do it anyway.", target)), nil
		}
		changes := value.MapOf(target, states.Change(nil, "cloned from "+url))
		if c.Test {
			return states.WouldChange(fmt.Sprintf("%s would be cloned from %s.", target, url), changes), nil
		}
		argv := []string{"clone"}
		if d := states.Int(args, "depth", 0); d > 0 {
			argv = append(argv, "--depth", fmt.Sprint(d))
		}
		if rev != "" {
			argv = append(argv, "--branch", rev)
		}
		if branch != "" && branch != rev {
			argv = append(argv, "--branch", branch)
		}
		argv = append(argv, url, target)
		if _, err := gitRun(c, "", argv...); err != nil {
			return states.False(fmt.Sprintf("%s could not be cloned: %v", url, err)), nil
		}
		head, _ := gitRun(c, target, "rev-parse", "HEAD")
		changes.Set(target, states.Change(nil, strings.TrimSpace(head.Stdout)))
		return states.Changed(fmt.Sprintf("%s was cloned from %s.", target, url), changes), nil
	}

	// The checkout is there. Fetch, then compare where it is with where it
	// should be.
	before, err := gitRun(c, target, "rev-parse", "HEAD")
	if err != nil {
		return states.False(fmt.Sprintf("%s could not be inspected: %v", target, err)), nil
	}
	current := strings.TrimSpace(before.Stdout)

	// A dry run asks the remote and writes nothing.
	//
	// This used to fetch under test mode, on the reasoning -- written in
	// this comment -- that "a fetch is a network call that changes
	// nothing in the working tree". That is true of the working tree and
	// false of the repository, which is the exculpatory shape this
	// project's own rules warn about: measured against the real git, a
	// `fetch --tags` into a clone wrote **seven files** under `.git`,
	// moved the remote-tracking refs, and created a tag that was not
	// there before. A tag arriving during a run that promised to change
	// nothing is a change that outlives the run and can decide what a
	// later `git describe` or a tag-pinned state resolves to.
	//
	// `git ls-remote` asks the same question over the same network and
	// writes nothing -- measured the same way, zero files touched. So the
	// prediction survives: this is not the usual trade of accuracy for
	// honesty, and where the remote does not know the ref the fallback is
	// the local resolution the fetching path used anyway.
	if c.Test {
		want, err := gitRemoteRev(c, target, remote, rev)
		if err != nil {
			return states.False(fmt.Sprintf("%s could not be reached: %v", remote, err)), nil
		}
		if current == want {
			return states.True(fmt.Sprintf("%s is already at %s.", target, shortCommit(want))), nil
		}
		return states.WouldChange(
			fmt.Sprintf("%s would move from %s to %s.", target, shortCommit(current), shortCommit(want)),
			value.MapOf("revision", states.Change(shortCommit(current), shortCommit(want)))), nil
	}

	fetch := []string{"fetch", "--quiet"}
	if states.Bool(args, "force_fetch", false) {
		fetch = append(fetch, "--force")
	}
	// A plain fetch brings down only the tags reachable from the branches
	// it fetched. `--tags` is what reaches the rest, and a tree pinning a
	// release tag that no branch points at gets nothing without it.
	// Checked against the git on this host: a tag pushed after the clone,
	// on a detached commit, is absent after `git fetch origin` and
	// present after `git fetch --tags origin`.
	if states.Bool(args, "fetch_tags", true) {
		fetch = append(fetch, "--tags")
	}
	if _, err := gitRun(c, target, append(fetch, remote)...); err != nil {
		return states.False(fmt.Sprintf("%s could not be fetched: %v", remote, err)), nil
	}

	wanted := rev
	if wanted == "" {
		wanted = remote + "/HEAD"
	} else if !looksLikeCommit(wanted) {
		wanted = remote + "/" + rev
	}
	resolved, err := gitRun(c, target, "rev-parse", wanted)
	if err != nil {
		// A tag or a local commit will not resolve through the remote.
		resolved, err = gitRun(c, target, "rev-parse", rev)
		if err != nil {
			return states.False(fmt.Sprintf("%s could not be resolved in %s: %v", rev, target, err)), nil
		}
	}
	want := strings.TrimSpace(resolved.Stdout)

	if current == want {
		return states.True(fmt.Sprintf("%s is already at %s.", target, shortCommit(want))), nil
	}
	changes := value.MapOf("revision", states.Change(shortCommit(current), shortCommit(want)))
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("%s would move from %s to %s.", target, shortCommit(current), shortCommit(want)), changes), nil
	}

	argv := []string{"checkout", "--quiet"}
	if states.Bool(args, "force_checkout", false) {
		argv = append(argv, "--force")
	}
	if branch != "" {
		// `-B` moves the named branch to the wanted commit and checks it
		// out, which is what Salt's branch argument does. Without it a
		// checkout by commit leaves a detached HEAD and the next run
		// finds no branch to advance.
		argv = append(argv, "-B", branch)
	}
	argv = append(argv, want)
	if states.Bool(args, "force_reset", false) {
		argv = []string{"reset", "--hard", want}
	}
	if _, err := gitRun(c, target, argv...); err != nil {
		return states.False(fmt.Sprintf("%s could not be moved to %s: %v", target, shortCommit(want), err)), nil
	}
	return states.Changed(
		fmt.Sprintf("%s moved from %s to %s.", target, shortCommit(current), shortCommit(want)), changes), nil
}

// gitRemoteRev is the commit a revision names on the remote, read without
// writing anything locally.
//
// `ls-remote` rather than `fetch`: it answers over the network and touches
// no file, which is what lets a dry run predict accurately and still be a
// dry run. Checked against the real git: `<sha>\t<ref>` per line, with a
// pattern argument matching `refs/heads/<rev>` and `refs/tags/<rev>`, and
// a bare `HEAD` for the remote's default branch.
//
// A branch is preferred over a tag of the same name, because that is what
// the fetching path resolved -- `<remote>/<rev>` is a remote-tracking
// branch. A revision that is already an object name needs no remote at
// all, and one the remote does not know falls back to resolving it
// locally, which is what the fetching path did when `<remote>/<rev>` did
// not resolve.
func gitRemoteRev(c *exec.Context, target, remote, rev string) (string, error) {
	if looksLikeCommit(rev) {
		return rev, nil
	}
	pattern := rev
	if pattern == "" {
		pattern = "HEAD"
	}
	res, err := gitRun(c, target, "ls-remote", remote, pattern)
	if err != nil {
		return "", err
	}
	var heads, tags, other string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || sha == "" {
			continue
		}
		switch {
		case ref == "refs/heads/"+rev, ref == "HEAD":
			heads = sha
		case ref == "refs/tags/"+rev:
			tags = sha
		default:
			if other == "" {
				other = sha
			}
		}
	}
	for _, sha := range []string{heads, tags, other} {
		if sha != "" {
			return sha, nil
		}
	}
	// The remote does not publish it. A local tag or commit still
	// resolves, and saying "unknown" for one would report a change that
	// is not there.
	local, err := gitRun(c, target, "rev-parse", rev)
	if err != nil {
		return "", fmt.Errorf("%s is not a ref on %s and does not resolve locally", rev, remote)
	}
	return strings.TrimSpace(local.Stdout), nil
}

// looksLikeCommit reports whether a revision is a raw object name, which
// must not be prefixed with a remote.
func looksLikeCommit(rev string) bool {
	if len(rev) < 7 || len(rev) > 40 {
		return false
	}
	for _, r := range rev {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
