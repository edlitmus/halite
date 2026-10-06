package main

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// A subcommand missing an operand it needs, or given a number that is
// not one, is a usage error, refused before the hub is opened.
//
// These went through cli.Fatalf, which exits 1 -- the exit for a command
// that failed -- and several only after opening the hub: `keys show`
// with no node opened it in the call's own argument list before noticing.
// And the numeric flags were read with an unchecked fmt.Sscanf, so
// `--limit abc` kept the default, `--limit 10x` was 10 and
// `--ssh-concurrency 0` quietly became 8. DIVERGENCE 5.221.
//
// Each runs in an empty root, so reaching for the hub would fail there,
// with a message about the CA or the operator certificate, and with 1.
func TestMissingOperandsAndMalformedNumbersAreUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		args []string
		says string
	}{
		{[]string{"keys", "show"}, "show needs a node"},
		{[]string{"keys", "reject"}, "reject needs a node"},
		{[]string{"keys", "revoke"}, "revoke needs a node"},
		{[]string{"keys", "delete"}, "delete needs a node"},
		{[]string{"keys", "accept"}, "accept needs a node"},
		{[]string{"keys", "operator", "create"}, "operator create needs a name"},
		{[]string{"keys", "signer", "create"}, "signer create needs a name"},
		{[]string{"keys", "token", "create"}, "needs --ttl"},
		{[]string{"keys", "token", "create", "--ttl", "1h", "--uses", "abc"}, "--uses"},
		{[]string{"keys", "token", "create", "--ttl", "1h", "--uses", "0"}, "--uses"},
		{[]string{"keys", "token", "revoke"}, "token revoke needs a token id"},
		{[]string{"jobs", "show"}, "needs a jid"},
		{[]string{"jobs", "kill"}, "kill needs a jid"},
		{[]string{"jobs", "list", "--limit", "10x"}, "--limit"},
		{[]string{"jobs", "list", "--limit", "0"}, "--limit"},
		{[]string{"orch", "show"}, "orch show needs a jid"},
		{[]string{"orch", "resume", "20260101T000000000000"}, "needs --from"},
		{[]string{"orch", "run", "x.sls", "--pillar", "{nope"}, "--pillar"},
		{[]string{"runner", "doc"}, "runner doc needs a name"},
		{[]string{"lint"}, "lint needs a path"},
		{[]string{"migrate"}, "migrate needs a tree"},
		{[]string{"ssh", "*", "test.ping", "--ssh-concurrency", "0"}, "--ssh-concurrency"},
		{[]string{"ssh", "*", "test.ping", "--out", "xml"}, "xml"},
	} {
		name := strings.Join(tc.args, " ")
		res := run(t, append(tc.args, "--root", t.TempDir())...)
		if res.code != cli.ExitUsage {
			t.Errorf("%s: exited %d, want %d: %s", name, res.code, cli.ExitUsage, res.stderr)
		}
		if !strings.Contains(res.stderr, tc.says) {
			t.Errorf("%s: the message does not say %q: %s", name, tc.says, res.stderr)
		}
		for _, past := range []string{"enrollment CA", "operator certificate"} {
			if strings.Contains(res.stderr, past) {
				t.Errorf("%s: reached for the hub before the command line was understood: %s",
					name, res.stderr)
			}
		}
	}
}
