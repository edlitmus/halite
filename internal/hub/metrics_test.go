package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// scrape reads /v1/metrics as an operator.
func scrape(t *testing.T, l *lab, as string) (string, error) {
	t.Helper()
	op := l.operator(t, as)
	return op.Metrics(context.Background())
}

func metricsLab(t *testing.T, policySrc string) *lab {
	t.Helper()
	l := newLab(t).withJobs(t)
	l.server.Metrics = metrics.NewRegistry()
	loaded, _, err := policy.Load([]byte(policySrc), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded
	return l
}

const metricsPolicy = `
roles:
  administrator:
    - target: '*'
      functions: ['*']
      runners: ['*']
  watcher:
    - runners: ['metrics.show']
  nothing:
    - runners: ['jobs.list']
bindings:
  - principal: 'cert:CN=ed'
    roles: ['administrator']
  - principal: 'cert:CN=watch'
    roles: ['watcher']
  - principal: 'cert:CN=none'
    roles: ['nothing']
`

func TestTheExpositionCarriesWhatTheHubDid(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	l.enrolled(t, "web1.example")

	op := l.operator(t, "ed")
	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := scrape(t, l, "ed")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`halite_build_info{component="hub"`,
		`halite_jobs_dispatched_total{fun="test.ping"} 1`,
		`halite_authz_decisions_total{result="allowed"}`,
		"halite_hub_nodes_connected 0",
		"halite_hub_keys_accepted 1",
		// The rule of SPEC 26.2: a drop path with no counter is a
		// backpressure design nobody can audit. An unlabelled one that
		// has not fired reads as zero; a labelled one is declared, so a
		// scraper can see it exists before anything has dropped.
		"# TYPE halite_events_dropped_total counter",
		"halite_reactor_dropped_total 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the exposition is missing %q", want)
		}
	}
}

// An unauthenticated scrape endpoint on a control plane tells anyone who
// asks how many nodes it has and when a deployment went out.
func TestScrapingNeedsTheGrant(t *testing.T) {
	l := metricsLab(t, metricsPolicy)

	if _, err := scrape(t, l, "watch"); err != nil {
		t.Errorf("a role granted metrics.show was refused: %v", err)
	}
	out, err := scrape(t, l, "none")
	if err == nil {
		t.Fatalf("a role without the grant scraped the hub: %s", out)
	}
	if !strings.Contains(err.Error(), "metrics.show") {
		t.Errorf("the refusal does not name the grant: %v", err)
	}
}

// A denial is counted as a denial. The useful alert is a rate of
// refusals, and a metric that counted only successes could not raise it.
func TestARefusalIsCountedAsOne(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	if _, err := scrape(t, l, "none"); err == nil {
		t.Fatal("the refusal did not happen")
	}
	out, err := scrape(t, l, "ed")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `halite_authz_decisions_total{result="denied"} 1`) {
		t.Errorf("the denial was not counted:\n%s", section(out, "halite_authz_decisions_total"))
	}
}

// A hub with metrics turned off still runs every line of instrumentation
// and answers the endpoint by saying so, rather than with an empty body
// that reads as a healthy estate with nothing happening.
func TestAHubWithoutARegistrySaysSo(t *testing.T) {
	l := newLab(t).withJobs(t)
	loaded, _, err := policy.Load([]byte(metricsPolicy), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded
	l.enrolled(t, "web1.example")

	out, err := scrape(t, l, "ed")
	if err == nil {
		t.Fatalf("a hub with no registry answered: %s", out)
	}
	if !strings.Contains(err.Error(), "records no metrics") {
		t.Errorf("the answer is %v", err)
	}
}

// section pulls the lines of one family out, for a readable failure.
func section(out, family string) string {
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, family) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
