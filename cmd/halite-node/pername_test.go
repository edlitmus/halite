package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Arguments given to one name under `names` are that name's: an option
// among them gates that chunk alone, and a requisite orders it alone,
// overriding the declaration's as Salt's live.update() does. They all
// went to the module, so a per-name `unless` failed to compile as "not a
// parameter of this function". A reverse requisite, which attaches to
// another declaration, is refused with a reason. DIVERGENCE 5.270.
func TestPerNameArgumentsAreOptionsAndRequisites(t *testing.T) {
	flags := tree(t, map[string]string{"p.sls": `
per name:
  cmd.run:
    - unless: 'false'
    - names:
      - echo gated:
        - unless: 'true'
      - echo after last:
        - require:
          - cmd: last
      - echo plain
last:
  cmd.run:
    - name: echo last
`, "bad.sls": `
x:
  cmd.run:
    - names:
      - echo y:
        - require_in:
          - cmd: x
`})

	// The gate: the name with its own `unless: 'true'` is held back, and
	// the declaration's `unless: 'false'` still applies to the others.
	got := run(t, append([]string{"state", "sls", "p"}, flags...)...)
	if got.code != 0 {
		t.Fatalf("%+v", got)
	}
	blocks := strings.Split(got.stdout, "----------")
	comment := func(name string) string {
		for _, b := range blocks {
			if strings.Contains(b, "Name: "+name+"\n") {
				return b
			}
		}
		t.Fatalf("no block for %q:\n%s", name, got.stdout)
		return ""
	}
	if b := comment("echo gated"); !strings.Contains(b, "unless condition was met") {
		t.Errorf("the per-name unless did not gate its chunk:\n%s", b)
	}
	if b := comment("echo plain"); !strings.Contains(b, "ran.") {
		t.Errorf("the per-name unless gated a chunk it was not given to:\n%s", b)
	}

	// The requisite: `last` is declared after, and only the per-name
	// require brings it before that one name.
	low := run(t, append([]string{"state", "show_lowstate", "p", "--out", "json"}, flags...)...)
	var chunks []map[string]any
	if err := json.Unmarshal([]byte(low.stdout), &chunks); err != nil {
		t.Fatalf("%v\n%s", err, low.stdout)
	}
	at := map[string]float64{}
	for _, c := range chunks {
		at[c["name"].(string)] = c["__run_num__"].(float64)
	}
	if !(at["echo last"] < at["echo after last"]) {
		t.Errorf("the per-name require did not order its chunk after `last`: %v", at)
	}
	if !(at["echo plain"] < at["echo last"]) {
		t.Errorf("the per-name require reordered a chunk it was not given to: %v", at)
	}

	bad := run(t, append([]string{"state", "show_lowstate", "bad"}, flags...)...)
	if bad.code == 0 || !strings.Contains(bad.stderr, "`require_in` cannot be given to one name") {
		t.Errorf("a per-name require_in: %+v", bad)
	}
}
