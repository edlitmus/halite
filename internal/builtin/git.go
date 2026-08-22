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

	// A fetch is a network call that changes nothing in the working tree,
	// so test mode does it too: without it the state cannot say whether
	// it would change anything, and guessing is worse.
	fetch := []string{"fetch", "--quiet"}
	if states.Bool(args, "force_fetch", false) {
		fetch = append(fetch, "--force")
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
