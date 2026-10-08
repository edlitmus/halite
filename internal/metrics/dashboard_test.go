package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// dashboardPanel is as much of Grafana's schema as this check reads.
type dashboardPanel struct {
	Title   string `json:"title"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
	Panels []dashboardPanel `json:"panels"`
}

type dashboardFile struct {
	Panels     []dashboardPanel `json:"panels"`
	Templating struct {
		List []struct {
			Name  string          `json:"name"`
			Query json.RawMessage `json:"query"`
		} `json:"list"`
	} `json:"templating"`
}

// TestDashboardQueriesNameRegisteredMetrics holds the example Grafana
// dashboard to querying metrics this build can actually expose.
//
// A panel written against a family that does not exist does not error:
// it draws an empty graph, which is indistinguishable from a fleet with
// nothing happening in it. That is the same failure the documented
// alerts have, and it is why they are checked the same way.
//
// Only the queries are read. A description may name a family in prose —
// several explain what is missing when a grant is absent — and prose is
// not a claim that the metric is registered.
func TestDashboardQueriesNameRegisteredMetrics(t *testing.T) {
	registered := registeredFamilies(t)

	path := filepath.Join("..", "..", "contrib", "examples", "grafana-dashboard.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var dash dashboardFile
	if err := json.Unmarshal(body, &dash); err != nil {
		t.Fatalf("the example dashboard is not valid JSON, so Grafana would "+
			"refuse the import: %v", err)
	}

	queries := collectQueries(dash.Panels)
	for _, variable := range dash.Templating.List {
		// A variable's query is a bare string in the older form and an
		// object in the current one.
		var asString string
		if json.Unmarshal(variable.Query, &asString) == nil {
			queries = append(queries, asString)
			continue
		}
		var asObject struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(variable.Query, &asObject) == nil {
			queries = append(queries, asObject.Query)
		}
	}
	if len(queries) == 0 {
		t.Fatal("no queries were found in the dashboard; this check has stopped checking")
	}

	named := regexp.MustCompile(`\bhalite_[a-z0-9_]+`)
	checked := 0
	for _, query := range queries {
		for _, name := range named.FindAllString(query, -1) {
			checked++
			// A histogram is registered under its family name and
			// queried through the series Prometheus derives from it.
			base := strings.TrimSuffix(strings.TrimSuffix(
				strings.TrimSuffix(name, "_bucket"), "_sum"), "_count")
			if !registered[name] && !registered[base] {
				t.Errorf("the example dashboard queries %s, which this build "+
					"never registers; the panel would draw an empty graph", name)
			}
		}
	}
	t.Logf("checked %d metric references across %d queries", checked, len(queries))
}

func collectQueries(panels []dashboardPanel) []string {
	var out []string
	for _, panel := range panels {
		for _, target := range panel.Targets {
			if target.Expr != "" {
				out = append(out, target.Expr)
			}
		}
		out = append(out, collectQueries(panel.Panels)...)
	}
	return out
}

// notOnTheDashboard names registered families that are deliberately not on
// the example dashboard, each with the reason. It starts empty: the
// judgement is "would an operator read this on a graph", and every family
// registered so far is one they would. An entry is a decision, made in
// the change that adds the family, and not a way to make this test pass.
var notOnTheDashboard = map[string]string{}

// TestEveryRegisteredFamilyIsOnTheDashboard holds the example dashboard to
// the metrics this build exposes.
//
// TestDashboardQueriesNameRegisteredMetrics catches a panel over a family
// that is not there. This is the other direction, and it was needed: eight
// families, two of them the evidence ones added that same day, had no
// panel, and what found them was a sweep by a script and not the change
// that added the metrics. The rule is that metrics work updates the
// dashboard in the same change; this is what makes the rule a failing
// test instead of something remembered.
//
// Registered families and not the rows of docs/metrics.md, because the
// first version read the tables and missed halite_reactor_queue_depth,
// which is documented in prose; TestEveryRegisteredMetricIsDocumented
// already requires every registered family to be documented somewhere, so
// this one does not need to find the documentation.
func TestEveryRegisteredFamilyIsOnTheDashboard(t *testing.T) {
	registered := registeredFamilies(t)
	if len(registered) < 60 {
		t.Fatalf("found only %d registered families; this check has stopped checking", len(registered))
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "contrib", "examples", "grafana-dashboard.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dash dashboardFile
	if err := json.Unmarshal(raw, &dash); err != nil {
		t.Fatal(err)
	}
	onDashboard := map[string]bool{}
	named := regexp.MustCompile(`\bhalite_[a-z0-9_]+`)
	for _, query := range collectQueries(dash.Panels) {
		for _, name := range named.FindAllString(query, -1) {
			// A histogram is queried through the series Prometheus
			// derives from its family name.
			onDashboard[strings.TrimSuffix(strings.TrimSuffix(
				strings.TrimSuffix(name, "_bucket"), "_sum"), "_count")] = true
		}
	}

	for family := range registered {
		if onDashboard[family] {
			continue
		}
		if why, ok := notOnTheDashboard[family]; ok {
			t.Logf("%s is not on the dashboard, deliberately: %s", family, why)
			continue
		}
		t.Errorf("%s is registered and has no panel on the example dashboard. Add one to "+
			"contrib/examples/grafana-dashboard.json in this change (and bump its version), "+
			"or name it in notOnTheDashboard with the reason an operator would never graph it", family)
	}
	for family, why := range notOnTheDashboard {
		if why == "" {
			t.Errorf("notOnTheDashboard[%s] has no reason", family)
		}
		if !registered[family] {
			t.Errorf("notOnTheDashboard names %s, which this build does not register; "+
				"remove the entry", family)
		}
		if onDashboard[family] {
			t.Errorf("notOnTheDashboard names %s, which now has a panel; remove the entry", family)
		}
	}
	t.Logf("%d registered families, %d queried by the dashboard", len(registered), len(onDashboard))
}
