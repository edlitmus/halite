// Package sshexec runs a job on a machine that has no agent.
//
// SPEC 21.1, and the design's advantage over `salt-ssh` is the thing it
// pushes: a single static binary, rather than a Python "thin" tarball
// that then has to find or bootstrap a compatible Python on the target.
// Most `salt-ssh` failures are that search going wrong; a static binary
// has no such problem.
//
// The connection is the system `ssh` binary rather than a linked SSH
// library, which is what makes an estate's `ssh_config`, `ProxyJump`,
// `ProxyCommand`, certificate authentication, agent policy, and
// `known_hosts` handling work without any of it being reimplemented
// here — and what removes `paramiko`, the largest dependency in
// `salt-ssh` and the source of its most persistent bugs.
package sshexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/roster"
)

// Options configure a run.
type Options struct {
	// Binary is the static `halite-node` to push, for the target's
	// platform.
	Binary string
	// BinaryFor answers with a binary for a target's platform, when an
	// estate serves more than one. Nil takes Binary for everything.
	BinaryFor func(target roster.Target) (string, error)
	// SSH is the ssh binary. Empty takes `ssh` from the path.
	SSH string
	// SCP is the copier. Empty takes `scp`.
	SCP string
	// Options are extra `-o` settings, applied before the roster's.
	Options []string
	// Timeout bounds one whole run against one target.
	Timeout time.Duration
	// Clean removes the cached binary before running, and after.
	Clean bool
	// Log receives what the run wants an operator to know.
	Log func(level, msg string, kv ...any)
	// Env is the environment ssh runs with. Nil inherits, which is
	// what makes `SSH_AUTH_SOCK` and the operator's agent work.
	Env []string
}

func (o *Options) log(level, msg string, kv ...any) {
	if o.Log != nil {
		o.Log(level, msg, kv...)
	}
}

func (o *Options) ssh() string {
	if o.SSH == "" {
		return "ssh"
	}
	return o.SSH
}

func (o *Options) scp() string {
	if o.SCP == "" {
		return "scp"
	}
	return o.SCP
}

func (o *Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 5 * time.Minute
	}
	return o.Timeout
}

// sshArgs builds the connection arguments for a target.
//
// Deliberately few. Everything an estate has configured about how to
// reach a machine is already in its ssh config, and re-specifying it
// here would mean two places to get it wrong. What is set is what the
// roster said and what a non-interactive run requires.
func (o *Options) sshArgs(t roster.Target) []string {
	args := []string{
		// Never prompt. A run against fifty machines that stops on the
		// first one asking for a password is worse than one that fails
		// and says which.
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(int(t.Timeout.Seconds())),
	}
	for _, opt := range o.Options {
		args = append(args, "-o", opt)
	}
	if t.Port != 0 && t.Port != 22 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	if t.Priv != "" {
		args = append(args, "-i", t.Priv)
	}
	if t.IdentitiesOnly {
		args = append(args, "-o", "IdentitiesOnly=yes")
	}
	if t.ProxyJump != "" {
		args = append(args, "-J", t.ProxyJump)
	}
	if t.TTY {
		args = append(args, "-tt")
	}
	return args
}

// destination is `user@host` or `host`.
func destination(t roster.Target) string {
	if t.User != "" {
		return t.User + "@" + t.Host
	}
	return t.Host
}

// Result is what one target answered.
type Result struct {
	Target roster.Target
	// Return is the framed JSON the pushed binary wrote.
	Return []byte
	// Err is why the run failed before or instead of a return.
	Err error
	// Pushed records that the binary was transferred rather than found
	// in the cache, which is what an operator wants to see when a run
	// is slower than expected.
	Pushed bool
	// Stderr is what the target said, for a diagnosis.
	Stderr string
}

// Run pushes the binary if the target does not have it and executes one
// job.
func (o *Options) Run(ctx context.Context, t roster.Target, request []byte) Result {
	result := Result{Target: t}

	binary, err := o.binaryFor(t)
	if err != nil {
		result.Err = err
		return result
	}
	digest, err := digestOf(binary)
	if err != nil {
		result.Err = err
		return result
	}

	// SPEC 21.1's cache: `<thin_dir>/<version>-<digest>`, so a
	// different build is a different path and an upgrade never runs the
	// old binary.
	remote := t.ThinDir + "/" + digest
	if o.Clean {
		// Before, not only after: a run with --clean is what an
		// operator reaches for when they suspect the cached copy.
		if _, _, err := o.script(ctx, t, "rm -rf "+shellQuote(t.ThinDir)); err != nil {
			o.log("warn", "the cached binary could not be removed",
				"target", t.ID, "error", err.Error())
		}
	}

	present, err := o.hasBinary(ctx, t, remote)
	if err != nil {
		result.Err = err
		return result
	}
	if !present {
		if err := o.push(ctx, t, binary, remote); err != nil {
			result.Err = err
			return result
		}
		result.Pushed = true
	}

	command, err := o.invocation(t, remote)
	if err != nil {
		result.Err = err
		return result
	}
	stdout, stderr, err := o.exec(ctx, t, command, request)
	result.Stderr = stderr
	if err != nil {
		// The exit status is not the whole story: the pushed binary
		// exits non-zero for a failed job, which is a return rather
		// than a transport failure. The frame decides.
		if framed, ok := Unframe(stdout); ok {
			result.Return = []byte(framed)
			return result
		}
		result.Err = fmt.Errorf("%s: %w", t.ID, enrich(err, stderr))
		return result
	}
	framed, ok := Unframe(stdout)
	if !ok {
		result.Err = fmt.Errorf("%s answered without a framed return; "+
			"the binary may not have run. Its output was: %s", t.ID, summarise(stdout))
		return result
	}
	result.Return = []byte(framed)

	if o.Clean {
		if _, _, err := o.script(ctx, t, "rm -rf "+shellQuote(t.ThinDir)); err != nil {
			o.log("warn", "the cached binary could not be removed after the run",
				"target", t.ID, "error", err.Error())
		}
	}
	return result
}

