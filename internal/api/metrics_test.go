package api

import (
	"net/http"
	"strings"
	"testing"
)

const metricsPolicy = `
roles:
  operator:
    - target: '*'
      functions: ['*']
      runners: ['*']
bindings:
  - principal: 'local:ed'
    roles: ['operator']
`

// A policy where the only account is granted something, but not this.
const narrowPolicy = `
roles:
  narrow:
    - runners: ['jobs.list']
bindings:
  - principal: 'local:ed'
    roles: ['narrow']
`

func TestTheExpositionCarriesBothServices(t *testing.T) {
	l, hub := executeLab(t, metricsPolicy)
	hub.metricsBody = "# HELP halite_hub_nodes_connected Nodes.\n" +
		"# TYPE halite_hub_nodes_connected gauge\nhalite_hub_nodes_connected 4\n"
	token := l.login(t, "ed", "hunter2").Token

	res, body := l.get(t, PathMetrics, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the scrape answered %d: %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("the content type is %q", got)
	}
	for _, want := range []string{
		`halite_build_info{component="api"`,
		`halite_auth_attempts_total{method="local",result="accepted"} 1`,
		// The hub's own exposition, fetched under this service's
		// certificate and appended.
		"halite_hub_nodes_connected 4",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the exposition is missing %q:\n%s", want, body)
		}
	}
}

// A login mints a token, and how many exist is a question the exposition
// could not answer: an estate whose tokens are never revoked has a
// growing store and nothing said so.
func TestTheExpositionCountsTokens(t *testing.T) {
	l, _ := executeLab(t, metricsPolicy)
	token := l.login(t, "ed", "hunter2").Token

	_, body := l.get(t, PathMetrics, token)
	for _, want := range []string{
		`halite_api_tokens_issued_total{method="local"} 1`,
		"halite_api_tokens_live 1",
		// Declared before anything has been revoked, so an alert on it
		// works from the first scrape.
		"halite_api_tokens_revoked_total 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the exposition is missing %q:\n%s", want, section(body, "halite_api_tokens"))
		}
	}
}

