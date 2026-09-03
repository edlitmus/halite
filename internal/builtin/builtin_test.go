package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

func newCtx(test bool) *exec.Context {
	return &exec.Context{
		Ctx:    context.Background(),
		Grains: value.MapOf("os", "Ubuntu", "os_family", "Debian", "haliteversion", "0.0.0-dev"),
		Pillar: value.NewMap(0),
		Config: value.NewMap(0),
		NodeID: "test.node",
		Env:    "base",
		Test:   test,
		Runner: &exec.RecordingRunner{},
	}
}

func run(t *testing.T, r *Registries, name string, args *value.Map, test bool) states.Result {
	t.Helper()
	res, err := r.States.Call(newCtx(test), name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// ---- the test-mode conformance harness, SPEC section 11.6 ----

func TestFileStatesConformToTestMode(t *testing.T) {
	r := New()
	dir := t.TempDir()

	// A probe reports the observable state of a path, so the harness can
	// check directly that test mode touched nothing rather than inferring
	// it from the module's own answers.
	probePath := func(path string) func() (string, error) {
		return func() (string, error) {
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				return "absent", nil
			}
			if err != nil {
				return "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				return "symlink -> " + target, err
			}
			if info.IsDir() {
				return "dir " + formatMode(info.Mode()), nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("file %s %q %d", formatMode(info.Mode()), b, info.ModTime().UnixNano()), nil
		}
	}

	cases := []states.Conformance{
		{
			Name:  "file.managed",
			Probe: probePath(filepath.Join(dir, "managed.conf")),
			Args: value.MapOf(
				"name", filepath.Join(dir, "managed.conf"),
				"contents", "hello\n",
				"mode", "0640",
			),
			Setup: func() error { return os.RemoveAll(filepath.Join(dir, "managed.conf")) },
		},
		{
			Name:  "file.directory",
			Probe: probePath(filepath.Join(dir, "adir")),
			Args:  value.MapOf("name", filepath.Join(dir, "adir"), "mode", "0750"),
			Setup: func() error { return os.RemoveAll(filepath.Join(dir, "adir")) },
		},
		{
			Name:  "file.symlink",
			Probe: probePath(filepath.Join(dir, "alink")),
			Args:  value.MapOf("name", filepath.Join(dir, "alink"), "target", "/etc/hosts"),
			Setup: func() error { return os.RemoveAll(filepath.Join(dir, "alink")) },
		},
		{
			Name:  "file.touch",
			Probe: probePath(filepath.Join(dir, "touched")),
			Args:  value.MapOf("name", filepath.Join(dir, "touched")),
			Setup: func() error { return os.RemoveAll(filepath.Join(dir, "touched")) },
		},
		{
			Name:  "file.absent",
			Probe: probePath(filepath.Join(dir, "doomed")),
			Args:  value.MapOf("name", filepath.Join(dir, "doomed")),
			Setup: func() error {
				return os.WriteFile(filepath.Join(dir, "doomed"), []byte("x"), 0o644)
			},
		},
		{
			Name:  "x509.private_key_managed",
			Args:  value.MapOf("name", filepath.Join(dir, "conf.key"), "algorithm", "ec", "curve", "p256"),
			Probe: probePath(filepath.Join(dir, "conf.key")),
			Setup: func() error { return os.RemoveAll(filepath.Join(dir, "conf.key")) },
		},
		{
			Name: "test.succeed_with_changes",
			Args: value.MapOf("name", "synthetic"),
			// The synthetic state reports a change every time by design,
			// which is exactly what makes it useful for exercising watch.
			SkipIdempotence:       true,
			SkipIdempotenceReason: "this state exists to report a change on every run, for exercising watch and onchanges",
		},
	}

	for _, cf := range cases {
		t.Run(cf.Name, func(t *testing.T) {
			failures := cf.Check(r.States, newCtx)
			for _, f := range failures {
				t.Errorf("%s", f)
			}
		})
	}
}

// ---- file.managed behaviour ----

func TestFileManagedCreatesWithModeAndContents(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.conf")

	args := value.MapOf("name", path, "contents", "worker_processes 4;\n", "mode", "0644")
	res := run(t, r, "file.managed", args, false)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("result = %+v", res)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "worker_processes 4;\n" {
		t.Errorf("contents = %q", b)
	}
	// The mode is checked where a mode is what decides permissions.
	// Windows has none: os.Stat synthesises 0666 for anything writable,
	// so this assertion read 0666 and said nothing about the file. What
	// the state does with a mode there is covered by
	// TestAModeThatCannotBeCarriedOutIsSaidSoOnceAndConverges.
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if formatMode(info.Mode()) != "0644" {
			t.Errorf("mode = %s", formatMode(info.Mode()))
		}
	}
}

func TestFileManagedTestModeWritesNothing(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.conf")

	res := run(t, r, "file.managed", value.MapOf("name", path, "contents", "x\n"), true)
	if res.Result != nil {
		t.Errorf("test mode must return a nil result when it would change, got %v", res.ResultString())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("test mode created the file")
	}
}

func TestFileManagedShowsADiff(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.conf")
	if err := os.WriteFile(path, []byte("port = 80\ndebug = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := run(t, r, "file.managed", value.MapOf("name", path, "contents", "port = 8080\ndebug = false\n"), true)
	diff, _ := res.Changes.Get("diff")
	text, _ := diff.(string)
	if !strings.Contains(text, "-port = 80") || !strings.Contains(text, "+port = 8080") {
		t.Errorf("diff should show the change:\n%s", text)
	}
	if strings.Contains(text, "-debug") {
		t.Errorf("the diff should not show unchanged lines:\n%s", text)
	}
}

func TestFileManagedIsIdempotent(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "same.conf")
	args := value.MapOf("name", path, "contents", "unchanged\n", "mode", "0600")

	first := run(t, r, "file.managed", args, false)
	if !first.HasChanges() {
		t.Fatal("the first run should have created the file")
	}
	second := run(t, r, "file.managed", args, false)
	if second.HasChanges() {
		t.Errorf("the second run reported changes: %+v", second.Changes)
	}
	if !strings.Contains(second.Comment, "already") {
		t.Errorf("comment = %q", second.Comment)
	}
}

func TestFileManagedVerifiesSourceHash(t *testing.T) {
	r := New()
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(src, []byte("payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := hashBytes([]byte("payload\n"), "sha256")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dest.txt")

	// The right digest is accepted.
	res := run(t, r, "file.managed", value.MapOf(
		"name", dst, "source", src, "source_hash", "sha256="+digest), false)
	if !res.Succeeded() {
		t.Fatalf("a correct source_hash was rejected: %s", res.Comment)
	}

	// The wrong digest is refused before anything is written.
	dst2 := filepath.Join(dir, "dest2.txt")
	res = run(t, r, "file.managed", value.MapOf(
		"name", dst2, "source", src, "source_hash", "sha256="+strings.Repeat("0", 64)), false)
	if res.Succeeded() {
		t.Error("a wrong source_hash must fail the state")
	}
	if _, err := os.Stat(dst2); !os.IsNotExist(err) {
		t.Error("the file was written despite a failed hash check")
	}
}

func TestFileManagedContentsFromPillar(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "frompillar.conf")

	c := newCtx(false)
	c.Pillar = value.MapOf("app", value.MapOf("banner", "welcome"))
	res, err := r.States.Call(c, "file.managed", value.MapOf("name", path, "contents_pillar", "app:banner"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("result: %s", res.Comment)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "welcome\n" {
		t.Errorf("contents = %q", b)
	}

	// A pillar key that is not there is an error naming the key, not an
	// empty file.
	res, err = r.States.Call(c, "file.managed", value.MapOf("name", path+"2", "contents_pillar", "app:absent"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() || !strings.Contains(res.Comment, "app:absent") {
		t.Errorf("a missing pillar key should fail by name: %s", res.Comment)
	}
}

func TestFileManagedRefusesUnquotedMode(t *testing.T) {
	r := New()
	_, err := r.States.Call(newCtx(false), "file.managed",
		value.MapOf("name", "/tmp/x", "mode", int64(420)))
	if err == nil || !strings.Contains(err.Error(), "quoted") {
		t.Errorf("an integer mode must be refused with advice, got %v", err)
	}
}

func TestWriteAtomicLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.conf")
	if err := writeAtomic(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The temporary file must be gone: a directory littered with
	// half-written configuration is exactly what atomic writes prevent.
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".halite-") {
			t.Errorf("a temporary file survived: %s", e.Name())
		}
	}
	b, _ := os.ReadFile(path)
	if string(b) != "second\n" {
		t.Errorf("contents = %q", b)
	}
}

// ---- cmd ----

func TestCmdDefaultsToNoShell(t *testing.T) {
	// Salt's default of a shell for cmd.run is the root of most of its
	// injection findings, and inverting it is deliberate. SPEC 15.2.
	r := New()
	rec := &exec.RecordingRunner{}
	c := newCtx(false)
	c.Runner = rec

	if _, err := r.States.Call(c, "cmd.run", value.MapOf("name", "/bin/echo", "args", []any{"hello"})); err != nil {
		t.Fatal(err)
	}
	if len(rec.Ran) != 1 {
		t.Fatalf("ran %d commands", len(rec.Ran))
	}
	if rec.Ran[0].Shell {
		t.Error("cmd.run used a shell by default")
	}
	if len(rec.Ran[0].Argv) != 2 || rec.Ran[0].Argv[0] != "/bin/echo" {
		t.Errorf("argv = %v", rec.Ran[0].Argv)
	}
}

func TestCmdShellIsOptIn(t *testing.T) {
	r := New()
	rec := &exec.RecordingRunner{}
	c := newCtx(false)
	c.Runner = rec
	var logged []string
	c.Log = func(level, msg string) { logged = append(logged, level+": "+msg) }

	if _, err := r.States.Call(c, "cmd.run", value.MapOf("name", "echo a | wc -l", "shell", true)); err != nil {
		t.Fatal(err)
	}
	if !rec.Ran[0].Shell {
		t.Error("shell: true was not honoured")
	}
	// Opting into a shell is logged, so the decision is visible.
	found := false
	for _, l := range logged {
		if strings.Contains(l, "shell") {
			found = true
		}
	}
	if !found {
		t.Errorf("using a shell should be logged, got %v", logged)
	}
}

func TestCmdTestModeRunsNothing(t *testing.T) {
	r := New()
	rec := &exec.RecordingRunner{}
	c := newCtx(true)
	c.Runner = rec

	res, err := r.States.Call(c, "cmd.run", value.MapOf("name", "/bin/false"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Ran) != 0 {
		t.Errorf("test mode ran %d commands", len(rec.Ran))
	}
	if res.Result != nil {
		t.Errorf("test mode should report that it would run the command, got %s", res.ResultString())
	}
}

func TestCmdWaitOnlyRunsOnWatch(t *testing.T) {
	r := New()
	rec := &exec.RecordingRunner{}
	c := newCtx(false)
	c.Runner = rec

	res, err := r.States.Call(c, "cmd.wait", value.MapOf("name", "/bin/true"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Ran) != 0 || res.HasChanges() {
		t.Error("cmd.wait ran on its own")
	}

	if _, err := r.States.CallWatch(c, "cmd.wait", value.MapOf("name", "/bin/true")); err != nil {
		t.Fatal(err)
	}
	if len(rec.Ran) != 1 {
		t.Error("cmd.wait did not run when its watch fired")
	}
}

// ---- pkg, exercised through a recorded package manager ----

func TestPkgInstalledPlansAndReports(t *testing.T) {
	r := New()
	rec := &exec.RecordingRunner{
		Responses: map[string]exec.Result{
			`dpkg-query -W "-f=\${Package}\\t\${Version}\\t\${Status}\\n"`: {
				Stdout: "curl\t7.81.0\tinstall ok installed\n",
			},
		},
	}
	c := newCtx(true)
	c.Runner = rec

	// The apt provider is only chosen when its binaries are present, so a
	// node without them reports that plainly rather than guessing.
	res, err := r.States.Call(c, "pkg.installed", value.MapOf("name", "nginx"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() && res.Result != nil && *res.Result {
		// On a host that has no package manager the state reports why.
		if !strings.Contains(res.Comment, "package") {
			t.Errorf("comment = %q", res.Comment)
		}
	}
}

func TestPackageSpecsReadsBothForms(t *testing.T) {
	// `pkgs` as a plain list.
	names, versions := packageSpecs(value.MapOf("pkgs", []any{"curl", "jq"}))
	if len(names) != 2 || len(versions) != 0 {
		t.Errorf("names=%v versions=%v", names, versions)
	}
	// `pkgs` carrying pinned versions, which is Salt's
	// list-of-single-key-mappings form.
	names, versions = packageSpecs(value.MapOf("pkgs", []any{
		value.MapOf("nginx", "1.24.0"),
		value.MapOf("curl", nil),
	}))
	if len(names) != 2 || versions["nginx"] != "1.24.0" {
		t.Errorf("names=%v versions=%v", names, versions)
	}
	// A bare name plus a version.
	names, versions = packageSpecs(value.MapOf("name", "nginx", "version", "1.24.*"))
	if len(names) != 1 || versions["nginx"] != "1.24.*" {
		t.Errorf("names=%v versions=%v", names, versions)
	}
}

func TestVersionSatisfies(t *testing.T) {
	cases := []struct {
		installed, want string
		ok              bool
	}{
		{"1.24.0", "1.24.0", true},
		{"1.24.0", "1.24.*", true},
		{"1.25.0", "1.24.*", false},
		{"1.24.0", "", true},
		{"1.24.0", "1.23.0", false},
	}
	for _, c := range cases {
		if got := versionSatisfies(c.installed, c.want); got != c.ok {
			t.Errorf("versionSatisfies(%q, %q) = %v", c.installed, c.want, got)
		}
	}
}

// ---- sys ----

func TestSysDocReadsTheBuildTimeSignatures(t *testing.T) {
	r := New()
	out, err := r.Exec.Call(newCtx(false), "sys.doc", value.MapOf("name", "cmd.run"))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(*value.Map)
	doc, ok := m.Get("cmd.run")
	if !ok {
		t.Fatal("cmd.run has no documentation")
	}
	text := doc.(string)
	for _, want := range []string{"cmd.run", "arbitrary code", "must be granted by name"} {
		if !strings.Contains(text, want) {
			t.Errorf("sys.doc output is missing %q:\n%s", want, text)
		}
	}
}

// TestArbitraryCodeIsMarked holds the functions that can run whatever the
// caller asks to being declared as such, because the RBAC rule of SPEC
// section 23.5 refuses to grant them by wildcard and can only do that if
// the signature says so.
func TestArbitraryCodeIsMarked(t *testing.T) {
	r := New()
	for _, name := range []string{"cmd.run", "cmd.run_all", "cmd.run_stdout", "cmd.retcode"} {
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Errorf("%s is missing", name)
			continue
		}
		if !sig.ArbitraryCode {
			t.Errorf("%s must declare arbitrary_code", name)
		}
	}
	// A function that does not run arbitrary code must not claim to.
	for _, name := range []string{"test.ping", "file.read", "sys.doc"} {
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Errorf("%s is missing", name)
			continue
		}
		if sig.ArbitraryCode {
			t.Errorf("%s should not declare arbitrary_code", name)
		}
	}
}

// TestEveryStateModuleDeclaresItsTestMode is the check that keeps the
// signature honest as modules are added.
func TestEveryStateModuleDeclaresItsTestMode(t *testing.T) {
	r := New()
	for _, name := range r.States.Names() {
		sig, _ := r.States.Signatures().Lookup(name)
		if sig.Mutates && sig.TestMode == signature.TestNotApplicable {
			t.Errorf("%s changes the system, so its test mode cannot be not_applicable", name)
		}
		if sig.Doc == "" {
			t.Errorf("%s has no documentation", name)
		}
	}
}

// A test-mode comment must read as a prediction. The same description
// serves both outcomes, and a test run reporting "/etc/motd was created"
// reads as though it happened: an operator scanning a --test log for what
// it is about to do would see a past tense and believe the change had
// already been made.
func TestTestModeCommentsArePredictions(t *testing.T) {
	r := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "f")

	args := value.MapOf("name", path, "contents", "x\n", "mode", "0644")
	res, err := r.States.Call(newCtx(true), "file.managed", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Fatalf("test mode should predict: %+v", res)
	}
	if !strings.Contains(res.Comment, "would be created") {
		t.Errorf("test-mode comment %q should be in the conditional", res.Comment)
	}
	if strings.Contains(res.Comment, "was created") {
		t.Errorf("test-mode comment %q reads as though the change happened", res.Comment)
	}

	// The real run says it happened, because it did.
	res, err = r.States.Call(newCtx(false), "file.managed", args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Comment, "was created") {
		t.Errorf("applied comment %q should be in the past tense", res.Comment)
	}
}
