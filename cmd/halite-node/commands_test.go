package main

import (
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

func TestTraverseAllAnswersEveryKeyAsked(t *testing.T) {
	m := value.MapOf(
		"os", "FreeBSD",
		"osrelease", "15.1",
		"nested", value.MapOf("inner", "deep"),
	)

	got := traverseAll(m, []string{"osrelease", "nested:inner", "absent", "os"})

	// Salt's grains.item answers about every key it was given, in the
	// order it was given them. Dropping one silently is how a caller
	// reads the wrong grain and never finds out.
	want := [][2]string{
		{"osrelease", "15.1"},
		{"nested:inner", "deep"},
		{"absent", ""},
		{"os", "FreeBSD"},
	}
	if got.Len() != len(want) {
		t.Fatalf("answered %d keys, asked about %d", got.Len(), len(want))
	}
	for i, w := range want {
		k := got.Keys()[i]
		if k != w[0] {
			t.Errorf("key %d = %q, want %q", i, k, w[0])
			continue
		}
		if v, _ := got.Get(k); v != w[1] {
			t.Errorf("%s = %v, want %q", k, v, w[1])
		}
	}
}
