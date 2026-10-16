//go:build unix

package sshexec

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/roster"
)

// Whether a cached binary can have been planted by another account.
// DIVERGENCE 5.306.
//
// The cache was trusted by its name: `test -x <thin_dir>/<digest>`, and
// a yes ran the file, as root under `sudo`. The default `thin_dir` is
// under the world-writable `/var/tmp` and the digest is published, so
// any account on a target could put an executable where the next run
// would execute it.
//
// These tests drive a whole `Run` with ssh and scp replaced by stand-ins
// that run the real scripts through a real `/bin/sh` on this machine, so
// what is checked is what a target would execute, not what its author
// meant (DIVERGENCE 5.31).
//
// What cannot be done here is the case that matters most: a directory
// another account really owns, read by a run as root. These tests do not
// run as root, so a directory cannot be given away. Two stand-ins are
// used instead, and they cover different halves:
//
//   - a group- or world-writable directory or binary, which is what
//     another account could have written, made with mode bits that any
//     account can set; and
//   - an `id` on the path that answers a uid other than the caller's,
//     so that every file the test made reads, to the script, as owned by
//     somebody else. That exercises the owner comparison against real
//     `ls -ldn` output. It does not exercise a real foreign owner.
//
// Root-owned files really are root-owned, so that half of the rule --
// root is trusted -- is checked against a real one.

// harness is a target that is this machine.
type harness struct {
	t      *testing.T
	o      *Options
	target roster.Target
	binary string
	digest string
	marker string
}

