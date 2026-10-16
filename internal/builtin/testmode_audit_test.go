package builtin

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// Every mutating execution function is held to the claim its signature
// makes about `--test`.
//
// # Why this exists
//
// `exec.Context.Test` says, in the field's own comment, "a run that must
// change nothing. Every function that mutates the system checks it." That
// was a claim about 271 functions and nothing checked it, and
// `signature.TestReliable` is the **zero value** — so a module that never
// thought about test mode makes the strongest of the three promises by
// saying nothing. That is the opposite of the evidence model in
// internal/exec, where the zero value deliberately means "nobody looked".
//
// Twenty-one functions were violating it when this was written: every
// mutating function in the language-manager modules (`pip install`,
// `npm install --global`, `gem install`, `cargo install`, `go install`,
// `cpan -i`, `composer require`, `mvn`, `virtualenv <path>`), `pkg.purge`,
// `pkg.upgrade`, `pkg.hold`, `pkg.unhold`, `service.force_reload`, and the
// seven `saltutil.sync_*` functions, which declared that test mode did not
// apply to them and fetched extension bundles under a dry run. Seven
// scheduling functions had gone the same way a day earlier (DIVERGENCE
// 5.130). The pattern in every case: one function in the module checked,
// so the shared signature's claim was believed for its neighbours.
//
// # How it decides
//
// Two independent checks, and a function has to fail both to be reported.
//
//   - *Dynamically*: the function is called **with Test set**, once in
//     each of several argument worlds (below), against a recording
//     runner, recording engines and a directory of its own. Whatever it
//     did, it did during a dry run.
//   - *Statically*: the code the registry holds for it is found in the
//     source (see TestEveryMutatingFunctionReachesTheTestFlag) and
//     searched for a reference to the context's Test field, following
//     the named functions it calls. This used to find a registration by
//     reading its `Module:` and `Function:` literals, which missed every
//     registration a helper built -- selinux's `mutate(function, …)`,
//     dnf_module's `mutating(…)`, `maskModule(…)` -- and so could not
//     excuse a correct one of those any more than it could catch a
//     wrong one.
//
// Neither alone is enough. A function that ran a command under a dry run
// may have been reading, which is how a prediction is made:
// `acl.wipe`, `at.atrm`, `ps.kill_pid`, `ps.pkill`, `reboot.cancel`,
// `firewall.disable` and `swap.off` all run one read and stop, and all
// seven are correct. The static check cannot see a mutation behind an
// interface it never reaches, and it cannot tell a `Test` reference that
// guards the mutation from one that only phrases a message. Together they
// name the ones that are wrong and nothing else.
//
// # Why there is no second, wet run
//
// The first version of this called each function again with Test unset, to
// prove the arguments had reached the mutating path — otherwise "the dry
// run did nothing" could mean "it never got there". That was wrong twice
// over.
//
// It was unsafe: a unit suite must not change the machine it runs on, and
// this one ran every mutating function for real. Most of them go through
// the recorder and could not execute, but the handful that write a fixed
// system path directly — `pam.set_module` on /etc/pam.d, `mount.set_fstab`
// on /etc/fstab, `system.set_system_date` on the clock — would have done
// it, as root, on whatever machine the suite was running on. All three
// turn out to honour `--test`, which is luck rather than design on the
// part of the audit.
//
// And it was unnecessary. **A dry run that did something is its own proof
// that the code was reached**, which is the only thing the wet run
// established. A function that did nothing under a dry run is not a
// violation whether it withheld the work or never arrived at it, so the
// verdict does not need to tell those apart — only the coverage number
// does, and that is reported as what it is.
//
// # What it does not cover
//
// The functions it cannot reach, which on any one machine is still many
// of them: a module gated to another platform refuses before it runs
// anything, and so does one whose tool is not installed. The count is
// reported so that a run on a different platform can be compared with
// this one, and the honest summary is that this audit is as broad as the
// machine it runs on -- which is why TestEveryMutatingFunctionReachesTheTestFlag
// exists beside it, and holds every platform's functions on every
// platform from the source.
//
// # Why every function is called several times
//
// The first version called each function once, with a path that named
// nothing. Nine `file.*` functions -- chmod, chown, chgrp, mkdir,
// makedirs, rmdir, move, hardlink, truncate -- acted under a dry run and
// were never seen: each failed on the missing path before the change,
// which looks exactly like a function that withheld it. `service.mask`
// and `unmask` were never seen either, because the machine had no init
// system that can mask and so both refused. And `beacons.*` and
// `schedule.*` refused on a context with no engine attached.
//
// So the arguments now name real things, in several worlds -- every path
// a regular file, every path an empty directory, the first path a file
// and the rest free names, every path free -- and the context carries a
// recording service provider that can mask, a beacon engine, a schedule
// and a mine, each of which reports a call as something done. A function
// is *judged* when some world got it past its own refusals: it either
// acted, or answered without an error. A function that refused in every
// world is *unreached*, and that bucket is the one a regression would
// hide in, so the functions this audit has been shown to reach are named
// in auditMustReach and fail the test if they fall into it.
func TestEveryMutatingFunctionHonoursItsTestModeClaim(t *testing.T) {
	r := New()
	idx := indexFunctionSource(t)
	installAuditServiceProvider(t)

	var violations []string
	acted, answered := 0, 0
	var unreached []string
	for _, name := range r.Exec.Signatures().Names() {
		sig, _ := r.Exec.Signatures().Lookup(name)
		if !sig.Mutates {
			continue
		}
		if sig.TestMode != signature.TestReliable {
			// Only the reliable claim promises that nothing changes.
			// `cmd.run` and its family declare unreliable and do run
			// under a dry run, which is Salt's behaviour and is what
			// `onlyif`, `unless` and `creates` are built on: a
			// conditional that did not run during a test would answer
			// for a machine nobody looked at. See the note in
			// DIVERGENCE about what `unreliable` is doing for two
			// different meanings. The static audit holds the rest of
			// the unreliable functions to reading Test at all.
			continue
		}
		var did []string
		reached := false
		for _, world := range auditWorlds {
			cmds, files, ok := callUnderAudit(t, r, name, sig, world)
			if ok {
				reached = true
			}
			if len(cmds) > 0 || len(files) > 0 {
				did = append(did, strings.TrimSpace(world.name+": "+strings.Join(cmds, " | ")+filesSuffix(files)))
			}
		}
		switch {
		case len(did) > 0:
			acted++
		case reached:
			answered++
			continue
		default:
			unreached = append(unreached, name)
			continue
		}
		if fn, ok := r.Exec.Func(name); ok {
			body, _ := idx.locate(reflect.ValueOf(fn).Pointer())
			if (body != nil && consultsTest(body, idx.named, 0)) || idx.delegatesReach(name) {
				// It consults Test, so what it did under the dry run is
				// the reading a prediction is made from.
				continue
			}
		}
		violations = append(violations, name+" — dry run did: "+strings.Join(did, " ; "))
	}

	for _, v := range violations {
		t.Errorf("%s\n\tIts signature says it honours `--test` and it did the same thing either "+
			"way. A dry run that acts is worse than one that fails: the operator has been told "+
			"nothing happened.", v)
	}
	if acted == 0 {
		t.Fatal("not one mutating function did anything under a dry run, which cannot be right: " +
			"this audit has stopped reaching the code")
	}
	unreachedSet := map[string]bool{}
	for _, n := range unreached {
		unreachedSet[n] = true
	}
	for _, n := range auditMustReach() {
		if unreachedSet[n] {
			t.Errorf("%s refused in every world this audit built, so a dry run of it was not "+
				"judged. It is reached on every platform -- it needs no tool and no platform -- "+
				"so the audit has stopped reaching it, and a regression in it would pass here", n)
		}
	}
	// All three numbers, because the last is the audit's blind spot and
	// saying only the first would read as coverage. A run on another
	// platform moves functions out of the last number.
	t.Logf("on %s: of the %d mutating functions that claim reliable, %d did something under a "+
		"dry run and were judged, %d were reached and answered without acting, and %d refused "+
		"in every world and were not judged",
		hostPlatform(), acted+answered+len(unreached), acted, answered, len(unreached))
}