func (o *Options) binaryFor(t roster.Target) (string, error) {
	if o.BinaryFor != nil {
		return o.BinaryFor(t)
	}
	if o.Binary == "" {
		return "", fmt.Errorf("agentless mode needs a halite-node binary to push")
	}
	return o.Binary, nil
}

// hasBinary reports whether the target already has this build cached.
func (o *Options) hasBinary(ctx context.Context, t roster.Target, remote string) (bool, error) {
	// `test -x` rather than `test -f`: a transfer interrupted part-way
	// leaves a file that is not executable, and running it would fail
	// in a way that blames the job.
	stdout, stderr, err := o.script(ctx, t,
		fmt.Sprintf("test -x %s && echo present || echo absent", shellQuote(remote)))
	if err != nil {
		return false, fmt.Errorf("%s: %w", t.ID, enrich(err, stderr))
	}
	return strings.Contains(stdout, "present"), nil
}

// push copies the binary and verifies its digest on the target.
//
// Verified after transfer, as SPEC 21.1 requires: a truncated copy is a
// binary that runs and does something unpredictable, and scp reports
// success for a transfer the network cut short more often than anybody
// would like.
func (o *Options) push(ctx context.Context, t roster.Target, binary, remote string) error {
	dir := t.ThinDir
	if _, stderr, err := o.script(ctx, t,
		"mkdir -p "+shellQuote(dir)+" && chmod 700 "+shellQuote(dir)); err != nil {
		return fmt.Errorf("%s: preparing %s: %w", t.ID, dir, enrich(err, stderr))
	}

	staging := remote + ".partial"
	args := append([]string{"-q", "-B"}, o.scpArgs(t)...)
	args = append(args, binary, destination(t)+":"+staging)

	cmd := exec.CommandContext(ctx, o.scp(), args...)
	cmd.Env = o.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: copying the binary: %w", t.ID, enrich(err, stderr.String()))
	}

	// The digest the target computes, compared with the one here.
	want, err := digestOf(binary)
	if err != nil {
		return err
	}
	got, err := o.remoteDigest(ctx, t, staging)
	if err != nil {
		return err
	}
	if got != want {
		_, _, _ = o.script(ctx, t, "rm -f "+shellQuote(staging))
		return fmt.Errorf("%s: the binary arrived with digest %s and should have %s; "+
			"the transfer was corrupted", t.ID, got, want)
	}

	// Only now does it become the cached name, so a concurrent run
	// never finds a half-written binary.
	move := fmt.Sprintf("chmod 700 %s && mv %s %s",
		shellQuote(staging), shellQuote(staging), shellQuote(remote))
	if _, stderr, err := o.script(ctx, t, move); err != nil {
		return fmt.Errorf("%s: installing the binary: %w", t.ID, enrich(err, stderr))
	}
	return nil
}

// remoteDigest asks the target for the binary's SHA-256.
//
// Through whichever of the usual tools is present. A target with none
// of them is a target this cannot verify on, and that is a refusal
// rather than a shrug: an unverified binary is the thing SPEC 21.1
// asks to be checked.
func (o *Options) remoteDigest(ctx context.Context, t roster.Target, path string) (string, error) {
	quoted := shellQuote(path)
	command := fmt.Sprintf(
		"if command -v sha256sum >/dev/null 2>&1; then sha256sum %s | cut -d' ' -f1; "+
			"elif command -v sha256 >/dev/null 2>&1; then sha256 -q %s; "+
			"elif command -v shasum >/dev/null 2>&1; then shasum -a 256 %s | cut -d' ' -f1; "+
			"else echo NO-DIGEST-TOOL; fi", quoted, quoted, quoted)

	stdout, stderr, err := o.script(ctx, t, command)
	if err != nil {
		return "", fmt.Errorf("%s: verifying the binary: %w", t.ID, enrich(err, stderr))
	}
	digest := strings.TrimSpace(lastLine(stdout))
	if digest == "NO-DIGEST-TOOL" {
		return "", fmt.Errorf("%s has none of sha256sum, sha256, or shasum, so the pushed "+
			"binary cannot be verified. SPEC 21.1 requires that it is", t.ID)
	}
	if len(digest) != 64 {
		return "", fmt.Errorf("%s answered %q, which is not a SHA-256", t.ID, summarise(stdout))
	}
	return digest, nil
}

