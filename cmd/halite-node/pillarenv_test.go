package main

import (
	"encoding/json"
	"testing"
)

// A node with no pillarenv asks the hub for every environment's pillar;
// one that names an environment asks for that one. The probe and every
// later fetch build their request here. DIVERGENCE 5.286.
func TestTheNodeAsksForEveryEnvironmentWithoutAPillarenv(t *testing.T) {
	n := &node{nodeID: "web1.example"}
	grains := json.RawMessage(`{}`)
	if req := n.pillarRequest("base", true, grains); !req.AllEnvs || req.Env != "base" || req.NodeID != "web1.example" {
		t.Errorf("no pillarenv: %+v", req)
	}
	if req := n.pillarRequest("ref", false, grains); req.AllEnvs || req.Env != "ref" {
		t.Errorf("pillarenv ref: %+v", req)
	}
}
