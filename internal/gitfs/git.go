package gitfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/fileperm"
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
	if err := fileperm.MkdirAll(o.CacheDir, 0o700); err != nil {
		return "", err
	}

	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		// A bare mirror, as SPEC 13.3 says. `--mirror` maps every
		// remote ref into the local namespace, which is what makes
		// `for-each-ref` below see branches and tags without a second
		// configuration step.
		//
		// `--` before the URL so that nothing in it can be read as an
		// option. checkURL refuses most of what could start with a dash,
		// and the URL is the operator's rather than the remote's, but
		// the argv should not depend on either being true.
		if _, err := o.run(ctx, o.CacheDir, "clone", "--mirror", "--quiet", "--", remote.URL, dir); err != nil {
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
		if !isObjectID(commit) {
			// for-each-ref printed something that is not a full object
			// id. Both `verify` and `materialise` pass it to git as an
			// argument and name a directory after it, so it must be
			// nothing else.
			o.log("warn", "a git ref has no usable object id",
				"remote", remote.label(), "ref", full)
			continue
		}
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

// isObjectID reports whether s is a full object id: forty hex digits
// for SHA-1, sixty-four for SHA-256, lower case as git prints them.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
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
	// The object id, never the name. What is served is ref.Commit — it
	// names the tree directory and it is what `materialise` archives —
	// so it is the only thing whose signature means anything. The name
	// is git's to resolve, and git resolves an ambiguous short name to
	// the tag before the branch: a mirror holds every tag the remote
	// has (`--mirror`, `fetch --tags`) whatever gitfs_ref_types says,
	// so anybody who can push a tag called `production` at an old
	// signed commit could have that commit verified while an unsigned
	// tip on the branch `production` was served (DIVERGENCE 5.292).
	// A full object id has no such reading.
	//
	// And `--` before it, because the argument came from the remote.
	// An object id cannot begin with a dash, but the guard costs
	// nothing and does not depend on that remaining what is passed.
	if _, err := opts.run(ctx, dir, command, "--raw", "--", ref.Commit); err != nil {
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
	if err := fileperm.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	spec := ref.Commit
	if root := strings.Trim(remote.Root, "/"); root != "" {
		// A repository that holds the tree under a subdirectory.
		spec += ":" + root
	}
	// Streamed, not buffered. The archive was read whole into memory and
	// only then checked against MaxArchiveBytes, so anyone who could push
	// a branch -- every branch is an environment -- could push a tree of
	// several gigabytes and exhaust the hub's memory on every update
	// (issue 321, M-53). The tar reader now takes git's output as it
	// comes, the limits stop it as soon as they are passed, and git is
	// killed rather than left writing into a pipe nobody reads.
	if err := o.runStream(ctx, mirror, func(r io.Reader) error {
		if err := extractTar(r, staging); err != nil {
			return fmt.Errorf("unpacking %s at %s: %w", remote.label(), ref.Name, err)
		}
		return nil
	}, "archive", "--format=tar", spec); err != nil {
		if strings.HasPrefix(err.Error(), "unpacking ") {
			return err
		}
		return fmt.Errorf("reading %s at %s: %w", remote.label(), ref.Name, err)
	}
	if err := atomicfile.Rename(staging, target); err != nil {
		return err
	}
	return nil
}

// trailerLimit is how much runStream reads after a consumer that finished
// without error. `git archive` pads its tar to a whole 10 KiB record after
// the end marker, where the tar reader stops; this is room for that and
// more, and is still a bound on a git that writes without end.
const trailerLimit = 1 << 20

// runStream runs git and hands its output, as it comes, to consume.
func (o *Options) runStream(ctx context.Context, dir string, consume func(io.Reader) error, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, o.git(), args...)
	cmd.Dir = dir
	cmd.Env = o.environment()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	consumeErr := consume(stdout)
	if consumeErr == nil {
		// A consumer that finished may not have read everything: the tar
		// reader stops at the end marker, and git still has the padding
		// after it to write. Waiting for git without reading it can
		// deadlock: on beastie (FreeBSD 15.1) a one-file tree was a single
		// 10 KiB record of which 7 KiB followed the marker, git did not
		// exit with that unread, and every gitfs update hung until its
		// timeout. The same on Linux finished, so the pipe there took it.
		// Which pipes do is not this function's to depend on: what is left
		// is read, up to a bound, and past it git is killed and the run
		// fails.
		n, _ := io.Copy(io.Discard, io.LimitReader(stdout, trailerLimit+1))
		if n > trailerLimit {
			consumeErr = fmt.Errorf("git %s wrote more than %d bytes past what it was asked for", args[0], trailerLimit)
		}
	}
	if consumeErr != nil {
		// The consumer stopped early, and git may still be writing: kill
		// it rather than wait on a pipe nobody will read.
		cancel()
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()
	if consumeErr != nil {
		return consumeErr
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return fmt.Errorf("git %s: %s", args[0], message)
	}
	return nil
}
