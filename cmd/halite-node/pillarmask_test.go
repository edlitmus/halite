package main

import (
	"testing"

	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/value"
)

// Pillar output is masked on the way to the screen, as Salt's is.
//
// `pillar items` is the command an operator reaches for while debugging,
// and it used to print every secret the tree carries in clear. Running
// it against a real estate's pillar is how that was found: sixteen live
// credentials on the terminal, against Salt's sixteen `**********`.
//
// The rule is Salt's own, from `salt/utils/secret.py`'s `serial`, which
// it applies at exactly these boundaries: every non-empty string leaf is
// replaced, and numbers, booleans, nulls and empty strings pass through.
// Compared leaf by leaf against a real Salt on the same host, the two
// agreed on every one. See DIVERGENCE.

func TestPillarOutputMasksEveryNonEmptyString(t *testing.T) {
	in := value.MapOf(
		"api_key", "s3cret-value",
		"nested", value.MapOf("bind_pw", "another", "empty", ""),
		"list", []any{"webhook-url", "", int64(7)},
		"port", int64(5432),
		"enabled", true,
		"absent", nil,
	)

	got, ok := maskPillar(in, false).(*value.Map)
	if !ok {
		t.Fatalf("masking returned %T, want a map", maskPillar(in, false))
	}

	// Non-empty strings go.
	if v, _ := got.GetString("api_key"); v != redact.Placeholder {
		t.Errorf("api_key = %v, want %q", v, redact.Placeholder)
	}
	nested, _ := got.GetString("nested")
	nm, ok := nested.(*value.Map)
	if !ok {
		t.Fatalf("nested is %T, want a map", nested)
	}
	if v, _ := nm.GetString("bind_pw"); v != redact.Placeholder {
		t.Errorf("nested bind_pw = %v, want %q", v, redact.Placeholder)
	}

	// Everything Salt passes through, this passes through: an empty
	// string carries nothing, and numbers and booleans are what a tree
	// branches on.
	if v, _ := nm.GetString("empty"); v != "" {
		t.Errorf("an empty string was masked into %v", v)
	}
	if v, _ := got.GetString("port"); v != int64(5432) {
		t.Errorf("port = %v, want the number unchanged", v)
	}
	if v, _ := got.GetString("enabled"); v != true {
		t.Errorf("enabled = %v, want the boolean unchanged", v)
	}
	if v, ok := got.GetString("absent"); !ok || v != nil {
		t.Errorf("a null became %v", v)
	}

	// Lists are walked, and the same rule applies inside them.
	list, _ := got.GetString("list")
	items, ok := list.([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("list = %#v, want three items", list)
	}
	if items[0] != redact.Placeholder {
		t.Errorf("a string in a list was not masked: %v", items[0])
	}
	if items[1] != "" || items[2] != int64(7) {
		t.Errorf("a list's empty string or number was altered: %#v", items)
	}
}

// The keys are the half of the tree the command is actually for: a state
// that cannot find `foxpass:api_key` is debugged by seeing the key
// exist, not by reading it.
func TestPillarMaskingKeepsTheShape(t *testing.T) {
	in := value.MapOf("foxpass", value.MapOf("api_key", "s3cret", "bind_pw", "other"))
	got := maskPillar(in, false).(*value.Map)

	outer, ok := got.GetString("foxpass")
	if !ok {
		t.Fatal("the foxpass key did not survive masking")
	}
	inner, ok := outer.(*value.Map)
	if !ok {
		t.Fatalf("foxpass is %T, want a map", outer)
	}
	for _, k := range []string{"api_key", "bind_pw"} {
		if _, ok := inner.GetString(k); !ok {
			t.Errorf("the %s key did not survive masking", k)
		}
	}
}

// `--reveal` is this build's, because Salt offers no way back and the
// remaining reason to run the command is to check a value. Asking for it
// is the point: the disclosure becomes deliberate.
func TestRevealReturnsThePillarUntouched(t *testing.T) {
	in := value.MapOf("api_key", "s3cret-value")
	got, ok := maskPillar(in, true).(*value.Map)
	if !ok {
		t.Fatalf("reveal returned %T, want a map", maskPillar(in, true))
	}
	if v, _ := got.GetString("api_key"); v != "s3cret-value" {
		t.Errorf("--reveal gave %v, want the real value", v)
	}
}
