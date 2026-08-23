package value

import (
	"encoding/json"
	"strings"
	"testing"
)

// An ordered map inside anything the standard encoder touches must come
// out as its mapping.
//
// Before this, `encoding/json` saw the unexported entries and the one
// exported field and wrote `{"Pos":{...}}` with the contents gone. The
// transport marshals its request bodies with the standard encoder, so
// every structured argument an operator typed — `run '*' state.apply
// pillar='{"a":1}'` — reached the hub as a position record.
func TestAnOrderedMapMarshalsAsItsMapping(t *testing.T) {
	body := map[string]any{"kwarg": map[string]any{"data": MapOf("version", "1.2")}}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"kwarg":{"data":{"version":"1.2"}}}`
	if string(raw) != want {
		t.Errorf("marshalled as %s", raw)
	}
}

// FromJSON is the boundary between what `encoding/json` produced and
// what a module may see.
func TestFromJSONLiftsAWholeStructure(t *testing.T) {
	var decoded any
	dec := json.NewDecoder(strings.NewReader(`{"b":2,"a":{"n":9007199254740993,"f":1.5},"list":[1,"x"]}`))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}

	lifted, ok := FromJSON(decoded).(*Map)
	if !ok {
		t.Fatalf("FromJSON returned %T", FromJSON(decoded))
	}
	// Ordered by key, because a Go map has no order and a run must not
	// depend on the one it came out in.
	if got := lifted.StringKeys(); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "list" {
		t.Errorf("keys are %v", got)
	}

	inner, _ := lifted.Get("a")
	nested, ok := inner.(*Map)
	if !ok {
		t.Fatalf("a nested object came back as %T", inner)
	}
	if n, _ := nested.Get("n"); n != int64(9007199254740993) {
		t.Errorf("a 64-bit integer came back as %v (%T)", n, n)
	}
	if f, _ := nested.Get("f"); f != 1.5 {
		t.Errorf("a fraction came back as %v (%T)", f, f)
	}
	list, _ := lifted.Get("list")
	items, ok := list.([]any)
	if !ok || len(items) != 2 || items[0] != int64(1) || items[1] != "x" {
		t.Errorf("a list came back as %v", list)
	}
}

// A value already in the model passes through unchanged in meaning.
func TestFromJSONLeavesTheModelAlone(t *testing.T) {
	in := MapOf("a", int64(1), "b", []any{"x"})
	out, ok := FromJSON(in).(*Map)
	if !ok {
		t.Fatalf("FromJSON returned %T", FromJSON(in))
	}
	if got := out.StringKeys(); len(got) != 2 || got[0] != "a" {
		t.Errorf("keys are %v", got)
	}
	if a, _ := out.Get("a"); a != int64(1) {
		t.Errorf("a is %v (%T)", a, a)
	}
}
