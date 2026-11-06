package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/doctor"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/value"
)

// `doctor` scrubs what it prints, both ways it prints.
//
// The hub's has since DIVERGENCE 5.110, with the reason beside it: a check
// prints what it *found*, and the pillar check's finding is a compilation
// error that can name a decrypted value. The node's did not — and the node
// compiles its whole tree, so it reaches more GPG blocks than the hub.
// There was no node doctor test at all, which is why nobody noticed the
// difference between two functions that otherwise read alike.
//
// `cli.Redact` is not the answer: it is applied by `cli.Fatalf` and nowhere
// else, so a report printed normally passes it by.
func TestNodeDoctorScrubsBothOutputPaths(t *testing.T) {
	const secret = "hunter2-from-a-decrypted-pillar-value"

	n := nodeForEvidence(t, "")
	n.secrets = redact.New()
	n.secrets.Add(secret)

	// What the pillar check reports when a compilation error quotes a
	// value the renderer had already decrypted.
	report := doctor.Report{
		Role: doctor.RoleNode,
		Results: []doctor.Result{{
			Name:   "pillar compilation",
			Status: doctor.Fail,
			Detail: "the pillar does not compile: users.sls: password " + secret + " is not a mapping",
			Remedy: "Every state that reads pillar fails until this does.",
		}},
	}

	text := n.secrets.Scrub(n.doctorHeading() + report.Text())
	if strings.Contains(text, secret) {
		t.Errorf("the text output carries the secret:\n%s", text)
	}
	if !strings.Contains(text, "pillar compilation") {
		t.Errorf("the scrub took the report with it:\n%s", text)
	}
	if !strings.Contains(text, redact.Placeholder) {
		t.Errorf("nothing was replaced, so this test is not measuring the scrub:\n%s", text)
	}

	// And the structured path, which is the one that leaked on this host:
	// `--out json` printed the pillar file and its error verbatim.
	structured, ok := n.secrets.ScrubValue(doctor.Value(report)).(*value.Map)
	if !ok {
		t.Fatal("the scrubbed report is no longer a mapping, so `--out json` would change shape")
	}
	encoded, err := value.EncodeJSON(structured, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Errorf("the JSON output carries the secret:\n%s", encoded)
	}
	if !strings.Contains(string(encoded), "pillar compilation") {
		t.Errorf("the JSON output lost the report:\n%s", encoded)
	}
}

// `doctor`'s configuration check compares the file with what the *agent*
// started on, not with itself.
//
// `doctor` is its own process, and its setup loads the file a moment
// before the check re-reads it. Until DIVERGENCE 5.589 the check compared
// the warning counts of those two loads, so an agent started on one file
// and a file edited since passed as "matches what this process is
// running". The comparison is now with the configuration record the agent
// wrote to its evidence chain when it started.
func TestNodeDoctorComparesTheFileWithWhatTheAgentStartedOn(t *testing.T) {
	agent := nodeForEvidence(t, "log_level: info\n")
	agent.startEvidence()
	path := agent.cfg.Files[0]

	check := func(t *testing.T) doctor.Result {
		t.Helper()
		// What `doctor` does: setup loads the file as it is now, then the
		// check reads it again.
		cfg, err := config.Load(config.Node, config.LoadOptions{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		doc := *agent
		doc.cfg = cfg
		doc.evidence = &evidenceState{}
		args, err := cli.Parse([]string{"--config", path})
		if err != nil {
			t.Fatal(err)
		}
		return nodeConfigCheck(args, &doc).Run(context.Background())
	}

	t.Run("untouched", func(t *testing.T) {
		res := check(t)
		if res.Status != doctor.Pass || !strings.Contains(res.Detail, "matches what the agent recorded") {
			t.Errorf("an unedited file: %s %q", res.Status, res.Detail)
		}
	})

	t.Run("edited", func(t *testing.T) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		edited := strings.Replace(string(data), "log_level: info", "log_level: debug", 1)
		if edited == string(data) {
			t.Fatal("the test did not edit the file")
		}
		if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
			t.Fatal(err)
		}
		res := check(t)
		if res.Status != doctor.Warn || !strings.Contains(res.Detail, "differs from what the agent") {
			t.Errorf("a file edited since the agent started passed: %s %q", res.Status, res.Detail)
		}
	})
}

// With nothing to compare against, the check says so rather than
// claiming a match.
func TestNodeDoctorSaysWhenItCannotCompare(t *testing.T) {
	n := nodeForEvidence(t, "")
	path := n.cfg.Files[0]
	args, err := cli.Parse([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}
	res := nodeConfigCheck(args, n).Run(context.Background())
	if res.Status != doctor.Pass || strings.Contains(res.Detail, "matches") ||
		!strings.Contains(res.Detail, "not compared") {
		t.Errorf("no agent has started, and: %s %q", res.Status, res.Detail)
	}
}

// A node certificate that cannot be parsed is reported as unreadable, not
// as expired an impossible time ago. DIVERGENCE 5.594.
func TestNodeDoctorReportsAnUnreadableCertificateAsUnreadable(t *testing.T) {
	n := nodeForEvidence(t, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, pki.NodeCertFile), []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, err := cli.Parse([]string{"--pki-dir", dir})
	if err != nil {
		t.Fatal(err)
	}
	res := nodeCertificateCheck(args, n).Run(context.Background())
	if res.Status != doctor.Fail || !strings.Contains(res.Detail, "could not be read") ||
		strings.Contains(res.Detail, "expired") {
		t.Errorf("an unparseable certificate: %s %q", res.Status, res.Detail)
	}
}