func newHarness(t *testing.T, thinDir string) *harness {
	t.Helper()
	bin := privateTempDir(t)

	ssh := filepath.Join(bin, "ssh")
	// The last argument is the remote command: `/bin/sh -s` with a
	// script on stdin, or the job's invocation with the job on stdin.
	// HALITE_TEST_TARGET_SH runs the scripts under another shell --
	// dash, ksh, a `bash --posix` -- which is the nearest this gets to a
	// target whose /bin/sh is not this machine's.
	shell := os.Getenv("HALITE_TEST_TARGET_SH")
	if shell == "" {
		shell = "/bin/sh"
	}
	writeExec(t, ssh, "#!/bin/sh\nfor a; do last=$a; done\n"+
		"if [ \"$last\" = '/bin/sh -s' ]; then exec "+shell+" -s; fi\n"+
		"exec "+shell+" -c \"$last\"\n")
	scp := filepath.Join(bin, "scp")
	writeExec(t, scp, "#!/bin/sh\nfor a; do src=$dst; dst=$a; done\nexec cp \"$src\" \"${dst#*:}\"\n")

	binary := filepath.Join(bin, "halite-node")
	writeExec(t, binary, "#!/bin/sh\ncat >/dev/null\necho '"+begin+"'\necho '{\"from\":\"pushed\"}'\necho '"+end+"'\n")
	digest, err := digestOf(binary)
	if err != nil {
		t.Fatal(err)
	}

	return &harness{
		t:      t,
		o:      &Options{Binary: binary, SSH: ssh, SCP: scp},
		target: roster.Target{ID: "web1.example", Host: "web1.example", ThinDir: thinDir},
		binary: binary,
		digest: digest,
		marker: filepath.Join(bin, "planted-binary-ran"),
	}
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

// plant puts what an attacker would put at the cached name: an
// executable that leaves a mark when it runs.
func (h *harness) plant(dir string, mode os.FileMode) string {
	h.t.Helper()
	path := filepath.Join(dir, h.digest)
	writeExec(h.t, path, "#!/bin/sh\ncat >/dev/null\ntouch '"+h.marker+"'\n"+
		"echo '"+begin+"'\necho '{\"from\":\"planted\"}'\necho '"+end+"'\n")
	if err := os.Chmod(path, mode); err != nil {
		h.t.Fatal(err)
	}
	return path
}

func (h *harness) run() Result {
	h.t.Helper()
	return h.o.Run(context.Background(), h.target, []byte(`{}`))
}

func (h *harness) plantedRan() bool {
	_, err := os.Stat(h.marker)
	return err == nil
}

// privateTempDir is t.TempDir with its mode set, rather than left to
// the umask.
//
// t.TempDir makes a 0700 directory per test and, inside it, one per
// call with mode 0777 filtered by the umask. Under the common 022 that
// is drwxr-xr-x. Under 002, the default for a user with a private group
// on Ubuntu and others, it is drwxrwxr-x. The trust check refuses a thin
// directory below anything another account may write to, and the group
// counts. So every test here that put a thin directory under
// privateTempDir(t) failed on such a machine, refusing the test's own
// directory. The check was right, because the test had asked for a
// directory it did not inspect. CI's runners use 022, so nothing showed
// it until a developer ran the package on a 002 host.
func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mkdir makes a directory with exactly this mode; the umask would
// otherwise take the write bits a test is trying to give it.
func mkdir(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnAgentlessRunPushesThenUsesItsCache(t *testing.T) {
	dir := filepath.Join(privateTempDir(t), "halite-thin")
	h := newHarness(t, dir)

	first := h.run()
	if first.Err != nil {
		t.Fatalf("the first run failed: %v", first.Err)
	}
	if !first.Pushed || string(first.Return) != `{"from":"pushed"}` {
		t.Fatalf("the first run pushed=%v and returned %q", first.Pushed, first.Return)
	}
	second := h.run()
	if second.Err != nil || second.Pushed {
		t.Fatalf("the second run pushed=%v, err=%v; want the cache used", second.Pushed, second.Err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != h.digest {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the cache holds %v; want only the binary, with no staging or probe left behind", names)
	}
}

// The defect itself. Each case is something another account could have
// arranged; each must be refused, and the planted binary must not run.
func TestABinaryPlantedWhereAnotherAccountCouldWriteIsNotRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *harness, root string) string // returns thin_dir
		// why is the end of the path refused and the reason, so that
		// each case is shown to be refused for its own reason and not
		// for something an earlier case set off.
		why string
	}{
		{"a world-writable thin_dir", func(h *harness, root string) string {
			dir := mkdir(t, filepath.Join(root, "halite-thin"), 0o777)
			h.plant(dir, 0o755)
			return dir
		}, "/halite-thin: accounts other than its owner may write to it (drwxrwxrwx"},
		{"a group-writable thin_dir", func(h *harness, root string) string {
			dir := mkdir(t, filepath.Join(root, "halite-thin"), 0o770)
			h.plant(dir, 0o755)
			return dir
		}, "/halite-thin: accounts other than its owner may write to it (drwxrwx---"},
		{"a sticky world-writable thin_dir", func(h *harness, root string) string {
			// Sticky excuses a parent, never thin_dir itself: in a
			// sticky directory anybody may still add a file.
			dir := mkdir(t, filepath.Join(root, "halite-thin"), 0o777|os.ModeSticky)
			h.plant(dir, 0o755)
			return dir
		}, "/halite-thin: accounts other than its owner may write to it (drwxrwxrwt"},
		{"a group-writable binary", func(h *harness, root string) string {
			dir := mkdir(t, filepath.Join(root, "halite-thin"), 0o700)
			h.plant(dir, 0o775)
			return dir
		}, "/halite-thin/" + "<digest>" + ": accounts other than its owner may write to it (-rwxrwxr-x"},
		{"a world-writable binary", func(h *harness, root string) string {
			dir := mkdir(t, filepath.Join(root, "halite-thin"), 0o700)
			h.plant(dir, 0o757)
			return dir
		}, "/halite-thin/" + "<digest>" + ": accounts other than its owner may write to it (-rwxr-xrwx"},
		{"a binary that is a link", func(h *harness, root string) string {
			elsewhere := mkdir(t, filepath.Join(root, "elsewhere"), 0o700)
			planted := h.plant(elsewhere, 0o755)
			dir := mkdir(t, filepath.Join(root, "halite-thin"), 0o700)
			if err := os.Symlink(planted, filepath.Join(dir, h.digest)); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "/halite-thin/" + "<digest>" + ": it is not a regular file (l"},
		{"a thin_dir that is a link to a writable directory", func(h *harness, root string) string {
			writable := mkdir(t, filepath.Join(root, "writable"), 0o777)
			h.plant(writable, 0o755)
			dir := filepath.Join(root, "halite-thin")
			if err := os.Symlink(writable, dir); err != nil {
				t.Fatal(err)
			}
			return dir
		}, "/writable: accounts other than its owner may write to it (drwxrwxrwx"},
		{"a parent that is world-writable and not sticky", func(h *harness, root string) string {
			parent := mkdir(t, filepath.Join(root, "shared"), 0o777)
			dir := mkdir(t, filepath.Join(parent, "halite-thin"), 0o700)
			h.plant(dir, 0o755)
			return dir
		}, "/shared: accounts other than its owner may write to it (drwxrwxrwx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "")
			h.target.ThinDir = tc.setup(h, privateTempDir(t))

			r := h.run()
			if h.plantedRan() {
				t.Fatalf("the planted binary ran: return %q", r.Return)
			}
			if r.Err == nil {
				t.Fatalf("the run succeeded (pushed=%v, return %q); want a refusal", r.Pushed, r.Return)
			}
			t.Logf("refused: %v", r.Err)
			if why := strings.ReplaceAll(tc.why, "<digest>", h.digest); !strings.Contains(r.Err.Error(), why) {
				t.Errorf("refused, but not for %q:\n%v", why, r.Err)
			}
			for _, want := range []string{"web1.example", "cannot be trusted", "thin_dir", "halite: refusing"} {
				if !strings.Contains(r.Err.Error(), want) {
					t.Errorf("the refusal does not mention %q:\n%v", want, r.Err)
				}
			}
		})
	}
}

// fakeID puts an `id` on the target's path that answers another uid, so
// that everything the test made reads as somebody else's.
func fakeID(t *testing.T, o *Options) {
	t.Helper()
	bin := privateTempDir(t)
	writeExec(t, filepath.Join(bin, "id"), "#!/bin/sh\necho 4242\n")
	o.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
}

// A directory another account owns, with a mode that is otherwise
// perfect, is refused both where a binary is already there and where the
// run would make the directory and push.
func TestAThinDirOwnedByAnotherAccountIsRefused(t *testing.T) {
	if os.Geteuid() == 4242 {
		t.Skip("the stand-in uid is this account's own")
	}
	for _, planted := range []bool{true, false} {
		h := newHarness(t, "")
		dir := mkdir(t, filepath.Join(privateTempDir(t), "halite-thin"), 0o700)
		if planted {
			h.plant(dir, 0o700)
		}
		h.target.ThinDir = dir
		fakeID(t, h.o)

		r := h.run()
		if h.plantedRan() {
			t.Fatal("the planted binary ran")
		}
		if r.Err == nil || !strings.Contains(r.Err.Error(), "only root or the connecting account (uid 4242)") {
			t.Fatalf("planted=%v: want a refusal naming the owner rule, got %v", planted, r.Err)
		}
		if !regexp.MustCompile(`belongs to uid \d+`).MatchString(r.Err.Error()) {
			t.Errorf("the refusal does not say who owns it: %v", r.Err)
		}
	}
}

// `/var/tmp` itself is world-writable. It is sticky, so nobody but its
// owner may rename or remove `halite-thin` inside it, and that is what
// makes it an acceptable parent. Without this the default would refuse
// on every machine.
func TestAStickySharedParentIsAccepted(t *testing.T) {
	parent := mkdir(t, filepath.Join(privateTempDir(t), "var-tmp"), 0o777|os.ModeSticky)
	h := newHarness(t, filepath.Join(parent, "halite-thin"))
	if r := h.run(); r.Err != nil || !r.Pushed {
		t.Fatalf("a run under a sticky parent: pushed=%v err=%v", r.Pushed, r.Err)
	}
	if r := h.run(); r.Err != nil || r.Pushed {
		t.Fatalf("the second run under a sticky parent: pushed=%v err=%v", r.Pushed, r.Err)
	}
}

// A thin_dir the connecting account owns, but that others may write to,
// is refused rather than repaired, even with nothing in it yet. Being
// ours says who made it, not who has written into it since; and
// repairing it would mean deciding which of its entries to believe.
// Before this change `prepare` quietly `chmod 700`ed it.
func TestAThinDirOfOurOwnThatOthersMayWriteIsRefused(t *testing.T) {
	dir := mkdir(t, filepath.Join(privateTempDir(t), "halite-thin"), 0o777)
	h := newHarness(t, dir)
	r := h.run()
	if r.Err == nil || !strings.Contains(r.Err.Error(), "accounts other than its owner may write to it") {
		t.Fatalf("pushed=%v err=%v; want a refusal", r.Pushed, r.Err)
	}
}

// One it owns with a mode that is merely too open to read is tightened:
// a new directory comes from `mkdir -p` with the umask's mode, and the
// `chmod 700` after the owner check is what makes it private.
func TestANewThinDirIsMadePrivate(t *testing.T) {
	dir := filepath.Join(privateTempDir(t), "halite-thin")
	h := newHarness(t, dir)
	if r := h.run(); r.Err != nil || !r.Pushed {
		t.Fatalf("pushed=%v err=%v", r.Pushed, r.Err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("thin_dir is mode %o after the run, want 700", info.Mode().Perm())
	}
}

// A binary in a trusted place whose content is not its name is replaced,
// not run: the hash is taken in the same script as the check.
func TestACachedBinaryThatIsNotItsDigestIsReplaced(t *testing.T) {
	dir := mkdir(t, filepath.Join(privateTempDir(t), "halite-thin"), 0o700)
	h := newHarness(t, dir)
	h.plant(dir, 0o700)

	r := h.run()
	if h.plantedRan() {
		t.Fatal("a binary whose digest is not its name ran")
	}
	if r.Err != nil || !r.Pushed || string(r.Return) != `{"from":"pushed"}` {
		t.Fatalf("pushed=%v return=%q err=%v; want it replaced and the real one run", r.Pushed, r.Return, r.Err)
	}
}

// Root is trusted, and that is checked against a file root really owns.
func TestARootOwnedBinaryIsTrusted(t *testing.T) {
	var path string
	for _, candidate := range []string{"/bin/ls", "/bin/cat", "/usr/bin/env", "/usr/bin/true"} {
		info, err := os.Lstat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Uid == 0 && info.Mode().Perm()&0o022 == 0 {
			path = candidate
			break
		}
	}
	if path == "" {
		t.Skip("no regular, root-owned, unwritable executable in the usual places")
	}
	out, code := runScript(t, checkScript(filepath.Dir(path), path))
	if code != 0 || !regexp.MustCompile(`(?m)^present [0-9a-f]{64}$`).MatchString(out) {
		t.Fatalf("%s, which root owns, was not trusted (exit %d):\n%s", path, code, out)
	}
}

func TestARelativeThinDirIsRefusedBeforeConnecting(t *testing.T) {
	h := newHarness(t, "halite-thin")
	h.o.SSH = filepath.Join(privateTempDir(t), "no-such-ssh")
	r := h.run()
	if r.Err == nil || !strings.Contains(r.Err.Error(), "not an absolute path") {
		t.Fatalf("got %v; want a refusal saying thin_dir must be absolute", r.Err)
	}
}

// Two runs against one machine used to stage, and probe, under the same
// `<digest>.partial`, and one failed when the other renamed it first
// (handbook chapter 17). Each now stages under a name of its own.
//
// Every harness is built before any run starts. Building one writes its
// fake `ssh` and `scp` scripts, and they used to be written in the same
// loop that started the runs. So run 0 could fork while harness 5's
// `ssh` was still open for writing. The child holds a copy of that
// descriptor until it execs, and exec of a file open for writing fails
// with ETXTBSY, so run 5 reported
//
//	fork/exec …/ssh: text file busy
//
// That is Go's golang/go#22315. It failed the fips-test leg on PR #372
// and failed 6 times in 300 runs locally, and 0 in 1000 after this
// change. The race was in the test's fixtures. The package itself writes
// no local file it then runs: its only execs are the system's `ssh` and
// `scp`. DIVERGENCE 5.332.
func TestConcurrentRunsAgainstOneMachineBothSucceed(t *testing.T) {
	dir := filepath.Join(privateTempDir(t), "halite-thin")
	var wg sync.WaitGroup
	results := make([]Result, 6)
	harnesses := make([]*harness, len(results))
	for i := range harnesses {
		harnesses[i] = newHarness(t, dir)
	}
	for i, h := range harnesses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = h.run()
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r.Err != nil {
			t.Errorf("run %d: %v", i, r.Err)
		}
	}
}

// The probe's name is not fixed either. The old fixed name is taken
// here by something that cannot be written over, which is how a second
// run used to find it mid-way through another's; the old script exited
// 2 on it. This is the deterministic half of the concurrent test above,
// which passed locally and failed in CI on timing.
func TestTheProbeNameIsNotFixed(t *testing.T) {
	dir := mkdir(t, filepath.Join(privateTempDir(t), "halite-thin"), 0o700)
	if err := os.Mkdir(filepath.Join(dir, ".halite-probe"), 0o700); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, dir)
	if _, err := h.o.prepare(context.Background(), h.target, dir); err != nil {
		t.Fatalf("a taken `.halite-probe` stopped the run: %v", err)
	}
}

// The staging file is made by mktemp, not computed from the digest.
func TestTheStagingNameIsNotPredictable(t *testing.T) {
	dir := filepath.Join(privateTempDir(t), "halite-thin")
	h := newHarness(t, dir)
	first, err := h.o.prepare(context.Background(), h.target, dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.o.prepare(context.Background(), h.target, dir)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || strings.Contains(first, h.digest) {
		t.Errorf("staging names %q and %q; want two different names unrelated to the digest", first, second)
	}
	for _, s := range []string{first, second} {
		info, err := os.Lstat(s)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s: %v %v; want a private regular file made in advance", s, info, err)
		}
	}
}
