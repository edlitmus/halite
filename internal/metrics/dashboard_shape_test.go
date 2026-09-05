package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// dashboardShape is as much of Grafana's schema as these checks read.
type dashboardShape struct {
	Panels     []shapePanel `json:"panels"`
	Templating struct {
		List []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"list"`
	} `json:"templating"`
}

type shapePanel struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	Description string `json:"description"`
	GridPos     struct {
		H, W, X, Y int
	} `json:"gridPos"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
	Panels []shapePanel `json:"panels"`
}

func readDashboard(t *testing.T) dashboardShape {
	t.Helper()
	path := filepath.Join("..", "..", "contrib", "examples", "grafana-dashboard.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var dash dashboardShape
	if err := json.Unmarshal(body, &dash); err != nil {
		t.Fatalf("the example dashboard is not valid JSON, so Grafana would refuse "+
			"the import: %v", err)
	}
	if len(dash.Panels) == 0 {
		t.Fatal("the dashboard has no panels; this check has stopped checking")
	}
	return dash
}

func allPanels(panels []shapePanel) []shapePanel {
	var out []shapePanel
	for _, p := range panels {
		out = append(out, p)
		out = append(out, allPanels(p.Panels)...)
	}
	return out
}

// Grafana keys a panel by its id, and two panels sharing one is a
// dashboard where editing either edits both. It imports without
// complaint and misbehaves afterwards, which is the worst of the two
// failures.
func TestEveryPanelHasItsOwnIdentifier(t *testing.T) {
	seen := map[int]string{}
	for _, p := range allPanels(readDashboard(t).Panels) {
		if p.ID == 0 {
			t.Errorf("the panel %q has no id", p.Title)
			continue
		}
		if other, ok := seen[p.ID]; ok {
			t.Errorf("%q and %q share the id %d", p.Title, other, p.ID)
		}
		seen[p.ID] = p.Title
	}
}

// The grid is twenty-four columns wide and Grafana silently reflows
// anything that does not fit, so a panel laid out past the edge or on
// top of another lands somewhere nobody chose.
func TestNoPanelOverlapsAnother(t *testing.T) {
	dash := readDashboard(t)
	checkLayout(t, "the dashboard", dash.Panels)
	for _, p := range dash.Panels {
		if len(p.Panels) > 0 {
			// A collapsed row lays its own panels out from zero.
			checkLayout(t, p.Title, p.Panels)
		}
	}
}

func checkLayout(t *testing.T, where string, panels []shapePanel) {
	t.Helper()
	type cell struct{ x, y int }
	occupied := map[cell]string{}
	for _, p := range panels {
		g := p.GridPos
		if g.W <= 0 || g.H <= 0 {
			t.Errorf("%s: %q has no size", where, p.Title)
			continue
		}
		if g.X+g.W > 24 {
			t.Errorf("%s: %q runs from column %d for %d, past the twenty-four the grid has",
				where, p.Title, g.X, g.W)
			continue
		}
		for x := g.X; x < g.X+g.W; x++ {
			for y := g.Y; y < g.Y+g.H; y++ {
				if other, ok := occupied[cell{x, y}]; ok {
					t.Errorf("%s: %q overlaps %q at column %d row %d",
						where, p.Title, other, x, y)
					return
				}
				occupied[cell{x, y}] = p.Title
			}
		}
	}
}

// A panel says what a reading means, or it is a graph an operator has
// to guess at during the incident it was drawn for.
func TestEveryPanelSaysWhatItMeans(t *testing.T) {
	for _, p := range allPanels(readDashboard(t).Panels) {
		if p.Type == "row" {
			continue
		}
		if strings.TrimSpace(p.Title) == "" {
			t.Errorf("a %s panel has no title", p.Type)
		}
		if strings.TrimSpace(p.Description) == "" {
			t.Errorf("the panel %q carries no description", p.Title)
		}
		if len(p.Targets) == 0 {
			t.Errorf("the panel %q queries nothing", p.Title)
		}
	}
}

// Every variable a query names has to exist, and every variable that
// exists has to be used. A query naming one that is not declared is
// interpolated to the empty string, which silently matches everything.
func TestTheDashboardVariablesAndTheQueriesAgree(t *testing.T) {
	dash := readDashboard(t)
	declared := map[string]bool{}
	for _, v := range dash.Templating.List {
		declared[v.Name] = true
	}

	named := regexp.MustCompile(`\$(\w+)`)
	used := map[string]bool{}
	for _, p := range allPanels(dash.Panels) {
		for _, target := range p.Targets {
			for _, m := range named.FindAllStringSubmatch(target.Expr, -1) {
				name := m[1]
				if strings.HasPrefix(name, "__") {
					// Grafana's own, `$__rate_interval` and friends.
					continue
				}
				used[name] = true
				if !declared[name] {
					t.Errorf("the panel %q names $%s, which the dashboard does not declare",
						p.Title, name)
				}
			}
		}
	}
	for name := range declared {
		if name == "DS_PROMETHEUS" || used[name] {
			continue
		}
		t.Errorf("$%s is declared and no query uses it, so the picker at the top "+
			"filters nothing", name)
	}
}

// The node families come from the nodes' own scrape job: a node is its
// own target with its own certificate, not something the API's scrape
// carries. A node panel filtered by the API's job variable draws an
// empty graph on every estate.
//
// Only the families no other process records. `halite_beacon_dropped_total`,
// `halite_beacon_events_total` and the two state-duration histograms are
// recorded on both sides deliberately -- the hub's are the estate, the
// node's are one machine -- so a query naming those under either job is
// correct.
func TestNodePanelsUseTheNodeScrapeJob(t *testing.T) {
	nodeOnly := regexp.MustCompile(
		`halite_(node_|ext_|beacon_(rate_limited|failures|queue_depth))`)
	for _, p := range allPanels(readDashboard(t).Panels) {
		for _, target := range p.Targets {
			if !nodeOnly.MatchString(target.Expr) {
				continue
			}
			if strings.Contains(target.Expr, `job="$job"`) {
				t.Errorf("the panel %q reads a node family under the API's scrape job:\n%s",
					p.Title, target.Expr)
			}
			if !strings.Contains(target.Expr, `job="$node_job"`) {
				t.Errorf("the panel %q reads a node family and names no scrape job:\n%s",
					p.Title, target.Expr)
			}
		}
	}
}
