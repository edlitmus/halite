package main

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// `runner` and `orch` called the hub and then read --out, so a format
// that does not exist ran the runner -- decided a key, ran an
// orchestration, resumed one -- and only then exited with a usage error.
// And `orch list --limit` went to the hub as the string it was typed as.
// Each is a usage error now, before the hub is reached: in this empty
// root, reaching for the hub fails at the operator certificate, so a 64
// without that message is the check, not a refusal from further in.
// DIVERGENCE 5.590.
func TestRunnerAndOrchCheckTheirFlagsBeforeTheCall(t *testing.T) {
	root := t.TempDir()

	for _, control := range [][]string{
		{"runner", "manage.status"},
		{"runner", "manage.status", "--out", "json"},
		{"orch", "run", "deploy.web"},
		{"orch", "run", "deploy.web", "--out", "yaml"},
		{"orch", "show", "20260101T000000000000", "--out", "timeline"},
		{"orch", "list", "--limit", "5"},
		{"orch", "resume", "20260101T000000000000", "--from", "web"},
	} {
		res := run(t, append(control, "--root", root)...)
		if res.code != 1 || !strings.Contains(res.stderr, "operator certificate") {
			t.Fatalf("%v: exited %d (%s); this test assumes a well-formed command reaches "+
				"the operator certificate and fails there", control, res.code, res.stderr)
		}
	}

	for _, tc := range []struct {
		args []string
		says string
	}{
		{[]string{"runner", "manage.status", "--out", "tabel"}, "tabel"},
		{[]string{"orch", "run", "deploy.web", "--out", "tabel"}, "tabel"},
		{[]string{"orch", "lint", "deploy.web", "--out", "tabel"}, "tabel"},
		{[]string{"orch", "show", "20260101T000000000000", "--out", "tabel"}, "tabel"},
		{[]string{"orch", "list", "--out", "tabel"}, "tabel"},
		{[]string{"orch", "resume", "20260101T000000000000", "--from", "web", "--out", "tabel"}, "tabel"},
		{[]string{"orch", "list", "--limit", "abc"}, "--limit"},
		{[]string{"orch", "list", "--limit", "5x"}, "--limit"},
		{[]string{"orch", "list", "--limit", "0"}, "--limit"},
		{[]string{"orch", "list", "--limit=-1"}, "--limit"},
	} {
		res := run(t, append(tc.args, "--root", root)...)
		if res.code != cli.ExitUsage {
			t.Errorf("%v: exited %d, want %d: %s", tc.args, res.code, cli.ExitUsage, res.stderr)
		}
		if !strings.Contains(res.stderr, tc.says) {
			t.Errorf("%v: the message does not say %q: %s", tc.args, tc.says, res.stderr)
		}
		if strings.Contains(res.stderr, "operator certificate") {
			t.Errorf("%v: reached for the hub before the flags were read: %s", tc.args, res.stderr)
		}
	}
}