// auditMustReach names the functions this audit must judge on every
// platform it runs on. Each needs no external tool and no platform, only
// the files and the recording engines the audit supplies, so each one
// that falls into the unreached bucket is the audit losing its grip
// rather than the machine lacking something. They are the ones that were
// unreached before and acting under a dry run.
//
// `service.mask` and `unmask` are left off on Windows, where the
// platform's own provider is chosen before the recording one and cannot
// mask.
func auditMustReach() []string {
	out := []string{
		"file.chmod", "file.mkdir", "file.makedirs", "file.rmdir", "file.move",
		"file.hardlink", "file.truncate",
		"beacons.add", "beacons.delete", "beacons.disable", "beacons.disable_beacon",
		"beacons.enable", "beacons.enable_beacon", "beacons.modify", "beacons.reset",
		"schedule.delete", "schedule.enable", "schedule.disable",
		"x509.create_private_key",
		"dnsutil.hosts_append", "dnsutil.hosts_remove",
	}
	if runtime.GOOS != "windows" {
		out = append(out, "service.mask", "service.unmask")
	}
	return out
}

// An auditWorld is one way of giving a function's path arguments
// something to name.
type auditWorld struct {
	name string
	// path makes the i-th path-like argument, named param, under dir.
	path func(t *testing.T, dir, param string, i int) string
}

