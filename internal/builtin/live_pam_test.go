package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `pam`, driven against the real /etc/pam.d of whatever machine this
// runs on.
//
// # What a fixture cannot say
//
// pam_test.go's fixtures are real files, but they are four files from
// two platforms chosen by somebody who already knew what the parser
// does. The failure this project keeps finding is the other kind: the
// line nobody thought to put in a fixture, in the one file on the one
// distribution nobody develops on. DIVERGENCE 5.31 is a firewall whose
// idempotence test passed against a fixture written in the module's own
// spelling; plan.md §1.3 and §1.4 are two fixtures that forced a branch
// their platform does not take.
//
// So this reads *every* service the machine has, and checks the parse
// against the file rather than against an expectation. There is no
// fixture here to agree with.
//
// # Why it is read-only, with no HALITE_SYSTEM_LIVE gate
//
// The gate exists for tests that change the machine. This changes
// nothing: it opens /etc/pam.d, reads it, and asserts. That is safe on a
// developer's laptop, on a CI runner and on the production FreeBSD host
// this project is written on, which is the point — the estate's own
// files are the corpus, and gating them behind an environment variable
// would mean the corpus was never read.
//
// **Nothing here may write to /etc/pam.d.** A wrong line there locks
// every account out of the node, and a test is not a thing to find that
// out with. The mutating half is exercised against a throwaway tree in
// pam_test.go and against nothing else.
//
// It skips where there is no PAM at all, which is Windows, and where the
// directory is not readable, which is any account that is not root on
// some platforms.

// livePamSkip reports why this machine cannot be asked, or an empty
// string.
func livePamSkip() string {
	if runtime.GOOS == "windows" {
		return "Windows has no PAM"
	}
	entries, err := os.ReadDir("/etc/pam.d")
	if err != nil {
		return fmt.Sprintf("/etc/pam.d could not be read on this %s host: %v", runtime.GOOS, err)
	}
	if len(entries) == 0 {
		return "this host has an empty /etc/pam.d"
	}
	return ""
}

