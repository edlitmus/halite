package value

import "testing"

// nest wraps leaf in n single-key maps named "k".
func nest(n int, leaf any) any {
	v := leaf
	for i := 0; i < n; i++ {
		v = MapOf("k", v)
	}
	return v
}

// descend walks n levels down through "k" and returns what is there.
func descend(v any, n int) any {
	for i := 0; i < n; i++ {
		v, _ = v.(*Map).Get("k")
	}
	return v
}

// Two trees built by a template rather than read from a file are not
// bounded by the parser's nesting limit. Merging them 120 levels down
// used to hand back src there, so every key dst held at that depth was
// lost without a word.
func TestMergeKeepsBothSidesBelowTheParsersDepth(t *testing.T) {
	dst := nest(120, MapOf("from_dst", 1))
	src := nest(120, MapOf("from_src", 2))
	got := descend(Merge(dst, src, MergeOpts{Strategy: Recurse}), 120).(*Map)
	a, _ := got.Get("from_dst")
	b, _ := got.Get("from_src")
	if a != 1 || b != 2 {
		t.Errorf("a merge 120 levels deep dropped a side: %v", got.Keys())
	}
}
