package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// show_lowstate prints a state's runner options as the tree wrote them,
// as Salt's low chunk carries them. It printed none: the compiler takes
// unless, onlyif, creates and the rest out of the module's arguments to
// act on them, and the rendering read only what was left, so a gated
// state read as unconditional. runas_password is masked, being the one
// that is a secret. DIVERGENCE 5.266.
func TestShowLowstateCarriesTheRunnerOptions(t *testing.T) {
	flags := tree(t, map[string]string{"g.sls": `
gated:
  cmd.run:
    - name: echo hi
    - unless: test -f /nonexistent
    - onlyif: 'true'
    - creates: /tmp/halite-never
    - retry:
        attempts: 2
    - runas_password: hunter2
expanded:
  cmd.run:
    - names:
      - echo a
      - echo b
    - creates: /tmp/halite-never
`})
	got := run(t, append([]string{"state", "show_lowstate", "g", "--out", "json"}, flags...)...)
	if got.code != 0 {
		t.Fatalf("%+v", got)
	}
	if strings.Contains(got.stdout, "hunter2") {
		t.Errorf("show_lowstate printed runas_password:\n%s", got.stdout)
	}
	var chunks []map[string]any
	if err := json.Unmarshal([]byte(got.stdout), &chunks); err != nil {
		t.Fatalf("%v\n%s", err, got.stdout)
	}
	byName := map[string]map[string]any{}
	for _, c := range chunks {
		byName[c["name"].(string)] = c
	}
	gated := byName["echo hi"]
	for k, want := range map[string]any{
		"unless": "test -f /nonexistent", "onlyif": "true", "creates": "/tmp/halite-never",
		"runas_password": "**********",
	} {
		if gated[k] != want {
			t.Errorf("gated: %s = %#v, want %#v", k, gated[k], want)
		}
	}
	if retry, ok := gated["retry"].(map[string]any); !ok || retry["attempts"] != float64(2) {
		t.Errorf("gated: retry = %#v", gated["retry"])
	}
	// Each chunk `names` expands to carries the options it was given.
	for _, n := range []string{"echo a", "echo b"} {
		if byName[n]["creates"] != "/tmp/halite-never" {
			t.Errorf("%s: creates = %#v, want it on every expanded chunk", n, byName[n]["creates"])
		}
	}
}
