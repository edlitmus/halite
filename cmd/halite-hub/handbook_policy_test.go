package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/policy"
)

// Chapter 26 of the handbook shows a policy and the decision it gives each
// request. The policy is a file under contrib/examples/handbook, quoted into
// the chapter byte for byte by the handbook's own check; this holds the file
// to the decisions the chapter states, so a reader who copies it gets the
// behaviour the page describes.

func loadHandbookPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	path := filepath.Join("..", "..", "contrib", "examples", "handbook", "security", "policy.yaml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, warnings, err := policy.Load(src, path)
	if err != nil {
		t.Fatalf("the handbook policy does not parse: %v", err)
	}
	for _, w := range warnings {
		t.Errorf("the handbook policy warns: %s", w.String())
	}
	loaded.ArbitraryCode = arbitraryCodeFunctions()
	return loaded
}

func TestTheHandbookPolicyDecidesWhatChapterTwentySixSays(t *testing.T) {
	p := loadHandbookPolicy(t)
	cases := []struct {
		name   string
		req    policy.Request
		allow  bool
		reason string // a fragment the chapter quotes, when it quotes one
	}{
		{"alice applies an allowed sls",
			policy.Request{Principal: "cert:CN=alice", Target: "web1.prod", Fun: "state.apply", Arg: []string{"webserver.nginx"}}, true, `role "webops"`},
		{"alice may not run the highstate by naming no sls",
			policy.Request{Principal: "cert:CN=alice", Target: "web1.prod", Fun: "state.apply"}, false, "runs the highstate"},
		{"alice may not apply another team's sls",
			policy.Request{Principal: "cert:CN=alice", Target: "web1.prod", Fun: "state.apply", Arg: []string{"db.postgres"}}, false, ""},
		{"alice may not pass pillar on the command line",
			policy.Request{Principal: "cert:CN=alice", Target: "web1.prod", Fun: "state.apply", Arg: []string{"webserver.nginx"}, Kwarg: map[string]any{"pillar": "{}"}}, false, ""},
		{"alice has no grant over the databases",
			policy.Request{Principal: "cert:CN=alice", Target: "db1.prod", Fun: "state.apply", Arg: []string{"webserver.nginx"}}, false, ""},
		{"alice may look at any node through readonly",
			policy.Request{Principal: "cert:CN=alice", Target: "db1.prod", Fun: "grains.items"}, true, ""},
		{"carol may install a package anywhere",
			policy.Request{Principal: "cert:CN=carol", Target: "web1.prod", Fun: "pkg.install", Arg: []string{"nginx"}}, true, `role "everything-but"`},
		{"carol's wildcard does not carry cmd.run",
			policy.Request{Principal: "cert:CN=carol", Target: "web1.prod", Fun: "cmd.run", Arg: []string{"id"}}, false, "never granted by a wildcard"},
		{"the pipeline runs the orchestration it is granted",
			policy.Request{Principal: "cert:CN=ci-pipeline", Fun: "state.orchestrate", Runner: true, Arg: []string{"deploy.web"}}, true, ""},
		{"the pipeline may not run another",
			policy.Request{Principal: "cert:CN=ci-pipeline", Fun: "state.orchestrate", Runner: true, Arg: []string{"other.thing"}}, false, ""},
		{"a load balancer may ask for ips",
			policy.Request{Principal: "node:lb1.prod", Target: "web1.prod", Fun: "ips"}, true, `role "lb_peer"`},
		{"a load balancer may not read pillar",
			policy.Request{Principal: "node:lb1.prod", Target: "web1.prod", Fun: "pillar.items"}, false, "pillar.items"},
		{"an unbound principal may do nothing",
			policy.Request{Principal: "cert:CN=mallory", Target: "web1.prod", Fun: "test.ping"}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.Authorize(c.req)
			if got.Allowed != c.allow {
				t.Fatalf("allowed=%v, want %v (%s)", got.Allowed, c.allow, got.Reason)
			}
			// Decision.String is what `policy test` prints and what the
			// chapter quotes.
			if c.reason != "" && !strings.Contains(got.String(), c.reason) {
				t.Errorf("%q does not contain %q, which the chapter quotes", got.String(), c.reason)
			}
		})
	}
}

// Chapter 26 quotes `policy show`'s "never granted by a wildcard" line and
// says it is that command's output for this build. It was not: the four
// functions DIVERGENCE 5.297 marked were missing from the page, and nothing
// compared the two. The quoted names are read out of the chapter and held to
// arbitraryCodeFunctions, which is what `policy show` prints.
func TestChapterTwentySixListsWhatPolicyShowPrints(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "docs", "handbook", "26-authorization.md"))
	if err != nil {
		t.Fatal(err)
	}
	const lead = "never granted by a wildcard: "
	text := string(src)
	start := strings.Index(text, lead)
	if start < 0 {
		t.Fatalf("chapter 26 no longer quotes the %q line; this check has stopped checking", lead)
	}
	block := text[start+len(lead):]
	block = block[:strings.Index(block, "```")]
	quoted := map[string]bool{}
	for _, name := range strings.Split(block, ",") {
		if name = strings.TrimSpace(name); name != "" {
			quoted[name] = true
		}
	}
	shipped := arbitraryCodeFunctions()
	for name := range shipped {
		if !quoted[name] {
			t.Errorf("policy show prints %s and chapter 26 does not list it", name)
		}
	}
	for name := range quoted {
		if !shipped[name] {
			t.Errorf("chapter 26 lists %s and policy show does not print it", name)
		}
	}
}
