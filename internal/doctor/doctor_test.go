package doctor

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the module root")
		}
		dir = parent
	}
}

// all is every check this package offers, built with inputs that make
// each one runnable. The guards below read it rather than a hand-kept
// list, so a check that is added and not wired here is a check the
// guards do not see — which is the failure they exist to prevent.
func all(t *testing.T) []Check {
	t.Helper()
	kernel := false
	return []Check{
		ConfigValidity("/etc/halite/node.yaml", nil, nil, true),
		ClockSkew(time.Second, nil, time.Minute),
		CertificateExpiry(map[string]*x509.Certificate{
			"node": {NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour)},
		}, time.Now(), 14*24*time.Hour),
		Connectivity("hub.example:4506", func(context.Context) (string, time.Duration, error) {
			return "halite-hub ok", time.Millisecond, nil
		}),
		FileServerReachable("local roots", []string{"/srv/halite"}, nil),
		PillarCompiles(nil, 3),
		DiskFree(map[string]FreeSpace{"/var/lib/halite": {Free: 100 << 30}}, 1<<30, 128<<20),
		QueueDepths(map[string]QueueDepth{"reactor": {Depth: 1, Limit: 10000}}),
		ExtensionSignatures(true, []ExtensionTrust{{Name: "ext", Signed: true}}),
		FIPSConsistency(FIPSState{Kernel: &kernel, Platform: "linux"}),
	}
}

// Every check SPEC 26.4 names exists, and nothing exists that it does
// not name.
//
// The sentence is the specification's own list, and matching it is the
// point: a check named there and not built is a diagnostic an operator
// is entitled to and does not have. That was the whole of this package
// until it existed — SPEC 27.4 gives the FIPS mismatch warning to
// `doctor`, and `doctor` was one mention of the word in a comment.
func TestEveryCheckTheSpecNamesExists(t *testing.T) {
	inSpec := specChecks(t)
	if len(inSpec) < 5 {
		t.Fatalf("read %d checks out of SPEC 26.4; the sentence's shape has changed: %v",
			len(inSpec), inSpec)
	}
	built := map[string]bool{}
	for _, name := range Names(all(t)) {
		built[strings.ToLower(name)] = true
	}
	for _, name := range inSpec {
		if !built[strings.ToLower(name)] {
			t.Errorf("SPEC 26.4 names %q and this package has no such check", name)
		}
	}
	seen := map[string]bool{}
	for _, name := range inSpec {
		seen[strings.ToLower(name)] = true
	}
	for name := range built {
		if !seen[name] {
			t.Errorf("%q is a check here and SPEC 26.4 does not name it; either the "+
				"wording drifted or the specification needs it adding", name)
		}
	}
	t.Logf("SPEC 26.4 names %d checks: %v", len(inSpec), inSpec)
}

// Every check that can report anything but a pass carries a remediation
// line.
//
// SPEC 26.4 asks for "a pass or fail per check with a remediation
// line", and the remedy is the whole difference between this and
// reading the grains: a check that says a certificate expires in three
// days and stops has moved the problem rather than answered it.
//
// Driven through every status each check can produce rather than
// through the happy path, because the happy path is the one case that
// needs no remedy.
func TestEveryFindingCarriesARemedy(t *testing.T) {
	for _, res := range everyOutcome(t) {
		if res.Status == Pass {
			continue
		}
		if strings.TrimSpace(res.Remedy) == "" {
			t.Errorf("%s reported %s with no remediation line: %q",
				res.Name, res.Status, res.Detail)
		}
		if strings.TrimSpace(res.Detail) == "" {
			t.Errorf("%s reported %s with no detail; \"warn\" on its own is not an answer",
				res.Name, res.Status)
		}
	}
}

// And a pass says what it found, rather than only that it passed.
func TestAPassSaysWhatItFound(t *testing.T) {
	for _, c := range all(t) {
		res := c.Run(context.Background())
		if res.Status != Pass {
			continue
		}
		if strings.TrimSpace(res.Detail) == "" {
			t.Errorf("%s passed with no detail; \"expires in 71 days\" is the answer and "+
				"\"pass\" is not", c.Name)
		}
	}
}