// pamActionableLines counts the lines PAM itself would act on: not
// blank, not a comment, and not the continuation of the line above.
//
// This is derived from /etc/pam.d/README's own description of the
// format rather than from the parser, which is the whole point — a
// count that shared the parser's idea of a line would agree with it
// whatever either of them did.
func pamActionableLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var (
		count      int
		continuing bool
	)
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, " \t\r")
		wasContinuing := continuing
		continuing = strings.HasSuffix(line, `\`)
		if wasContinuing {
			continue
		}
		if cut := strings.IndexByte(line, '#'); cut >= 0 {
			line = line[:cut]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		count++
	}
	return count
}

// Every line this machine's PAM would act on becomes exactly one rule.
//
// Two failures are caught here and neither needs an expected value. A
// parser that drops a line reports fewer rules than the file has, which
// is a chain an operator is told is shorter than the one that runs. A
// parser that tears one — the bracketed control flag, the continuation —
// reports more, which is a rule the machine does not have.
func TestPamParsesEveryLineOfThisMachinesRealConfiguration(t *testing.T) {
	if why := livePamSkip(); why != "" {
		t.Skip(why)
	}

	names, err := pamServices()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Skip("this host configures no PAM services")
	}
	t.Logf("reading %d real PAM services on %s", len(names), runtime.GOOS)

	for _, name := range names {
		path := filepath.Join("/etc/pam.d", name)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		rules, err := pamReadFile(path)
		if err != nil {
			t.Errorf("%s: this machine's own file could not be parsed: %v", path, err)
			continue
		}
		if want := pamActionableLines(t, path); len(rules) != want {
			t.Errorf("%s: parsed %d rules from %d lines PAM would act on", path, len(rules), want)
		}
	}
}

// Every control flag read off this machine is one PAM accepts.
//
// This is the reverse audit of the test above, and it is what catches a
// torn line that happens to leave the count right. `[success=1` is not a
// control flag, and neither is `default=ignore]`; if either appears in
// this column the field splitter has cut a bracketed flag in half.
func TestEveryControlFlagOnThisMachineIsOnePamAccepts(t *testing.T) {
	if why := livePamSkip(); why != "" {
		t.Skip(why)
	}

	// From /etc/pam.d/README on FreeBSD and pam.conf(5) on Linux-PAM.
	// `include` and `substack` are in the list because they occupy the
	// same column, which is exactly why pamIncludeTarget has to look at
	// it.
	known := map[string]bool{
		"required": true, "requisite": true, "sufficient": true,
		"optional": true, "binding": true, "include": true, "substack": true,
	}

	names, err := pamServices()
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, name := range names {
		path := filepath.Join("/etc/pam.d", name)
		rules, err := pamReadFile(path)
		if err != nil {
			continue
		}
		for _, rule := range rules {
			if rule.Control == pamAtInclude {
				continue
			}
			checked++
			flag := strings.ToLower(rule.Control)
			if known[flag] {
				continue
			}
			if strings.HasPrefix(flag, "[") && strings.HasSuffix(flag, "]") {
				continue
			}
			t.Errorf("%s:%d reads its control flag as %q, which PAM does not accept",
				path, rule.Line, rule.Control)
		}
	}
	if checked == 0 {
		t.Skip("no rules were readable on this host")
	}
	t.Logf("%d real control flags, all of them ones PAM accepts", checked)
}

// Resolving this machine's own includes produces a chain, terminates,
// and keeps every rule attributable to a file.
//
// The attribution is the part worth asserting. A rule that reached `su`
// through `auth include system` is one an operator has to edit in
// `system`, and an answer that named `su` would send them to a file that
// does not contain it.
func TestPamResolvesThisMachinesOwnIncludes(t *testing.T) {
	if why := livePamSkip(); why != "" {
		t.Skip(why)
	}

	names, err := pamServices()
	if err != nil {
		t.Fatal(err)
	}
	r := New()
	var resolved, throughAnInclude int
	for _, service := range names {
		args := value.NewMap(1)
		args.Set("service", service)
		out, err := r.Exec.Call(&exec.Context{}, "pam.rules", args)
		if err != nil {
			// A service file that is a directory or is unreadable is
			// not this test's business; the parse test above reports
			// what could not be read.
			continue
		}
		resolved++
		for _, entry := range out.([]any) {
			rule := entry.(*value.Map)
			file, _ := rule.GetString("file")
			line, _ := rule.GetString("line")
			if file == "" || file.(string) == "" {
				t.Errorf("%s: a resolved rule names no file", service)
				continue
			}
			if line.(int64) < 1 {
				t.Errorf("%s: a rule in %s reports line %v", service, file, line)
			}
			if filepath.Base(file.(string)) != service {
				throughAnInclude++
			}
		}
	}
	if resolved == 0 {
		t.Skip("no services were readable on this host")
	}
	t.Logf("resolved %d services; %d rules arrived through an include and kept the file that holds them",
		resolved, throughAnInclude)
}

// The sweep and the per-service answer agree with each other.
//
// These are two different code paths over the same data — one walks
// every service looking for a module, the other resolves one service and
// is asked whether the module is in it — and this project's commonest
// defect is a pair of paths that must agree and do not. Here the pair is
// checked against the machine's own most-used module rather than against
// a fixture.
func TestTheSweepAndHasModuleAgreeOnThisMachine(t *testing.T) {
	if why := livePamSkip(); why != "" {
		t.Skip(why)
	}

	// pam_unix is on every platform with PAM and is in nearly every
	// service. If this host somehow has none, the test says so rather
	// than passing on an empty set.
	const module = "pam_unix.so"

	r := New()
	sweepArgs := value.NewMap(1)
	sweepArgs.Set("module", module)
	out, err := r.Exec.Call(&exec.Context{}, "pam.services_using", sweepArgs)
	if err != nil {
		t.Fatal(err)
	}
	sweep := map[string]bool{}
	for _, name := range out.(*value.Map).SortedKeys() {
		sweep[name] = true
	}
	if len(sweep) == 0 {
		t.Skipf("no service on this host reaches %s", module)
	}

	names, err := pamServices()
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range names {
		args := value.NewMap(2)
		args.Set("service", service)
		args.Set("module", module)
		has, err := r.Exec.Call(&exec.Context{}, "pam.has_module", args)
		if err != nil {
			continue
		}
		if has.(bool) != sweep[service] {
			t.Errorf("%s: has_module says %v and the sweep says %v", service, has, sweep[service])
		}
	}
	t.Logf("%d of this host's %d services reach %s, and both paths agree on which",
		len(sweep), len(names), module)
}
