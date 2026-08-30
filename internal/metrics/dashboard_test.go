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
