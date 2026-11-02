package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// One grain that cannot be encoded does not take the others with it.
//
// `grainsJSON` encoded the whole mapping and, when that failed, logged a
// warning that named nothing and sent no grains. A NaN from one probe cost the
// hub all of the node's facts, or, on the refresh, never told it the new ones.
// DIVERGENCE 5.534.
func TestOneGrainThatCannotBeEncodedIsLeftOutAndNamed(t *testing.T) {
	m := value.NewMap(3)
	m.Set("os", "FreeBSD")
	m.Set("load_ratio", math.NaN())
	m.Set("num_cpus", int64(8))

	var warned []string
	raw := encodeGrains(m, func(msg string, kv ...any) {
		warned = append(warned, msg+" "+fmt.Sprint(kv...))
	})
	if raw == nil {
		t.Fatal("no grains were sent because one could not be encoded")
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["os"] != "FreeBSD" || got["num_cpus"] != float64(8) {
		t.Errorf("the grains that could be encoded were lost: %s", raw)
	}
	if _, ok := got["load_ratio"]; ok {
		t.Errorf("a grain with no JSON form was sent: %s", raw)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "load_ratio") {
		t.Errorf("the warning does not name the grain: %v", warned)
	}
}

// Grains that all encode produce no warning and the same bytes as before.
func TestGrainsThatEncodeAreUnchanged(t *testing.T) {
	m := value.NewMap(2)
	m.Set("id", "web1")
	m.Set("big", int64(9007199254740993))
	raw := encodeGrains(m, func(string, ...any) { t.Error("warned for grains that encode") })
	if string(raw) != `{"id":"web1","big":9007199254740993}` {
		t.Errorf("%s", raw)
	}
}
