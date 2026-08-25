package gitfs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// run invokes git and returns its standard output.
//
// The environment is built rather than inherited, for the reason SPEC
// 13.3 gives about credentials: a git that inherits the hub's
// environment inherits whatever is in it, and a git that can prompt
// will hang a fetch forever waiting for a password nobody is there to
// type.
func (o *Options) run(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, o.git(), args...)
	cmd.Dir = dir
	cmd.Env = o.environment()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return stdout.String(), fmt.Errorf("git %s: %s", args[0], message)
	}
	return stdout.String(), nil
}

// environment is what git runs with.
func (o *Options) environment() []string {
	env := []string{
		// Never prompt. A fetch that asks for a password on a hub with
		// no terminal waits for ever, and the symptom is a file server
		// that stopped updating for no visible reason.
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		// Deterministic messages, so a parse of git's output does not
		// depend on the hub's locale.
		"LC_ALL=C",
		"LANG=C",
		// A repository the hub does not own is still one it may read:
		// the mirror is created by this process, and git's ownership
		// check would otherwise refuse it after a uid change.
		"GIT_CONFIG_NOSYSTEM=1",
	}
	for _, keep := range []string{"PATH", "HOME", "SSH_AUTH_SOCK", "GNUPGHOME"} {
		if value, ok := os.LookupEnv(keep); ok {
			env = append(env, keep+"="+value)
		}
	}
	// Anything the operator added — a credential helper, a token — goes
	// last so it can override the above.
	return append(env, o.Env...)
}

// mirror makes sure a remote's bare mirror exists and is current.
func (o *Options) mirror(ctx context.Context, remote Remote) (string, error) {
	if err := checkURL(remote); err != nil {
		return "", err
	}
	dir := filepath.Join(o.CacheDir, remote.mirrorDir())
	if err := os.MkdirAll(o.CacheDir, 0o700); err != nil {
		return "", err
	}

	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		// A bare mirror, as SPEC 13.3 says. `--mirror` maps every
		// remote ref into the local namespace, which is what makes
		// `for-each-ref` below see branches and tags without a second
		// configuration step.
		if _, err := o.run(ctx, o.CacheDir, "clone", "--mirror", "--quiet", remote.URL, dir); err != nil {
			// A failed clone leaves a directory behind that the next
			// run would mistake for a mirror.
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("mirroring %s: %w", remote.label(), err)
		}
		return dir, nil
	}

	// `--prune` so a branch deleted upstream stops being an
	// environment here. Without it a deleted branch is served for ever,
	// which is how an estate keeps applying a tree nobody maintains.
	if _, err := o.run(ctx, dir, "fetch", "--prune", "--quiet", "--tags", "origin"); err != nil {
		return "", fmt.Errorf("fetching %s: %w", remote.label(), err)
	}
	return dir, nil
}

// refs lists the branches and tags a remote exposes, mapped to
// environments.
func (o *Options) refs(ctx context.Context, dir string, remote Remote) ([]Ref, error) {
	types := remote.RefTypes
	if len(types) == 0 {
		types = o.refTypes()
	}
	var patterns []string
	for _, t := range types {
		switch t {
		case "branch", "branches":
			patterns = append(patterns, "refs/heads/")
		case "tag", "tags":
			patterns = append(patterns, "refs/tags/")
		default:
			return nil, fmt.Errorf("%q is not a ref type; use `branches` or `tags`", t)
		}
	}
	if len(patterns) == 0 {
		return nil, nil
	}

	args := append([]string{"for-each-ref", "--format=%(refname) %(objectname)"}, patterns...)
	out, err := o.run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}

	base := remote.Base
	if base == "" {
		base = o.base()
	}
	var refs []Ref
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		full, commit := fields[0], fields[1]
		name, isTag := shortRef(full)
		if name == "" {
			continue
		}
		env := envFor(name, base)
		if env == "" {
			o.log("warn", "a git ref maps to no usable environment name",
				"remote", remote.label(), "ref", name)
			continue
		}
		if !o.permitted(env) {
			continue
		}
		refs = append(refs, Ref{Name: name, Env: env, Commit: commit, Tag: isTag})
	}
	return refs, nil
}

func shortRef(full string) (string, bool) {
	if name, ok := strings.CutPrefix(full, "refs/heads/"); ok {
		return name, false
	}
	if name, ok := strings.CutPrefix(full, "refs/tags/"); ok {
		return name, true
	}
	return "", false
}

// verify checks a ref's signature, as SPEC 13.3's
// `gitfs_verify_signatures` requires.
//
// A ref that fails is not served. That is the whole control: a tree
// whose signature is checked and served anyway is a tree that is served,
// and the check is decoration.
func (o *Options) verify(ctx context.Context, dir string, remote Remote, ref Ref) error {
	if !o.VerifySignatures {
		return nil
	}
	if o.Keyring == "" {
		// Verifying against the hub user's own keyring is not a
		// configuration decision anybody made, and it would pass for
		// whatever that user happens to trust.
		return fmt.Errorf("gitfs_verify_signatures is on and no keyring is configured; " +
			"set gitfs_keyring to the GnuPG home holding the keys this estate trusts")
	}
	command := "verify-commit"
	if ref.Tag {
		command = "verify-tag"
	}
	opts := *o
	opts.Env = append(append([]string(nil), o.Env...), "GNUPGHOME="+o.Keyring)
	if _, err := opts.run(ctx, dir, command, "--raw", ref.Name); err != nil {
		return fmt.Errorf("%s %s is not signed by a key this estate trusts: %w",
			remote.label(), ref.Name, err)
	}
	return nil
}

// materialise writes a ref's tree into a directory the file server can
// read.
//
// `git archive` piped into a fresh directory rather than a worktree: a
// worktree leaves git metadata in the served tree, and a `.git` file
// inside a directory the file server publishes is a file the estate
// did not mean to publish.
func (o *Options) materialise(ctx context.Context, mirror, target string, remote Remote, ref Ref) error {
	// Under a commit-named directory, so a ref that has not moved needs
	// no work and a ref that has cannot be half-replaced: the new tree
	// is built beside the old one and the pointer moves at the end.
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	staging := target + ".building"
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	spec := ref.Commit
	if root := strings.Trim(remote.Root, "/"); root != "" {
		// A repository that holds the tree under a subdirectory.
		spec += ":" + root
	}
	archive, err := o.runBinary(ctx, mirror, "archive", "--format=tar", spec)
	if err != nil {
		return fmt.Errorf("reading %s at %s: %w", remote.label(), ref.Name, err)
	}
	if err := extractTar(archive, staging); err != nil {
		return fmt.Errorf("unpacking %s at %s: %w", remote.label(), ref.Name, err)
	}
	if err := os.Rename(staging, target); err != nil {
		return err
	}
	return nil
}

// runBinary is `run` for output that is not text.
func (o *Options) runBinary(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, o.git(), args...)
	cmd.Dir = dir
	cmd.Env = o.environment()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], message)
	}
	return stdout.Bytes(), nil
}
