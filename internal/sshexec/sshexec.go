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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
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
	//
	// The directory is checked here, before anything reaches the
	// target, rather than only in `invocation`: the scripts below put it
	// inside double quotes, and the trust check it is subject to walks
	// it component by component, which means nothing for a relative
	// path whose meaning depends on where the login shell starts.
	dir := path.Clean(t.ThinDir)
	if !path.IsAbs(dir) {
		result.Err = fmt.Errorf("%s: thin_dir is %q, which is not an absolute path; "+
			"a cached binary that runs as root has to be somewhere whose every "+
			"directory can be checked", t.ID, t.ThinDir)
		return result
	}
	if err := shellSafe("thin_dir", dir); err != nil {
		result.Err = fmt.Errorf("%s: %w", t.ID, err)
		return result
	}
	remote := dir + "/" + digest
	if o.Clean {
		// Before, not only after: a run with --clean is what an
		// operator reaches for when they suspect the cached copy.
		if _, _, err := o.script(ctx, t, "rm -rf "+shellQuote(dir)); err != nil {
			o.log("warn", "the cached binary could not be removed",
				"target", t.ID, "error", err.Error())
		}
	}

	present, err := o.cached(ctx, t, dir, remote, digest)
	if err != nil {
		result.Err = err
		return result
	}
	if !present {
		if err := o.push(ctx, t, binary, dir, remote); err != nil {
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
		if _, _, err := o.script(ctx, t, "rm -rf "+shellQuote(dir)); err != nil {
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

// Who may have put a cached binary where the next run will execute it.
//
// # The defect this answers
//
// The cache used to be trusted by its name. A run asked the target
// `test -x <thin_dir>/<digest>`, and on a yes it skipped the transfer and
// ran that file -- under `sudo` when the roster asked for it, so as root.
// The default `thin_dir` is under `/var/tmp`, which every local account
// may write to, and the digest is the SHA-256 of a published,
// reproducible build, so the name is known to anybody who can read the
// release page. Any account on a target could make `/var/tmp/halite-thin`
// before halite ever ran there, put an executable in it under the next
// release's digest, and have the next agentless run execute it as root.
// `mkdir -p` and `chmod 700`, which were the only preparation, both
// succeed for root on a directory somebody else made, and leave them its
// owner. The staging name `<digest>.partial` was predictable as well, so
// the transfer itself could be redirected by a link put there first.
// DIVERGENCE 5.306.
//
// # What is trusted now, and why this and not something else
//
// A cached binary is run only when nobody but root and the connecting
// account could have written it or anything on the way to it:
//
//   - the binary is a regular file, owned by root or the connecting
//     account, writable by neither its group nor others, with no access
//     control list;
//   - `thin_dir` is a directory with the same properties;
//   - every directory above it is owned by root or the connecting
//     account and is not writable by group or others unless it is
//     sticky, which is what makes `/var/tmp` itself acceptable: in a
//     sticky directory only an entry's owner may rename or remove it, so
//     another account cannot swap `halite-thin` for its own;
//   - all of that holds for the path as written *and* for the physical
//     path a `cd -P` reaches, so a symbolic link anywhere along it is
//     followed to where it really goes and that is checked too. (FreeBSD's
//     `/home` and macOS's `/var` are links, so links cannot simply be
//     refused.)
//
// The connecting account counts as trusted because it already is: with
// `sudo` in the roster it can become root, and without it the binary
// runs as that account. Nothing is trusted that was not already.
//
// Re-hashing the cached binary on its own would not have been enough.
// A hash taken in one ssh session and an `exec` in the next leave a gap
// in which a writable directory can have its file replaced, and the
// hash then speaks for a file that is no longer there. Ownership closes
// that gap by construction: when no other account can write anywhere on
// the path, there is nobody to win the race. The hash is still taken, in
// the same script as the check, and a mismatch is treated as a missing
// binary and replaced -- not as the security property, which it is not,
// but because a binary truncated by a full disk or edited by a careless
// root should not be run either, and it costs no extra round trip.
//
// A different default `thin_dir` was considered and not taken. Salt's
// own default is under `/var/tmp` too (`/var/tmp/.<user>_<id>_salt`, in
// `salt/client/ssh/__init__.py` as of 3006.28), and its shim's answer is
// the same one made here: refuse a directory the running user does not
// own or whose mode is not 0700. A root-owned default would need the
// copy itself to run under `sudo`, which `scp` cannot do; a default in
// the account's home would be a relative path, which `sudo` resolves
// against a working directory that its policy may change. What remains
// is that another account can *deny* a run by making the directory
// first. That is refused with a message naming the directory, its
// owner and the `thin_dir` setting -- which is the right failure: an
// operator who sees it has found somebody on that machine doing
// something worth knowing about.
//
// Portability. Everything below is POSIX sh and POSIX utilities. The
// owner and mode come from `ls -ldn`, whose first and third fields are
// the mode string and the numeric owner on GNU, BSD and busybox alike;
// `stat` would have been tidier and is spelled `-c` on Linux and `-f` on
// the BSDs. A trailing `+` on the mode string is how their `ls` pages say
// an access control list is marked, and `.` (SELinux) or `@` (macOS
// extended attributes) are not refused. `mktemp` is not POSIX, and is on
// Linux, FreeBSD and macOS. The tests run these scripts through the
// test machine's /bin/sh, and through dash, ksh and `bash --posix` when
// HALITE_TEST_TARGET_SH names them; zsh, which does not split words,
// fails closed.
const trustFunctions = `set -f
halite_refuse() {
	echo "halite: refusing $1: $2" >&2
	exit 3
}
halite_me=$(id -u) || halite_refuse "the cache" "id -u did not say which account this is"
halite_stat() {
	halite_line=$(ls -ldn "$1" 2>/dev/null) || return 1
	set -- $halite_line
	halite_mode=$1
	halite_uid=$3
}
halite_owner() {
	halite_stat "$1" || halite_refuse "$1" "it cannot be examined"
	[ "$halite_uid" = 0 ] || [ "$halite_uid" = "$halite_me" ] ||
		halite_refuse "$1" "it belongs to uid $halite_uid, and only root or the connecting account (uid $halite_me) may own it"
}
halite_private() {
	case $halite_mode in
	?????w*|????????w*) halite_refuse "$1" "accounts other than its owner may write to it ($halite_mode)" ;;
	??????????+*) halite_refuse "$1" "it has an access control list ($halite_mode), which this cannot read" ;;
	esac
}
halite_above() {
	halite_p=$1
	while [ "$halite_p" != / ]; do
		halite_p=$(dirname "$halite_p")
		halite_owner "$halite_p"
		case $halite_mode in
		l*) ;;
		?????????[tT]*) ;;
		*) halite_private "$halite_p" ;;
		esac
	done
}
halite_trust_dir() {
	case $1 in /*) ;; *) halite_refuse "$1" "it is not an absolute path" ;; esac
	halite_above "$1"
	halite_owner "$1"
	halite_phys=$(cd -P "$1" 2>/dev/null && pwd -P) ||
		halite_refuse "$1" "it is not a directory this account can enter"
	halite_above "$halite_phys"
	halite_owner "$halite_phys"
	case $halite_mode in d*) ;; *) halite_refuse "$1" "it is not a directory" ;; esac
	[ "$2" = owner ] || halite_private "$halite_phys"
}
`

// untrusted is the status the scripts exit with when they refuse.
const untrusted = 3

// digestCommand prints the SHA-256 of a file through whichever of the
// usual tools the target has: `sha256sum` on Linux, `sha256 -q` on
// FreeBSD, `shasum` on macOS. It prints NO-DIGEST-TOOL when none is.
func digestCommand(quoted string) string {
	return fmt.Sprintf(
		"if command -v sha256sum >/dev/null 2>&1; then sha256sum %s | cut -d' ' -f1; "+
			"elif command -v sha256 >/dev/null 2>&1; then sha256 -q %s; "+
			"elif command -v shasum >/dev/null 2>&1; then shasum -a 256 %s | cut -d' ' -f1; "+
			"else echo NO-DIGEST-TOOL; fi", quoted, quoted, quoted)
}

// checkScript is what the target runs to ask whether this build is
// cached and may be trusted. Its last line is `absent`, or `present`
// and the digest the target computed; it exits `untrusted` with the
// reason on stderr when something on the path could have been written
// by another account.
//
// A missing directory is `absent`, and `prepare` makes and checks it.
// A binary that is not executable is `absent` too: nothing but a
// trusted account can have left it there, and the staged copy's `mv`
// replaces it.
func checkScript(dir, remote string) string {
	return trustFunctions +
		"halite_dir=" + shellQuote(dir) + "\n" +
		"halite_bin=" + shellQuote(remote) + "\n" +
		`if [ ! -e "$halite_dir" ] && [ ! -h "$halite_dir" ]; then echo absent; exit 0; fi
halite_trust_dir "$halite_dir"
halite_stat "$halite_bin" || { echo absent; exit 0; }
case $halite_mode in -*) ;; *) halite_refuse "$halite_bin" "it is not a regular file ($halite_mode)" ;; esac
halite_owner "$halite_bin"
halite_private "$halite_bin"
case $halite_mode in ???x*) ;; *) echo absent; exit 0 ;; esac
echo present $(` + digestCommand(`"$halite_bin"`) + `)
`
}

// cached reports whether the target already has this build, in a
// place nobody else could have put it.
func (o *Options) cached(ctx context.Context, t roster.Target, dir, remote, digest string) (bool, error) {
	stdout, stderr, err := o.script(ctx, t, checkScript(dir, remote))
	if err != nil {
		if exitStatus(err) == untrusted {
			return false, refusal(t, dir, stderr)
		}
		return false, fmt.Errorf("%s: %w", t.ID, enrich(err, stderr))
	}
	fields := strings.Fields(lastLine(stdout))
	if len(fields) == 0 || fields[0] != "present" {
		return false, nil
	}
	if len(fields) != 2 || fields[1] != digest {
		// Not an attack -- only root or the connecting account could
		// have written it, which the script has just established -- but
		// not this build either, so it is replaced rather than run.
		o.log("warn", "the cached binary does not match its name and will be replaced",
			"target", t.ID, "path", remote, "answer", summarise(stdout))
		return false, nil
	}
	return true, nil
}

// refusal is what an operator is told when the cache cannot be trusted.
func refusal(t roster.Target, dir, stderr string) error {
	return fmt.Errorf("%s: %s cannot be trusted to hold a binary that this run executes: %s. "+
		"Only root or the connecting account may own it or any directory above it, and no "+
		"other account may be able to write to them. If another account made it, find out "+
		"why, then remove it as root, or set `thin_dir` in the roster to a directory that "+
		"meets this", t.ID, dir, summarise(stderr))
}

// push copies the binary and verifies its digest on the target.
//
// Verified after transfer, as SPEC 21.1 requires: a truncated copy is a
// binary that runs and does something unpredictable, and scp reports
// success for a transfer the network cut short more often than anybody
// would like.
func (o *Options) push(ctx context.Context, t roster.Target, binary, dir, remote string) error {
	staging, err := o.prepare(ctx, t, dir)
	if err != nil {
		return err
	}

	args := append([]string{"-q", "-B"}, o.scpArgs(t)...)
	args = append(args, binary, destination(t)+":"+staging)

	cmd := exec.CommandContext(ctx, o.scp(), args...)
	cmd.Env = o.Env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_, _, _ = o.script(ctx, t, "rm -f "+shellQuote(staging))
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
	// never finds a half-written binary. Two runs against the same
	// machine stage under two names, so neither renames the other's.
	move := fmt.Sprintf("chmod 700 %s && mv -f %s %s",
		shellQuote(staging), shellQuote(staging), shellQuote(remote))
	if _, stderr, err := o.script(ctx, t, move); err != nil {
		return fmt.Errorf("%s: installing the binary: %w", t.ID, enrich(err, stderr))
	}
	return nil
}

// prepareScript is what the target runs. Separated so that a test can
// run it through a real /bin/sh against a real directory: the shell is
// the thing being programmed here, and a script checked only against
// what its author meant is the mistake DIVERGENCE 5.31 is about.
//
// The directory is checked for its owner *before* the `chmod`, and in
// full after it. The order matters when the connecting account is
// root: `chmod` as root succeeds on a directory another account made,
// and on the target of a link another account planted, and it would be
// changing the mode of something that is not ours before the check
// that says so.
//
// Its last line names a staging file made by `mktemp`, which the copy
// is written to. The previous `<digest>.partial` was a name anybody
// could compute and make first.
func prepareScript(dir string) string {
	quoted := shellQuote(dir)
	return trustFunctions +
		"mkdir -p " + quoted + " || exit 1\n" +
		"halite_trust_dir " + quoted + " owner\n" +
		"chmod 700 " + quoted + " || exit 1\n" +
		"halite_trust_dir " + quoted + "\n" +
		probeScript(dir)
}

// probeScript is the half of prepareScript after the directory has been
// trusted: run something out of it, then make the staging file.
//
// Separate so that the tests which mount a real `noexec` filesystem can
// run it on its own. They do that inside an unprivileged user namespace,
// where every directory the host's root owns -- `/` among them -- shows
// as owned by the overflow uid, and the trust check refuses it, rightly:
// from in there it cannot tell who owns it. That refusal is tested
// elsewhere; what those tests are for is the probe.
func probeScript(dir string) string {
	// The probe is made by `mktemp` as well, for the same reason as the
	// staging file: two runs against one machine used to share the name
	// `.halite-probe`, and one removed it, or was writing it, while the
	// other ran it. CI found that on its first run of the concurrent
	// test, on Linux, FreeBSD and macOS alike: "chmod: cannot access
	// '.../.halite-probe': No such file or directory" and, on Linux,
	// "Text file busy" (DIVERGENCE 5.306).
	//
	// Written as one script over stdin, because a target's login shell
	// is not always POSIX and this is already how everything else here
	// reaches it. `rm` runs whatever happened, so a refused probe does
	// not leave a file behind.
	return "halite_probe=$(mktemp " + shellQuote(dir+"/.probe.XXXXXXXX") + ") || exit 2\n" +
		"printf '#!/bin/sh\\nexit 0\\n' > \"$halite_probe\" && chmod 700 \"$halite_probe\" || " +
		"{ rm -f \"$halite_probe\"; exit 2; }\n" +
		"\"$halite_probe\"; status=$?\n" +
		"rm -f \"$halite_probe\"\n" +
		"[ $status -eq 0 ] || exit $status\n" +
		"halite_staging=$(mktemp " + shellQuote(dir+"/.partial.XXXXXXXX") + ") || exit 2\n" +
		"echo staging $halite_staging\n"
}

// prepare makes the staging directory, proves that nobody else could
// have written to it and that the target can run something out of it,
// and answers with the name to copy the binary to.
//
// `mkdir -p` succeeding says the directory exists. It does not say this
// account can use it, and the dimension that decides an agentless run is
// execution: a host hardened to a benchmark commonly mounts `/var/tmp`
// `noexec`, which is where SPEC 21.1's cache lives by default.
//
// Every step up to the last one succeeds on such a host. The directory
// is made, the binary copies, its digest verifies, it is installed under
// its cached name -- and then it does not run, and what the operator is
// given is "Permission denied" about a file that was just installed
// successfully. So the probe runs here, once, where the answer can name
// the directory and the likely reason.
//
// It is the check `OpenNodeCache` already makes for itself on the hub,
// asked about execution rather than about writing.
func (o *Options) prepare(ctx context.Context, t roster.Target, dir string) (string, error) {
	return o.prepareWith(ctx, t, dir, prepareScript(dir))
}

// prepareWith runs a preparation script and reads its answer. Only the
// noexec tests pass anything but prepareScript; see probeScript.
func (o *Options) prepareWith(ctx context.Context, t roster.Target, dir, script string) (string, error) {
	stdout, stderr, err := o.script(ctx, t, script)
	if err == nil {
		staging := strings.TrimPrefix(strings.TrimSpace(lastLine(stdout)), "staging ")
		// What came back is about to be put on an scp command line and
		// into scripts, so it is held to the same rule as thin_dir, and
		// to being where it was asked to be.
		if !strings.HasPrefix(staging, dir+"/.partial.") || shellSafe("staging", staging) != nil {
			return "", fmt.Errorf("%s: preparing %s answered %q, which is not a staging file in it",
				t.ID, dir, summarise(stdout))
		}
		return staging, nil
	}
	switch exitStatus(err) {
	case 1:
		return "", fmt.Errorf("%s: %s could not be created: %w", t.ID, dir, enrich(err, stderr))
	case 2:
		return "", fmt.Errorf("%s: nothing could be written to %s: %w", t.ID, dir, enrich(err, stderr))
	case untrusted:
		return "", refusal(t, dir, stderr)
	case noStatus:
		// The script never ran, so nothing has been learned about the
		// directory. ssh could not be started, or was killed, or the
		// context expired -- and calling any of that a `noexec` mount
		// would be a confident answer to a question nobody asked. CI's
		// Windows leg found this by failing to find `ssh` at all and
		// being told its /var/tmp was mounted wrong.
		return "", fmt.Errorf("%s: preparing %s: %w", t.ID, dir, enrich(err, stderr))
	}
	// Anything else is the probe itself refusing to run, which is what
	// a `noexec` mount looks like from here: 126 from a shell, or 1
	// from an `execve` that never happened.
	return "", fmt.Errorf("%s: %s holds the pushed binary and will not run it. "+
		"A filesystem mounted `noexec` is the usual reason, and hardening benchmarks "+
		"ask for exactly that on /var/tmp and /tmp. Set `thin_dir` in the roster to a "+
		"directory this account may execute from: %w", t.ID, dir, enrich(err, stderr))
}

// noStatus is what exitStatus reports for a command that never ran.
const noStatus = -1

// exitStatus reports a command's exit status, or noStatus when it did
// not run far enough to have one.
func exitStatus(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return noStatus
}

// remoteDigest asks the target for the binary's SHA-256.
//
// Through whichever of the usual tools is present. A target with none
// of them is a target this cannot verify on, and that is a refusal
// rather than a shrug: an unverified binary is the thing SPEC 21.1
// asks to be checked.
func (o *Options) remoteDigest(ctx context.Context, t roster.Target, path string) (string, error) {
	quoted := shellQuote(path)
	command := digestCommand(quoted)

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