// everyOutcome drives each check through the states it can reach.
func everyOutcome(t *testing.T) []Result {
	t.Helper()
	now := time.Now()
	yes, no := true, false
	var checks []Check

	checks = append(checks,
		ConfigValidity("/etc/halite/node.yaml", errors.New("line 3: bad indent"), nil, true),
		ConfigValidity("/etc/halite/node.yaml", nil, []string{"tracing"}, true),
		ConfigValidity("/etc/halite/node.yaml", nil, nil, false),

		CertificateExpiry(nil, now, time.Hour),
		CertificateExpiry(map[string]*x509.Certificate{
			"node": {NotBefore: now.Add(-time.Hour), NotAfter: now.Add(-time.Minute)},
		}, now, 14*24*time.Hour),
		CertificateExpiry(map[string]*x509.Certificate{
			"node": {NotBefore: now.Add(-time.Hour), NotAfter: now.Add(3 * 24 * time.Hour)},
		}, now, 14*24*time.Hour),
		CertificateExpiry(map[string]*x509.Certificate{
			"node": {NotBefore: now.Add(time.Hour), NotAfter: now.Add(48 * time.Hour)},
		}, now, time.Hour),

		Connectivity("", nil),
		Connectivity("hub.example:4506", func(context.Context) (string, time.Duration, error) {
			return "", 0, errors.New("connection refused")
		}),

		ClockSkew(0, errors.New("no answer"), time.Minute),
		ClockSkew(5*time.Minute, nil, time.Minute),
		ClockSkew(-5*time.Minute, nil, time.Minute),

		FileServerReachable("the hub", nil, errors.New("403")),
		FileServerReachable("local roots", nil, nil),

		PillarCompiles(errors.New("top.sls: no such file\nsecond line"), 0),

		DiskFree(nil, 1<<30, 1<<20),
		DiskFree(map[string]FreeSpace{"/var/lib/halite": {Free: 1 << 20}}, 1<<30, 128<<20),
		DiskFree(map[string]FreeSpace{"/var/lib/halite": {Free: 512 << 20}}, 1<<30, 128<<20),
		DiskFree(map[string]FreeSpace{"/var/lib/halite": {Err: errors.New("permission denied")}}, 1<<30, 1<<20),

		QueueDepths(nil),
		QueueDepths(map[string]QueueDepth{"reactor": {Depth: 10000, Limit: 10000}}),
		QueueDepths(map[string]QueueDepth{"reactor": {Depth: 6000, Limit: 10000}}),

		ExtensionSignatures(true, nil),
		ExtensionSignatures(true, []ExtensionTrust{{Name: "e", Err: errors.New("bad signature")}}),
		ExtensionSignatures(true, []ExtensionTrust{{Name: "e", Signed: false}}),
		ExtensionSignatures(false, []ExtensionTrust{{Name: "e", Signed: false}}),

		// Every FIPS combination, including both platforms.
		FIPSConsistency(FIPSState{Kernel: nil, Platform: "freebsd"}),
		FIPSConsistency(FIPSState{Kernel: nil, Artifact: true, Module: "v1.0.0", Platform: "freebsd"}),
		FIPSConsistency(FIPSState{Kernel: &yes, Artifact: true, Enabled: true, Module: "v1.0.0", Platform: "linux"}),
		FIPSConsistency(FIPSState{Kernel: &yes, Artifact: true, Enabled: false, Module: "v1.0.0", Platform: "linux"}),
		FIPSConsistency(FIPSState{Kernel: &yes, Platform: "linux"}),
		FIPSConsistency(FIPSState{Kernel: &no, Artifact: true, Enabled: true, Module: "v1.0.0", Platform: "linux"}),
		FIPSConsistency(FIPSState{Kernel: &no, Platform: "linux"}),
	)

	var out []Result
	for _, c := range checks {
		out = append(out, c.Run(context.Background()))
	}
	return out
}

