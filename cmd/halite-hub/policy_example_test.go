package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/policy"
)

// loadExamplePolicy reads contrib/examples/policy.yaml the way the CLI
// does, arbitrary-code list included. Loading it any other way would
// test a policy nobody runs.
func loadExamplePolicy(t *testing.T) *policy.Policy {
	t.Helper()
	path := filepath.Join("..", "..", "contrib", "examples", "policy.yaml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, warnings, err := policy.Load(src, path)
	if err != nil {
		t.Fatalf("the example policy does not parse: %v", err)
	}
	for _, w := range warnings {
		t.Errorf("the example policy warns: %s", w.String())
	}
	loaded.ArbitraryCode = arbitraryCodeFunctions()
	return loaded
}

// The example policy is documentation, so it has to be documentation
// that executes: every claim its comments make is a decision here.
func TestTheExamplePolicyDecidesWhatItSaysItDoes(t *testing.T) {
	p := loadExamplePolicy(t)

	cases := []struct {
		name      string
		req       policy.Request
		wantAllow bool
	}{
		{"webops applies an allowed sls to its own targets",
			policy.Request{Principal: "cert:CN=alice", Target: "web1.prod", Fun: "state.apply",
				Arg: []string{"webserver.nginx"}}, true},
		{"webops is not granted a target outside its pattern",
			policy.Request{Principal: "cert:CN=alice", Target: "db1.prod", Fun: "state.apply"}, false},
		{"webops may not pass pillar on the command line",
			policy.Request{Principal: "cert:CN=alice", Target: "web1.prod", Fun: "state.apply",
				Kwarg: map[string]any{"pillar": "{}"}}, false},
		{"webops may not apply an sls outside allow_sls",
			policy.Request{Principal: "cert:CN=alice", Target: "web1.prod", Fun: "state.apply",
				Arg: []string{"database.postgres"}}, false},
		{"readonly reaches the whole fleet",
			policy.Request{Principal: "cert:CN=alice", Target: "db1.prod", Fun: "test.ping"}, true},
		{"a runner grant is not a job grant",
			policy.Request{Principal: "cert:CN=ci-pipeline", Target: "web1.prod", Fun: "state.apply"}, false},
		{"the deployer runs the orchestration it is granted",
			policy.Request{Principal: "cert:CN=ci-pipeline", Fun: "state.orchestrate", Runner: true,
				Arg: []string{"deploy.web"}}, true},
		{"the deployer may not orchestrate outside allow_mods",
			policy.Request{Principal: "cert:CN=ci-pipeline", Fun: "state.orchestrate", Runner: true,
				Arg: []string{"teardown.everything"}}, false},
		{"a relay proxies and does nothing else",
			policy.Request{Principal: "node:relay1.prod", Fun: "relay.proxy", Runner: true}, true},
		{"a relay is not an operator",
			policy.Request{Principal: "node:relay1.prod", Target: "*", Fun: "test.ping"}, false},
		{"an unbound principal gets nothing",
			policy.Request{Principal: "cert:CN=nobody", Target: "web1.prod", Fun: "test.ping"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.Authorize(c.req)
			if got.Allowed != c.wantAllow {
				t.Errorf("allowed=%v, want %v (%s)", got.Allowed, c.wantAllow, got.Reason)
			}
		})
	}
}

// The example's own comment says a wildcard does not carry the
// arbitrary-code functions. That claim is the reason the break_glass
// role exists, so it is checked rather than asserted in prose.
func TestTheExampleAdministratorIsStillRefusedArbitraryCode(t *testing.T) {
	p := loadExamplePolicy(t)

	for _, fun := range []string{"cmd.run", "cmd.shell", "cmd.script",
		"module.run", "file.write", "file.replace"} {
		d := p.Authorize(policy.Request{
			Principal: "local:ed", Target: "web1.prod", Fun: fun,
		})
		if d.Allowed {
			t.Errorf("functions: ['*'] granted %s, which SPEC 23.5 says a wildcard never does", fun)
		}
	}

	// And naming them works, which is what break_glass is for. Nothing
	// is bound to it in the example, so a binding is added here rather
	// than shipped.
	p.Bindings = append(p.Bindings, policy.Binding{
		Principal: "cert:CN=oncall", Roles: []string{"break_glass"},
	})
	d := p.Authorize(policy.Request{
		Principal: "cert:CN=oncall", Target: "web1.prod", Fun: "cmd.run",
	})
	if !d.Allowed {
		t.Errorf("a role naming cmd.run was still refused: %s", d.Reason)
	}
}
