package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// `node_grain` with no `node_grain_allow` is refused (issue 622). It
// used to mean "any secret", as Salt's module did, so any node could
// have the hub fetch any secret its credentials can read into that
// node's own pillar, by setting a grain. The refusal names both keys
// and what to set, and is made before anything is fetched -- including
// for a node that names nothing, because the block is wrong whichever
// node it is compiled for.
func TestANodeGrainWithoutAnAllowListIsRefused(t *testing.T) {
	f := newFakeSecrets(t, map[string]string{"hub/db": "hunter22", "any/thing": "v"})
	block := `{"secrets":[{"name":"db","secret_id":"hub/db"}],"node_grain":"wants"}`
	for name, grains := range map[string]string{
		"a node that names a secret": `{"wants":[{"name":"k","secret_id":"any/thing","region":"us-east-1"}]}`,
		"a node that names nothing":  `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := f.call(t, block, grains, "")
			if err == nil {
				t.Fatalf("the block was accepted, and produced %v", out)
			}
			for _, want := range []string{"`node_grain`", "`node_grain_allow`", `"wants"`, "remove `node_grain`"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %s: %v", want, err)
				}
			}
		})
	}
	if len(f.calls) != 0 {
		t.Errorf("Secrets Manager was called %d time(s) for a refused block", len(f.calls))
	}

	// An empty list written out is the same as none.
	if _, err := parseConfig(json.RawMessage(`{"node_grain":"wants","node_grain_allow":[]}`)); err == nil {
		t.Error("an empty node_grain_allow was accepted")
	}
	// And the list alone, with no grain to bound, is harmless and kept.
	if _, err := parseConfig(json.RawMessage(`{"node_grain_allow":["vmop/*"]}`)); err != nil {
		t.Errorf("an allow list with no node_grain was refused: %v", err)
	}
	// `allowed` itself no longer reads an empty list as "any": a Config
	// built some other way than parseConfig does not reopen the hole.
	c := &Config{NodeGrain: "wants"}
	if err := c.allowed("any/thing"); err == nil {
		t.Error("an empty allow list permitted a node-named secret")
	}
}