// specChecks reads SPEC 26.4's sentence.
func specChecks(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	spec := string(b)
	start := strings.Index(spec, "### 26.4 Diagnostics")
	if start < 0 {
		t.Fatal("SPEC.md has no section 26.4; this audit is reading a document it was not written for")
	}
	section := spec[start:]
	if end := strings.Index(section, "\n## "); end > 0 {
		section = section[:end]
	}
	// "... doctor` check A, B, and C, and print a pass or fail ..."
	body := strings.Join(strings.Fields(section), " ")
	open := regexp.MustCompile(`doctor` + "`" + ` check `).FindStringIndex(body)
	if open == nil {
		t.Fatal("SPEC 26.4 no longer lists the checks in the shape this audit reads")
	}
	rest := body[open[1]:]
	end := strings.Index(rest, ", and print a pass or fail")
	if end < 0 {
		t.Fatal("SPEC 26.4's check list no longer ends where this audit expects")
	}
	var out []string
	for _, name := range strings.Split(rest[:end], ",") {
		name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "and "))
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// A check that is not for this role is not run at all.
//
// "queue depths: skipped, this is a node" on every node run is noise
// rather than information, and a report whose every other line is a
// skip is one an operator stops reading.
func TestAReportOnlyRunsTheChecksForItsRole(t *testing.T) {
	node := Run(context.Background(), RoleNode, all(t))
	hub := Run(context.Background(), RoleHub, all(t))

	has := func(r Report, name string) bool {
		for _, res := range r.Results {
			if res.Name == name {
				return true
			}
		}
		return false
	}
	if has(node, "queue depths") {
		t.Error("a node ran the hub's queue check")
	}
	if has(hub, "connectivity") {
		t.Error("a hub ran the node's connectivity check")
	}
	if has(hub, "extension signatures") {
		t.Error("a hub ran the node's extension check")
	}
	for _, name := range []string{"configuration validity", "FIPS mode consistency", "disk space"} {
		if !has(node, name) || !has(hub, name) {
			t.Errorf("%q should run for both roles", name)
		}
	}
	if len(node.Results) == 0 || len(hub.Results) == 0 {
		t.Fatal("a role ran no checks at all")
	}
}

// A warning does not fail the command and a failure does.
//
// `doctor` belongs in a cron job and in a state's `onlyif`. A
// certificate three weeks from expiry must not fail either — it is a
// thing to do this month, not a reason to stop.
func TestOnlyAFailureIsANonZeroExit(t *testing.T) {
	for _, tc := range []struct {
		what   string
		status Status
		want   int
	}{
		{"a clean run", Pass, 0},
		{"a warning", Warn, 0},
		{"a skip", Skip, 0},
		{"a failure", Fail, 1},
	} {
		r := Report{Results: []Result{
			{Name: "a", Status: Pass, Detail: "fine"},
			{Name: "b", Status: tc.status, Detail: "x", Remedy: "y"},
		}}
		if got := r.ExitCode(); got != tc.want {
			t.Errorf("%s exits %d, want %d", tc.what, got, tc.want)
		}
	}
}

// The rendered report puts the remedy under the check it belongs to,
// and never under a pass.
func TestTheRenderedReportShowsRemediesOnlyWhereTheyApply(t *testing.T) {
	text := Report{Results: []Result{
		{Name: "configuration validity", Status: Pass, Detail: "loads", Remedy: "should not appear"},
		{Name: "disk space", Status: Warn, Detail: "512 MiB free", Remedy: "check retention"},
	}}.Text()

	if strings.Contains(text, "should not appear") {
		t.Error("a passing check printed a remediation line")
	}
	if !strings.Contains(text, "check retention") {
		t.Error("a warning printed no remediation line")
	}
	if !strings.Contains(text, "1 pass, 1 warn") {
		t.Errorf("the summary line is missing or wrong:\n%s", text)
	}
}

// Sizes and durations are rendered the way somebody says them out loud.
func TestSizesAndDurationsReadTheWayAPersonSaysThem(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{5 * time.Second, "5s"},
		{5 * time.Minute, "5m"},
		{5 * time.Hour, "5h"},
		{72 * time.Hour, "3 days"},
	} {
		if got := roughly(tc.in); got != tc.want {
			t.Errorf("roughly(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct {
		in   uint64
		want string
	}{
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{5 << 20, "5.0 MiB"},
		{3 << 30, "3.0 GiB"},
	} {
		if got := bytesOf(tc.in); got != tc.want {
			t.Errorf("bytesOf(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
