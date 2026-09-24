package main

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/doctor"
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
	structured, ok := n.secrets.ScrubValue(doctorValue(report)).(*value.Map)
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
