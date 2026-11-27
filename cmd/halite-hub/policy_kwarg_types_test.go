package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// `run` reads a keyword's value with cli.Parse: `test=true` is the boolean,
// and `policy test --kwarg` kept every value a string. So a rule that
// requires `test: true` -- compared by type, `"true"` is not `true` -- allowed
// the call `run` sends and denied the one `policy test` was asked about, and
// a policy tested in CI disagreed with the hub it was written for (issue 881).
// The refusal said "requires test=true ... and the call has test=true", which
// reads as a contradiction.
func TestPolicyTestReadsAKeywordValueAsRunDoes(t *testing.T) {
	root := t.TempDir()
	policyYAML := `roles:
  reporter:
    - target: '*'
      functions: ['state.apply']
      args:
        'state.apply':
          require_kwargs: {test: true}
bindings:
  - principal: 'cert:CN=alice'
    roles: [reporter]
`
	if err := os.WriteFile(filepath.Join(root, "policy.yaml"), []byte(policyYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	ask := func(extra ...string) result {
		return run(t, append([]string{"policy", "test", "--root", root,
			"cert:CN=alice", "web1", "state.apply"}, extra...)...)
	}

	for _, tc := range []struct {
		name  string
		extra []string
		want  int
	}{
		{"test=true, which run sends as a boolean", []string{"--kwarg", "test=true"}, 0},
		{"test=True, which run sends as a boolean", []string{"--kwarg", "test=True"}, 0},
		{"test=false, which run sends as a boolean", []string{"--kwarg", "test=false"}, 1},
		{"test=yes, which run sends as a string", []string{"--kwarg", "test=yes"}, 1},
		// The flag still works, and is what the handbook told people to use.
		{"--test", []string{"--test"}, 0},
		{"no test keyword", nil, 1},
	} {
		res := ask(tc.extra...)
		if res.code != tc.want {
			t.Errorf("%s: exited %d, want %d: %s%s", tc.name, res.code, tc.want, res.stdout, res.stderr)
		}
	}
}

// The two paths that must agree, asked directly and with no policy in the way:
// the value `policy test` builds for a keyword is the value `run` builds for
// the same text, for every kind of text there is.
func TestPolicyTestAndRunBuildTheSameKeywordValue(t *testing.T) {
	for _, text := range []string{
		"true", "True", "false", "False", "null", "None",
		"yes", "no", "1", "1.0", "5", "text", "",
		`["a","b"]`, `{"k":"v"}`,
	} {
		t.Run(text, func(t *testing.T) {
			fromRun, err := cli.Parse([]string{"state.apply", "key=" + text})
			if err != nil {
				t.Fatal(err)
			}
			want, ok := fromRun.Kwargs.Get("key")
			if !ok {
				t.Fatalf("run read no keyword from key=%s", text)
			}

			fromPolicyTest, err := cli.Parse([]string{"--kwarg", "key=" + text})
			if err != nil {
				t.Fatal(err)
			}
			kwargs, err := policyTestKwargs(fromPolicyTest)
			if err != nil {
				// A comma inside a JSON value is the one thing --kwarg
				// refuses (it is the list separator), and run takes.
				t.Skipf("--kwarg refuses this text: %v", err)
			}
			if got := kwargs["key"]; !reflect.DeepEqual(got, want) {
				t.Errorf("policy test read key=%s as %#v (%T); run reads it as %#v (%T)", text, got, got, want, want)
			}
		})
	}
}

// A value that looks like JSON and is not is refused by `run`, with the
// key in the message. `--kwarg` reads it the same way now, so it is a usage
// error and not a string the rule never meant to judge.
func TestPolicyTestRefusesAValueThatLooksLikeJSONAndIsNot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "policy.yaml"),
		[]byte("roles: {}\nbindings: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := run(t, "policy", "test", "--root", root, "cert:CN=alice", "web1", "state.apply", "--kwarg", "x={bad")

	if res.code != cli.ExitUsage {
		t.Errorf("exited %d, want %d: %s%s", res.code, cli.ExitUsage, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "--kwarg x") {
		t.Errorf("the message does not name the key: %s", res.stderr)
	}
}
