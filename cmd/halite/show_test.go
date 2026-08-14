package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/sls"
)

func samplePlan() []sls.State {
	return []sls.State{
		{
			ID: "install_nginx", Module: "pkg", Fn: "installed",
			Args: map[string]any{"name": "nginx"},
			Src:  "/srv/states/web/init.sls",
		},
		{
			ID: "nginx_conf", Module: "file", Fn: "managed",
			Args:    map[string]any{"name": "/etc/nginx.conf", "mode": "0644"},
			Require: []sls.Ref{{Module: "pkg", ID: "install_nginx"}},
			Src:     "/srv/states/web/init.sls",
		},
		{
			ID: "nginx", Module: "service", Fn: "running",
			Args:  map[string]any{"enable": "true"},
			Watch: []sls.Ref{{Module: "file", ID: "nginx_conf"}},
			Src:   "/srv/states/web/service.sls",
		},
	}
}

func TestPlanIsPrintedInRunOrder(t *testing.T) {
	out := formatPlan(samplePlan(), "/srv/states")

	for i, want := range []string{"1. install_nginx", "2. nginx_conf", "3. nginx"} {
		if !strings.Contains(out, want) {
			t.Fatalf("want %q in the plan (position %d), got:\n%s", want, i+1, out)
		}
	}
	if strings.Index(out, "install_nginx") > strings.Index(out, "nginx_conf") {
		t.Fatal("states should print in the order they would run")
	}
}

func TestPlanShowsFunctionsRequisitesAndSources(t *testing.T) {
	out := formatPlan(samplePlan(), "/srv/states")

	for _, want := range []string{
		"pkg.installed",
		"file.managed",
		"(web/init.sls)",    // relative to the root, not the absolute path
		"(web/service.sls)", //
		"require: pkg:install_nginx",
		"watch: file:nginx_conf",
		"mode: 0644",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("want %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/srv/states/web") {
		t.Fatalf("sources should be relative to the root:\n%s", out)
	}
}

func TestPlanCountsStatesAndFiles(t *testing.T) {
	out := formatPlan(samplePlan(), "/srv/states")
	if !strings.Contains(out, "3 states from 2 sls files") {
		t.Fatalf("want a summary line, got:\n%s", out)
	}
	if got := formatPlan(nil, "/srv/states"); got != "no states\n" {
		t.Fatalf("an empty plan should say so, got %q", got)
	}
	one := formatPlan(samplePlan()[:1], "/srv/states")
	if !strings.Contains(one, "1 state from 1 sls file") {
		t.Fatalf("want singulars, got:\n%s", one)
	}
}

func TestPlanArgumentsAreOrdered(t *testing.T) {
	states := []sls.State{{
		ID: "s", Module: "cmd", Fn: "run",
		Args: map[string]any{"zulu": "1", "alpha": "2", "mike": "3"},
	}}
	out := formatPlan(states, "")
	alpha, mike, zulu := strings.Index(out, "alpha"), strings.Index(out, "mike"), strings.Index(out, "zulu")
	if !(alpha < mike && mike < zulu) {
		t.Fatalf("arguments should sort, so two runs read the same:\n%s", out)
	}
}

func TestPlanFlattensListsAndMaps(t *testing.T) {
	states := []sls.State{{
		ID: "s", Module: "pkg", Fn: "installed",
		Args: map[string]any{
			"pkgs": []any{"vim", "curl"},
			"env":  map[string]any{"PATH": "/bin", "HOME": "/root"},
		},
	}}
	out := formatPlan(states, "")
	if !strings.Contains(out, "pkgs: [vim, curl]") {
		t.Fatalf("want a flattened list, got:\n%s", out)
	}
	if !strings.Contains(out, "env: {HOME: /root, PATH: /bin}") {
		t.Fatalf("want a flattened, sorted map, got:\n%s", out)
	}
}

func TestPlanJSONCarriesTheSameFacts(t *testing.T) {
	b, err := json.Marshal(planJSON(samplePlan(), "/srv/states"))
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want three states, got %d", len(got))
	}
	if got[0]["id"] != "install_nginx" || got[0]["module"] != "pkg" || got[0]["function"] != "installed" {
		t.Fatalf("unexpected first state: %v", got[0])
	}
	if got[0]["order"].(float64) != 1 {
		t.Fatalf("want the run order recorded, got %v", got[0]["order"])
	}
	if got[0]["source"] != "web/init.sls" {
		t.Fatalf("want a relative source, got %v", got[0]["source"])
	}
	require, ok := got[1]["require"].([]any)
	if !ok || len(require) != 1 || require[0] != "pkg:install_nginx" {
		t.Fatalf("want the requisite listed, got %v", got[1]["require"])
	}
	if _, ok := got[0]["require"]; ok {
		t.Fatal("a state with no requisites should not carry an empty one")
	}
}

func TestPlanNamesTheDeclarationAnExpansionCameFrom(t *testing.T) {
	states := []sls.State{{
		ID: "install_tools (vim)", BaseID: "install_tools", Module: "pkg", Fn: "installed",
		Args: map[string]any{"name": "vim"},
	}}
	entry := planJSON(states, "")[0]
	if entry["declared_id"] != "install_tools" {
		t.Fatalf("an expanded state should name its declaration, got %v", entry["declared_id"])
	}
}