var auditWorlds = []auditWorld{
	{"files", func(t *testing.T, dir, param string, _ int) string {
		return auditMake(t, filepath.Join(dir, param), false)
	}},
	{"dirs", func(t *testing.T, dir, param string, _ int) string {
		return auditMake(t, filepath.Join(dir, param), true)
	}},
	{"first-exists", func(t *testing.T, dir, param string, i int) string {
		if i == 0 {
			return auditMake(t, filepath.Join(dir, param), false)
		}
		return filepath.Join(dir, param)
	}},
	{"free", func(t *testing.T, dir, param string, _ int) string {
		// One level down, so that makedirs -- which creates a path's
		// parents and not the path -- has a parent to create.
		return filepath.Join(dir, param, "leaf")
	}},
}

// auditMake creates a file or an empty directory with a mode no audit
// argument asks for, so that a chmod to the audit's `0644` is a change.
func auditMake(t *testing.T, path string, dir bool) string {
	t.Helper()
	if dir {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if err := os.WriteFile(path, []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func hostPlatform() string {
	if v := os.Getenv("GOOS"); v != "" {
		return v
	}
	return runtime.GOOS
}

// callUnderAudit runs one function under a dry run in one world and
// reports what it did, and whether it answered without an error.
//
// The temporary directory is substituted out of the recorded commands, so
// that a result reads the same from one run to the next.
func callUnderAudit(t *testing.T, r *Registries, name string, sig signature.Signature,
	world auditWorld) ([]string, []string, bool) {
	t.Helper()
	dir := t.TempDir()
	args := auditArgs(t, sig, dir, world)
	before := auditSnapshot(dir)

	runner := &exec.RecordingRunner{Default: exec.Result{Code: 0}}
	rec := &auditRecorder{}
	c := newCtx(true)
	c.Runner = runner
	c.Ctx = context.Background()
	rec.attach(c)
	auditServiceRecorder = rec

	ok := false
	func() {
		// A function handed arguments it did not expect may panic, and a
		// panic here would say nothing about test mode.
		defer func() { _ = recover() }()
		_, err := r.Exec.Call(c, name, args)
		ok = err == nil
	}()
	auditServiceRecorder = nil

	ran := append(runner.RanCommands(), rec.calls...)
	for i := range ran {
		ran[i] = strings.ReplaceAll(ran[i], dir, "<dir>")
	}
	return ran, auditChanged(before, auditSnapshot(dir)), ok
}

// auditArgs makes a plausible value for every required parameter, and
// for every path-typed one whether required or not, so that a call
// reaches the code rather than failing validation -- which would look
// exactly like a function that changes nothing. An optional path is
// filled because leaving it empty is how `x509.create_private_key`
// returned its PEM rather than writing it, and so was never seen writing
// under a dry run; and how `dnsutil.hosts_append` would default to the
// machine's own /etc/hosts.
func auditArgs(t *testing.T, sig signature.Signature, dir string, world auditWorld) *value.Map {
	args := value.NewMap(len(sig.Params))
	paths := 0
	for _, p := range sig.Params {
		if auditIsPath(p) {
			args.Set(p.Name, world.path(t, dir, p.Name, paths))
			paths++
			continue
		}
		if !p.Required {
			continue
		}
		args.Set(p.Name, auditValue(p))
	}
	return args
}

func auditIsPath(p signature.Param) bool {
	if len(p.Choices) > 0 {
		return false
	}
	if p.Type == signature.Path {
		return true
	}
	if p.Type != signature.String || !p.Required {
		return false
	}
	switch name := strings.ToLower(p.Name); {
	case name == "name", name == "source", name == "target",
		strings.Contains(name, "path"), strings.Contains(name, "file"),
		strings.Contains(name, "dir"):
		return true
	}
	return false
}

func auditValue(p signature.Param) any {
	if len(p.Choices) > 0 {
		return p.Choices[0]
	}
	switch p.Type {
	case signature.Int:
		return int64(1)
	case signature.Float:
		return 1.0
	case signature.Bool:
		return false
	case signature.List:
		return []any{"halite-audit"}
	case signature.Map:
		return value.MapOf("halite-audit", "1")
	case signature.Mode:
		return "0644"
	case signature.Duration:
		return "1s"
	}
	return "halite-audit"
}

// auditSnapshot records every path under dir, directories included, with
// its mode and its contents. A directory is part of the picture because
// mkdir, makedirs and rmdir change nothing else; the mode because chmod
// changes nothing else.
func auditSnapshot(dir string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == dir {
			return nil
		}
		if info.IsDir() {
			out[path] = info.Mode().String()
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			out[path] = info.Mode().String() + ":unreadable"
			return nil
		}
		out[path] = info.Mode().String() + ":" + string(data)
		return nil
	})
	return out
}

func auditChanged(before, after map[string]string) []string {
	var out []string
	for path, state := range after {
		if before[path] != state {
			out = append(out, filepath.Base(path))
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			out = append(out, "removed:"+filepath.Base(path))
		}
	}
	sort.Strings(out)
	return out
}

func filesSuffix(files []string) string {
	if len(files) == 0 {
		return ""
	}
	return " [changed " + strings.Join(files, ", ") + "]"
}

// ---- the recording engines ----

// auditRecorder stands in for everything a node attaches to a context
// that a function can change other than through a command or a file: the
// beacon engine, the schedule, the saved configuration, the mine. Each
// call that would change something is written down as something done.
type auditRecorder struct{ calls []string }

func (a *auditRecorder) note(format string, args ...any) {
	a.calls = append(a.calls, fmt.Sprintf(format, args...))
}

func (a *auditRecorder) attach(c *exec.Context) {
	c.Beacons = auditBeacons{a}
	c.Schedule = auditSchedule{a}
	c.Mine = auditMine{a}
	c.SaveConfig = func(kind string, _ *value.Map) (string, error) {
		a.note("save config %s", kind)
		return "/nonexistent/" + kind, nil
	}
	c.ReloadConfig = func(kind string) error { a.note("reload config %s", kind); return nil }
}

type auditBeacons struct{ a *auditRecorder }

func (b auditBeacons) List() *value.Map { return value.NewMap(0) }
func (b auditBeacons) Add(n string, _ *value.Map) error {
	b.a.note("beacon add %s", n)
	return nil
}
func (b auditBeacons) Modify(n string, _ *value.Map) error {
	b.a.note("beacon modify %s", n)
	return nil
}
func (b auditBeacons) Delete(n string) error { b.a.note("beacon delete %s", n); return nil }
func (b auditBeacons) SetEnabled(n string, on bool) error {
	b.a.note("beacon enabled=%v %s", on, n)
	return nil
}
func (b auditBeacons) Reset() error         { b.a.note("beacon reset"); return nil }
func (b auditBeacons) Snapshot() *value.Map { return value.NewMap(0) }

type auditSchedule struct{ a *auditRecorder }

func (s auditSchedule) List() *value.Map { return value.NewMap(0) }
func (s auditSchedule) Add(n string, _ *value.Map) error {
	s.a.note("schedule add %s", n)
	return nil
}
func (s auditSchedule) Modify(n string, _ *value.Map) error {
	s.a.note("schedule modify %s", n)
	return nil
}
func (s auditSchedule) Delete(n string) error { s.a.note("schedule delete %s", n); return nil }
func (s auditSchedule) SetEnabled(n string, on bool) error {
	s.a.note("schedule enabled=%v %s", on, n)
	return nil
}
func (s auditSchedule) RunJob(_ context.Context, n string) error {
	s.a.note("schedule run %s", n)
	return nil
}
func (s auditSchedule) Snapshot() *value.Map { return value.NewMap(0) }
func (s auditSchedule) NextFireTime(string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

type auditMine struct{ a *auditRecorder }

func (m auditMine) Publish(functions map[string]exec.MineValue, replace bool) error {
	m.a.note("mine publish %d replace=%v", len(functions), replace)
	return nil
}
func (m auditMine) Fetch(string, string, string) (*value.Map, error) { return value.NewMap(0), nil }

// auditServiceRecorder is where the recording service provider writes;
// it is set for the length of one call.
var auditServiceRecorder *auditRecorder

// auditService is an init system that can do everything, including mask,
// and does none of it: each call is written down. It is put in front of
// the real providers for the length of the audit, which is how a
// `service.mask` that ignored `--test` was reproduced on a machine with
// no systemd.
type auditService struct{}

func (auditService) note(verb, name string) error {
	if auditServiceRecorder != nil {
		auditServiceRecorder.note("service %s %s", verb, name)
	}
	return nil
}
func (auditService) Name() string                                { return "halite-audit" }
func (auditService) Available(*exec.Context) bool                { return true }
func (auditService) Status(*exec.Context, string) (bool, error)  { return false, nil }
func (auditService) Enabled(*exec.Context, string) (bool, error) { return false, nil }
func (s auditService) Start(_ *exec.Context, n string) error     { return s.note("start", n) }
func (s auditService) Stop(_ *exec.Context, n string) error      { return s.note("stop", n) }
func (s auditService) Restart(_ *exec.Context, n string) error   { return s.note("restart", n) }
func (s auditService) Reload(_ *exec.Context, n string) error    { return s.note("reload", n) }
func (s auditService) Enable(_ *exec.Context, n string) error    { return s.note("enable", n) }
func (s auditService) Disable(_ *exec.Context, n string) error   { return s.note("disable", n) }
func (s auditService) Mask(_ *exec.Context, n string) error      { return s.note("mask", n) }
func (s auditService) Unmask(_ *exec.Context, n string) error    { return s.note("unmask", n) }
func (auditService) Masked(*exec.Context, string) (bool, error)  { return false, nil }
func (auditService) List(*exec.Context) ([]string, error)        { return []string{"halite-audit"}, nil }

func installAuditServiceProvider(t *testing.T) {
	t.Helper()
	saved := serviceProviders
	serviceProviders = append([]serviceProvider{auditService{}}, saved...)
	t.Cleanup(func() { serviceProviders = saved })
}

// A mutating function may not say that test mode does not apply to it.
//
// The same rule internal/builtin has held state modules to since it was
// written, applied to execution modules, where nothing said it and the
// seven `saltutil.sync_*` functions took the gap.
func TestNoMutatingFunctionSaysTestModeDoesNotApply(t *testing.T) {
	r := New()
	for _, name := range r.Exec.Signatures().Names() {
		sig, _ := r.Exec.Signatures().Lookup(name)
		if sig.Mutates && sig.TestMode == signature.TestNotApplicable {
			t.Errorf("%s changes the system, so its test mode cannot be not_applicable: "+
				"either it honours `--test` (reliable), or it cannot predict what it would do "+
				"(unreliable), and both of those are answers", name)
		}
	}
}

// moduleAndFunction reads the `Module:` and `Function:` fields out of a
// signature literal. A signature built by a helper names only the
// function, and the helper supplies the module — those are reported as
// unknown and fall to the dynamic check alone.
func moduleAndFunction(sig ast.Expr) (string, string) {
	lit, ok := sig.(*ast.CompositeLit)
	if !ok {
		return "", ""
	}
	module, function := "", ""
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		bl, ok := kv.Value.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			continue
		}
		text := strings.Trim(bl.Value, `"`)
		switch kv.Key.(*ast.Ident).Name {
		case "Module":
			module = text
		case "Function":
			function = text
		}
	}
	return module, function
}