// The API is a client of the hub, so half its latency is not its own.
// Without this the only question the exposition could answer about a
// slow API was that it was slow.
func TestTheExpositionTimesTheHubItWaitsOn(t *testing.T) {
	l, hub := executeLab(t, metricsPolicy)
	hub.metricsBody = "# HELP halite_hub_nodes_connected Nodes.\n" +
		"# TYPE halite_hub_nodes_connected gauge\nhalite_hub_nodes_connected 4\n"
	token := l.login(t, "ed", "hunter2").Token

	// One scrape, which is itself a request to the hub, then a second
	// that can see the first counted.
	l.get(t, PathMetrics, token)
	_, body := l.get(t, PathMetrics, token)

	for _, want := range []string{
		`halite_api_hub_requests_total{route="/v1/metrics",code="200"}`,
		`halite_api_hub_request_duration_seconds_count{route="/v1/metrics"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the exposition is missing %q:\n%s", want, section(body, "halite_api_hub_request"))
		}
	}
}

// A request that is in flight and one that finished answer different
// questions: a wedged service is requests arriving and none finishing,
// and the completed counter goes flat and says nothing about it.
func TestTheExpositionDeclaresWhatIsInFlight(t *testing.T) {
	l, _ := executeLab(t, metricsPolicy)
	token := l.login(t, "ed", "hunter2").Token

	_, body := l.get(t, PathMetrics, token)
	if !strings.Contains(body, "# TYPE halite_api_requests_in_flight gauge") {
		t.Errorf("the in-flight gauge is not declared:\n%s", body)
	}
	// The scrape is itself in flight while it renders, and it is
	// counted under the route it will be counted under when it
	// finishes -- a gauge that goes up under one label and down under
	// another leaks a series that never returns to zero.
	if !strings.Contains(body, `halite_api_requests_in_flight{route="/v1/metrics"} 1`) {
		t.Errorf("the scrape did not count itself:\n%s",
			section(body, "halite_api_requests_in_flight"))
	}
	if !strings.Contains(body, `halite_api_response_bytes_total{route="/v1/login"}`) {
		t.Errorf("the login's response was not counted:\n%s",
			section(body, "halite_api_response_bytes_total"))
	}
}

// A scrape that fails entirely because the hub is unreachable loses the
// API's own metrics too — and one of those is how often the hub is
// unreachable.
func TestAnUnreachableHubLeavesTheServicesOwnMetrics(t *testing.T) {
	l, hub := executeLab(t, metricsPolicy)
	hub.metricsFails = "the hub refused"
	token := l.login(t, "ed", "hunter2").Token

	res, body := l.get(t, PathMetrics, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the scrape answered %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "halite_build_info") {
		t.Errorf("the service's own metrics are missing:\n%s", body)
	}
	if !strings.Contains(body, "# the hub's metrics are absent") {
		t.Errorf("nothing says the hub is missing:\n%s", body)
	}
	// A comment, so a scraper ignores it rather than failing to parse.
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "the hub's metrics are absent") && !strings.HasPrefix(line, "#") {
			t.Errorf("the explanation is not a comment: %q", line)
		}
	}
	if !strings.Contains(body, "halite_api_hub_scrape_failures_total 1") {
		t.Errorf("the failure was not counted:\n%s", body)
	}
}

func TestScrapingTheAPINeedsTheGrant(t *testing.T) {
	l, _ := executeLab(t, narrowPolicy)
	token := l.login(t, "ed", "hunter2").Token

	res, body := l.get(t, PathMetrics, token)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a token without the grant scraped: %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "metrics.show") {
		t.Errorf("the refusal does not name the grant: %s", body)
	}
}

// A job identifier in a path would be a series per job, which is exactly
// the unbounded label that kills a metrics endpoint.
func TestARequestIsCountedByRouteNotByPath(t *testing.T) {
	l, _ := executeLab(t, metricsPolicy)
	token := l.login(t, "ed", "hunter2").Token
	l.get(t, PathJob+"20260824T101500.000000", token)
	l.get(t, PathJob+"20260824T101500.000001", token)

	_, body := l.get(t, PathMetrics, token)
	if !strings.Contains(body, `halite_api_requests_total{route="/v1/jobs/{id}",code="200"} 2`) {
		t.Errorf("requests are not counted by route:\n%s", section(body, "halite_api_requests_total"))
	}
	if strings.Contains(body, "20260824T101500") {
		t.Errorf("a job identifier reached a label:\n%s", section(body, "halite_api_requests_total"))
	}
}

func TestAFailedLoginIsCounted(t *testing.T) {
	l, _ := executeLab(t, metricsPolicy)
	l.post(t, PathLogin, `{"username":"ed","password":"wrong"}`, "")
	token := l.login(t, "ed", "hunter2").Token

	_, body := l.get(t, PathMetrics, token)
	if !strings.Contains(body, `halite_auth_attempts_total{method="local",result="refused"} 1`) {
		t.Errorf("the refusal was not counted:\n%s", section(body, "halite_auth_attempts_total"))
	}
}

func section(out, family string) string {
	var kept []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, family) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// Both components expose `halite_build_info`, and two `# HELP` lines for
// one metric name make the whole document invalid — a scraper answers
// "no metrics at all", not "one duplicated family". Found by scraping a
// real API with a real hub behind it.
func TestTheMergedExpositionDeclaresEachFamilyOnce(t *testing.T) {
	l, hub := executeLab(t, metricsPolicy)
	hub.metricsBody = "# HELP halite_build_info Build identity.\n" +
		"# TYPE halite_build_info gauge\n" +
		`halite_build_info{component="hub",version="v1"} 1` + "\n"
	token := l.login(t, "ed", "hunter2").Token

	_, body := l.get(t, PathMetrics, token)
	if got := strings.Count(body, "# HELP halite_build_info"); got != 1 {
		t.Errorf("halite_build_info is declared %d times:\n%s", got, section(body, "build_info"))
	}
	if !strings.Contains(body, `component="api"`) || !strings.Contains(body, `component="hub"`) {
		t.Errorf("a component was lost in the merge:\n%s", section(body, "build_info"))
	}

	// No metric name may carry two declarations anywhere in the body.
	seen := map[string]int{}
	for _, line := range strings.Split(body, "\n") {
		if rest, ok := strings.CutPrefix(line, "# TYPE "); ok {
			name, _, _ := strings.Cut(rest, " ")
			seen[name]++
		}
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("%s is declared %d times", name, n)
		}
	}
}