// invocation is the command line that runs the job on the target.
//
// This is the one command that cannot go over stdin, because stdin
// carries the job. So the values that reach it are *validated* rather
// than escaped: a quote inside them would have to survive the target's
// login shell, and the POSIX escape for one does not survive every
// shell. A path with a quote in it is refused, with the reason.
func (o *Options) invocation(t roster.Target, remote string) (string, error) {
	for name, v := range map[string]string{
		"thin_dir":  t.ThinDir,
		"sudo_user": t.SudoUser,
		"set_path":  t.SetPath,
	} {
		if err := shellSafe(name, v); err != nil {
			return "", fmt.Errorf("%s: %w", t.ID, err)
		}
	}

	command := remote + " oneshot"
	if t.SetPath != "" {
		command = "PATH=" + t.SetPath + ":$PATH " + command
	}
	if t.Sudo {
		// `-n` so sudo never prompts: a run that hangs waiting for a
		// password nobody will type is worse than one that fails and
		// says sudo refused.
		sudo := "sudo -n"
		if t.SudoUser != "" {
			sudo += " -u " + t.SudoUser
		}
		command = sudo + " " + command
	}
	// Wrapped so the script is POSIX sh whatever the login shell is.
	// The single quotes are safe because nothing inside them can hold
	// one.
	return "/bin/sh -c '" + command + "'", nil
}

// shellSafe refuses a value that would need quoting a login shell may
// not understand.
//
// The permitted set is what a path and a user name actually contain.
// An operator whose `thin_dir` has a space in it gets a message saying
// so, which is a better outcome than a command that means something
// different on a target running zsh.
func shellSafe(name, v string) error {
	if v == "" {
		return nil
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/', r == '.', r == '-', r == '_', r == ':', r == '+', r == '=':
		default:
			return fmt.Errorf("%s is %q, and %q is not a character this can pass to a "+
				"target's shell safely; use a path of letters, digits, and /._-", name, v, string(r))
		}
	}
	return nil
}

// script runs a POSIX shell script on the target, sent on stdin.
//
// `sh -s` rather than `sh -c '<script>'`, and this is not a stylistic
// choice: ssh hands its command to the target's *login* shell, and a
// login shell is not always POSIX. On a target whose shell is `fish`,
// a script containing `if ... then ... fi` is a syntax error before
// `/bin/sh` is ever reached, and the POSIX `'\”` idiom for an
// embedded quote does not survive it either. A script on stdin needs no
// quoting at all and so has nothing to survive. Found against a real
// target whose login shell is fish.
func (o *Options) script(ctx context.Context, t roster.Target, body string) (string, string, error) {
	return o.exec(ctx, t, "/bin/sh -s", []byte(body))
}

// exec runs one command, which the target's login shell parses.
//
// Used only where stdin is needed for something else — the job itself.
// Whatever is interpolated into `command` is checked by `shellSafe`
// first, so the single quotes around it are the only quoting involved
// and they mean the same thing in every shell.
func (o *Options) exec(ctx context.Context, t roster.Target, command string, stdin []byte) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout())
	defer cancel()

	args := append(o.sshArgs(t), destination(t), command)
	cmd := exec.CommandContext(ctx, o.ssh(), args...)
	cmd.Env = o.Env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (o *Options) scpArgs(t roster.Target) []string {
	var args []string
	args = append(args, "-o", "BatchMode=yes")
	for _, opt := range o.Options {
		args = append(args, "-o", opt)
	}
	if t.Port != 0 && t.Port != 22 {
		// scp spells the port `-P`, not `-p`, which is a mistake worth
		// making only once.
		args = append(args, "-P", strconv.Itoa(t.Port))
	}
	if t.Priv != "" {
		args = append(args, "-i", t.Priv)
	}
	if t.IdentitiesOnly {
		args = append(args, "-o", "IdentitiesOnly=yes")
	}
	if t.ProxyJump != "" {
		args = append(args, "-J", t.ProxyJump)
	}
	return args
}

func digestOf(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading the binary to push: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// shellQuote makes a value safe inside the single command string ssh
// hands to the target's shell.
//
// Single quotes with the escape POSIX shells accept, because a path
// from a roster reaches this and a roster is a file somebody edits.
func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

// enrich puts the target's own words on an exit status.
//
// `exit status 255` is ssh's way of saying anything at all went wrong,
// and on its own it sends an operator nowhere.
func enrich(err error, stderr string) error {
	message := strings.TrimSpace(stderr)
	if message == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, summarise(message))
}

func summarise(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 400 {
		return v[:400] + "…"
	}
	return v
}

func lastLine(v string) string {
	lines := strings.Split(strings.TrimSpace(v), "\n")
	return lines[len(lines)-1]
}