// consultsTest reports whether code reads a `.Test` field, following
// the package's own functions by name.
//
// What counts as a call to one of them is narrower than it was. The
// first version followed every identifier that shared a name with a
// package-level function, including the selected half of a selector --
// so `errors.New(...)` was followed into this package's `New()`, which
// registers every module and so contains a Test reference somewhere.
// Any function that built an error with errors.New "consulted Test".
// That is how `pro.detach`, whose only helper says
// `errors.New("this node has no `pro`...")`, passed a check it had no
// business passing, and it is why a selector's name is never followed
// now, and neither is a struct literal's field key.
func consultsTest(e ast.Expr, named map[string]*ast.FuncDecl, depth int) bool {
	if e == nil {
		return false
	}
	return consultsNode(e, named, depth, nil)
}

func consultsBody(fd *ast.FuncDecl, named map[string]*ast.FuncDecl, depth int) bool {
	if fd.Body == nil {
		return false
	}
	return consultsNode(fd.Body, named, depth, fd)
}

func consultsNode(root ast.Node, named map[string]*ast.FuncDecl, depth int, self *ast.FuncDecl) bool {
	if depth > 4 {
		return false
	}
	found := false
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		if found || n == nil {
			return false
		}
		switch t := n.(type) {
		case *ast.SelectorExpr:
			if t.Sel.Name == "Test" {
				found = true
				return false
			}
			ast.Inspect(t.X, visit)
			return false
		case *ast.KeyValueExpr:
			if _, isField := t.Key.(*ast.Ident); isField {
				ast.Inspect(t.Value, visit)
				return false
			}
		case *ast.Ident:
			if fd, ok := named[t.Name]; ok && fd != self && fd.Body != nil &&
				consultsNode(fd.Body, named, depth+1, fd) {
				found = true
				return false
			}
		}
		return true
	}
	ast.Inspect(root, visit)
	return found
}
