package value

import (
	"math"
	"strings"
	"testing"
	"time"
)

// The ordered mapping is the foundation everything else stands on. Its
// order is not a convenience: state run order follows declaration order in
// the file, so a mapping that loses its order loses the run order with it.

func TestMapPreservesDeclarationOrder(t *testing.T) {
	m := NewMap(0)
	for _, k := range []string{"zebra", "apple", "mango", "cherry"} {
		m.Set(k, k)
	}
	want := []string{"zebra", "apple", "mango", "cherry"}
	if got := m.StringKeys(); !equalStrings(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	// Replacing a key keeps its position rather than moving it to the end.
	m.Set("apple", "replaced")
	if got := m.StringKeys(); !equalStrings(got, want) {
		t.Errorf("after replacement keys = %v, want %v", got, want)
	}
	if v, _ := m.Get("apple"); v != "replaced" {
		t.Errorf("apple = %#v", v)
	}
}

func TestMapDeletePreservesTheRest(t *testing.T) {
	m := MapOf("a", 1, "b", 2, "c", 3)
	if !m.Delete("b") {
		t.Error("Delete reported nothing removed")
	}
	if got := m.StringKeys(); !equalStrings(got, []string{"a", "c"}) {
		t.Errorf("keys = %v", got)
	}
	// The index must be rebuilt, or a later lookup finds the wrong entry.
	if v, ok := m.Get("c"); !ok || v != 3 {
		t.Errorf("c = %#v after a delete", v)
	}
	if m.Delete("nope") {
		t.Error("Delete reported removing something that was not there")
	}
}

func TestKeyTypesDoNotCollide(t *testing.T) {
	// `1: a` and `"1": b` are two keys, not one silently overwritten.
	m := NewMap(0)
	m.Set(int64(1), "int")
	m.Set("1", "string")
	m.Set(true, "bool")
	m.Set("true", "string true")
	m.Set(nil, "null")

	if m.Len() != 5 {
		t.Fatalf("len = %d, want 5 distinct keys: %v", m.Len(), m.StringKeys())
	}
	if v, _ := m.Get(int64(1)); v != "int" {
		t.Errorf("int key = %#v", v)
	}
	if v, _ := m.Get("1"); v != "string" {
		t.Errorf("string key = %#v", v)
	}
	if v, _ := m.Get(true); v != "bool" {
		t.Errorf("bool key = %#v", v)
	}
	if v, _ := m.Get(nil); v != "null" {
		t.Errorf("null key = %#v", v)
	}
}

func TestMapEntryCarriesPositions(t *testing.T) {
	m := NewMap(0)
	pos := Pos{File: "web.sls", Line: 3, Col: 1}
	m.SetAt("k", "v", pos, Pos{File: "web.sls", Line: 3, Col: 4})

	e, ok := m.Entry("k")
	if !ok {
		t.Fatal("Entry missed a key it holds")
	}
	if e.KeyPos != pos {
		t.Errorf("key position = %v", e.KeyPos)
	}
	// A replacement with a zero position leaves the original alone, so a
	// merge does not move a diagnostic.
	m.SetAt("k", "v2", Pos{}, Pos{})
	e, _ = m.Entry("k")
	if e.KeyPos != pos {
		t.Errorf("a zero position overwrote the original: %v", e.KeyPos)
	}
	if e.Val != "v2" {
		t.Errorf("value = %#v", e.Val)
	}
}

func TestDeepCopyIsIndependent(t *testing.T) {
	orig := MapOf(
		"scalar", "x",
		"list", []any{"a", MapOf("nested", 1)},
		"map", MapOf("inner", []any{1, 2}),
		"bytes", []byte("data"),
	)
	copy := Deep(orig).(*Map)

	// Mutating the copy at any depth must not reach the original.
	copy.Set("scalar", "changed")
	copy.Get("list")
	list, _ := copy.Get("list")
	list.([]any)[0] = "changed"
	inner, _ := list.([]any)[1].(*Map)
	inner.Set("nested", 99)
	sub, _ := copy.Get("map")
	sub.(*Map).Set("inner", "changed")
	b, _ := copy.Get("bytes")
	b.([]byte)[0] = 'X'

	if v, _ := orig.Get("scalar"); v != "x" {
		t.Errorf("scalar leaked: %#v", v)
	}
	ol, _ := orig.Get("list")
	if ol.([]any)[0] != "a" {
		t.Errorf("list leaked: %#v", ol)
	}
	if n, _ := ol.([]any)[1].(*Map).Get("nested"); n != 1 {
		t.Errorf("nested map leaked: %#v", n)
	}
	os, _ := orig.Get("map")
	if _, ok := os.(*Map).Get("inner"); !ok {
		t.Error("nested mapping leaked")
	}
	ob, _ := orig.Get("bytes")
	if string(ob.([]byte)) != "data" {
		t.Errorf("bytes leaked: %s", ob)
	}
	// A nil map deep-copies to a nil map rather than panicking.
	if got := Deep((*Map)(nil)); got.(*Map) != nil {
		t.Error("Deep of a nil map should stay nil")
	}
}

func TestCloneIsShallow(t *testing.T) {
	orig := MapOf("m", MapOf("k", "v"))
	c := orig.Clone()
	c.Set("added", 1)
	if orig.Has("added") {
		t.Error("Clone shares its entry list")
	}
	// The values are shared, which is what makes Clone cheap and Deep
	// necessary.
	sub, _ := c.Get("m")
	sub.(*Map).Set("k", "changed")
	origSub, _ := orig.Get("m")
	if v, _ := origSub.(*Map).Get("k"); v != "changed" {
		t.Error("Clone unexpectedly deep-copied")
	}
}

func TestTruthyFollowsPython(t *testing.T) {
	falsey := []any{nil, false, int64(0), 0.0, "", []byte{}, []any{}, NewMap(0), int(0), math.NaN()}
	for _, v := range falsey {
		if Truthy(v) {
			t.Errorf("Truthy(%#v) = true", v)
		}
	}
	truthy := []any{true, int64(1), -1.5, "x", []byte("x"), []any{nil}, MapOf("a", 1), int(2), time.Now()}
	for _, v := range truthy {
		if !Truthy(v) {
			t.Errorf("Truthy(%#v) = false", v)
		}
	}
}

func TestTraverse(t *testing.T) {
	root := MapOf(
		"a", MapOf("b", MapOf("c", "deep")),
		"list", []any{"zero", MapOf("k", "v")},
	)
	cases := map[string]any{
		"a:b:c":    "deep",
		"list:0":   "zero",
		"list:1:k": "v",
		"":         root,
	}
	for path, want := range cases {
		got, ok := Traverse(root, path, ":")
		if !ok {
			t.Errorf("%q did not resolve", path)
			continue
		}
		if path != "" && got != want {
			t.Errorf("%q = %#v, want %#v", path, got, want)
		}
	}
	for _, path := range []string{"a:nope", "a:b:c:deeper", "list:9", "list:-1", "list:notanumber", "nope"} {
		if _, ok := Traverse(root, path, ":"); ok {
			t.Errorf("%q should not resolve", path)
		}
	}
	// An alternate delimiter, which matters for a key containing a colon.
	alt := MapOf("a:b", MapOf("c", "found"))
	if got, ok := Traverse(alt, "a:b/c", "/"); !ok || got != "found" {
		t.Errorf("alternate delimiter = %#v %v", got, ok)
	}
}

func TestKindAndTypeNames(t *testing.T) {
	cases := []struct {
		v    any
		kind Kind
		name string
	}{
		{nil, KindNull, "null"},
		{true, KindBool, "bool"},
		{int64(1), KindInt, "int"},
		{1.5, KindFloat, "float"},
		{"s", KindString, "string"},
		{[]byte("b"), KindBinary, "binary"},
		{time.Now(), KindTimestamp, "timestamp"},
		{[]any{}, KindSeq, "sequence"},
		{NewMap(0), KindMap, "mapping"},
	}
	for _, c := range cases {
		if got := KindOf(c.v); got != c.kind {
			t.Errorf("KindOf(%#v) = %v, want %v", c.v, got, c.kind)
		}
		if got := TypeName(c.v); got != c.name {
			t.Errorf("TypeName(%#v) = %q, want %q", c.v, got, c.name)
		}
	}
	// Something outside the nine types reports its Go type, which is a
	// programming error in a caller rather than data.
	if got := TypeName(struct{}{}); !strings.Contains(got, "struct") {
		t.Errorf("TypeName of an outside type = %q", got)
	}
	if KindOf(struct{}{}) >= 0 {
		t.Error("an outside type should not classify as one of the nine")
	}
}

func TestKeyStringRendering(t *testing.T) {
	cases := map[any]string{
		"s": "s", int64(3): "3", 1.5: "1.5", true: "true", nil: "null",
	}
	for k, want := range cases {
		if got := KeyString(k); got != want {
			t.Errorf("KeyString(%#v) = %q, want %q", k, got, want)
		}
	}
}

// ---- merge, SPEC section 12.3 ----

func TestMergeStrategies(t *testing.T) {
	dst := MapOf("nested", MapOf("a", 1, "b", 2), "list", []any{"x"}, "scalar", "old")
	src := MapOf("nested", MapOf("b", 22, "c", 3), "list", []any{"y"}, "scalar", "new")

	// smart and recurse deep merge mappings and replace lists.
	for _, s := range []Strategy{Smart, Recurse} {
		out := Merge(dst, src, MergeOpts{Strategy: s}).(*Map)
		if v, _ := Traverse(out, "nested:a", ":"); v != 1 {
			t.Errorf("%v nested:a = %#v", s, v)
		}
		if v, _ := Traverse(out, "nested:b", ":"); v != 22 {
			t.Errorf("%v nested:b = %#v", s, v)
		}
		if v, _ := out.Get("list"); len(v.([]any)) != 1 {
			t.Errorf("%v list = %#v; lists replace by default", s, v)
		}
		if v, _ := out.Get("scalar"); v != "new" {
			t.Errorf("%v scalar = %#v", s, v)
		}
	}

	// merge_lists concatenates.
	out := Merge(dst, src, MergeOpts{Strategy: Recurse, MergeLists: true}).(*Map)
	if v, _ := out.Get("list"); len(v.([]any)) != 2 {
		t.Errorf("with MergeLists list = %#v", v)
	}

	// aggregate concatenates and still deep merges.
	out = Merge(dst, src, MergeOpts{Strategy: Aggregate}).(*Map)
	if v, _ := out.Get("list"); len(v.([]any)) != 2 {
		t.Errorf("aggregate list = %#v", v)
	}
	if v, _ := Traverse(out, "nested:a", ":"); v != 1 {
		t.Errorf("aggregate nested:a = %#v", v)
	}

	// overwrite replaces a top-level key wholesale, without recursing.
	out = Merge(dst, src, MergeOpts{Strategy: Overwrite}).(*Map)
	if _, ok := Traverse(out, "nested:a", ":"); ok {
		t.Error("overwrite should replace the whole nested mapping")
	}
}

func TestMergeDoesNotMutateItsInputs(t *testing.T) {
	dst := MapOf("nested", MapOf("a", 1))
	src := MapOf("nested", MapOf("b", 2))
	Merge(dst, src, MergeOpts{Strategy: Recurse})

	if dst.Len() != 1 {
		t.Errorf("dst gained keys: %v", dst.StringKeys())
	}
	sub, _ := dst.Get("nested")
	if sub.(*Map).Has("b") {
		t.Error("merge mutated the destination's nested mapping")
	}
	srcSub, _ := src.Get("nested")
	if srcSub.(*Map).Has("a") {
		t.Error("merge mutated the source's nested mapping")
	}
}

func TestMergeDirectives(t *testing.T) {
	dst := MapOf("nested", MapOf("keep", 1, "replace", 2), "list", []any{"x"})

	// __replace__ discards the destination's mapping entirely.
	src := MapOf("nested", MapOf("__replace__", true, "only", 9))
	out := Merge(dst, src, MergeOpts{Strategy: Recurse}).(*Map)
	sub, _ := out.Get("nested")
	if sub.(*Map).Has("keep") {
		t.Errorf("__replace__ did not discard the destination: %v", sub.(*Map).StringKeys())
	}
	if !sub.(*Map).Has("only") {
		t.Error("__replace__ dropped the source's own keys")
	}
	// The directive itself never survives into the result.
	if sub.(*Map).Has("__replace__") {
		t.Error("the directive leaked into the merged data")
	}

	// __aggregate__ turns on list concatenation for that mapping.
	dst = MapOf("inner", MapOf("list", []any{"x"}))
	src = MapOf("inner", MapOf("__aggregate__", true, "list", []any{"y"}))
	out = Merge(dst, src, MergeOpts{Strategy: Recurse}).(*Map)
	got, _ := Traverse(out, "inner:list", ":")
	if len(got.([]any)) != 2 {
		t.Errorf("__aggregate__ list = %#v", got)
	}

	// A directive set false is consumed but changes nothing.
	dst = MapOf("nested", MapOf("keep", 1))
	src = MapOf("nested", MapOf("__replace__", false, "added", 2))
	out = Merge(dst, src, MergeOpts{Strategy: Recurse}).(*Map)
	sub, _ = out.Get("nested")
	if !sub.(*Map).Has("keep") || !sub.(*Map).Has("added") {
		t.Errorf("a false directive changed the merge: %v", sub.(*Map).StringKeys())
	}
	if sub.(*Map).Has("__replace__") {
		t.Error("the directive leaked into the merged data")
	}
}

func TestMergeNonMappings(t *testing.T) {
	// A source that is not a mapping replaces whatever is there.
	if got := Merge(MapOf("a", 1), "scalar", MergeOpts{}); got != "scalar" {
		t.Errorf("merge with a scalar source = %#v", got)
	}
	// A null source replaces a value, which is how a pillar layer clears
	// a key rather than being ignored.
	if got := Merge("old", nil, MergeOpts{}); got != nil {
		t.Errorf("merge with a null source = %#v", got)
	}
}

func TestMergeDepthIsBounded(t *testing.T) {
	// A pathological pillar tree should not blow the stack.
	deep := func() *Map {
		root := NewMap(1)
		cur := root
		for i := 0; i < 300; i++ {
			next := NewMap(1)
			cur.Set("n", next)
			cur = next
		}
		return root
	}
	out := Merge(deep(), deep(), MergeOpts{Strategy: Recurse})
	if out == nil {
		t.Error("a deep merge returned nothing")
	}
}

func TestParseStrategy(t *testing.T) {
	for name, want := range map[string]Strategy{
		"": Smart, "smart": Smart, "recurse": Recurse,
		"aggregate": Aggregate, "overwrite": Overwrite,
	} {
		got, ok := ParseStrategy(name)
		if !ok || got != want {
			t.Errorf("ParseStrategy(%q) = %v %v", name, got, ok)
		}
		if got.String() != want.String() {
			t.Errorf("String round trip for %q", name)
		}
	}
	if _, ok := ParseStrategy("nonsense"); ok {
		t.Error("an unknown strategy should not resolve")
	}
}

// ---- JSON, SPEC section 6.4 ----

func TestJSONIntegersSurviveARoundTrip(t *testing.T) {
	// The known hazard: a 64-bit grain such as mem_total in bytes, or a
	// package epoch, must not be mangled through float64.
	const big = int64(9007199254740993) // 2^53 + 1, which float64 cannot hold
	src := MapOf("mem_total", big, "epoch", int64(3), "ratio", 1.5)

	b, err := EncodeJSON(src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "9007199254740993") {
		t.Errorf("the integer was mangled on encode: %s", b)
	}

	back, err := DecodeJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := back.(*Map).Get("mem_total")
	if got != big {
		t.Errorf("mem_total = %#v (%T), want %d", got, got, big)
	}
	if r, _ := back.(*Map).Get("ratio"); r != 1.5 {
		t.Errorf("ratio = %#v", r)
	}
}

func TestJSONKeepsObjectOrder(t *testing.T) {
	src := MapOf("zebra", 1, "apple", 2, "mango", 3)
	b, err := EncodeJSON(src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"zebra":1,"apple":2,"mango":3}` {
		t.Errorf("encode lost the order: %s", b)
	}
	back, err := DecodeJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.(*Map).StringKeys(); !equalStrings(got, []string{"zebra", "apple", "mango"}) {
		t.Errorf("decode lost the order: %v", got)
	}
}

func TestJSONIndentAndNesting(t *testing.T) {
	src := MapOf("a", MapOf("b", []any{1, "two"}), "empty_map", NewMap(0), "empty_list", []any{})
	b, err := EncodeJSON(src, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\n  ") {
		t.Errorf("indent was not applied:\n%s", b)
	}
	back, err := DecodeJSON(b)
	if err != nil {
		t.Fatalf("indented output did not decode: %v\n%s", err, b)
	}
	if got, _ := Traverse(back, "a:b:1", ":"); got != "two" {
		t.Errorf("nested value = %#v", got)
	}
}

func TestJSONDoesNotEscapeHTML(t *testing.T) {
	// HTML escaping would corrupt a URL or a shell fragment held in
	// pillar, which is data rather than markup.
	b, err := EncodeJSON(MapOf("url", "https://host/?a=1&b=2"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `\u0026`) {
		t.Errorf("the ampersand was escaped: %s", b)
	}
	if !strings.Contains(string(b), "a=1&b=2") {
		t.Errorf("the query string did not survive: %s", b)
	}
}

func TestJSONRejectsMalformedInput(t *testing.T) {
	cases := []string{
		`{"a": 1`,
		`{"a": 1} trailing`,
		`{"a": 1, "a": 2}`,
		``,
	}
	for _, src := range cases {
		if _, err := DecodeJSON([]byte(src)); err == nil {
			t.Errorf("%q should not decode", src)
		}
	}
}

func TestJSONScalarsAndArrays(t *testing.T) {
	for _, src := range []string{`"just a string"`, `42`, `true`, `null`, `[1,2,3]`} {
		if _, err := DecodeJSON([]byte(src)); err != nil {
			t.Errorf("%s did not decode: %v", src, err)
		}
	}
	b, err := EncodeJSON([]any{int64(1), "two", nil, true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `[1,"two",null,true]` {
		t.Errorf("array encode = %s", b)
	}
}

func TestSortedKeys(t *testing.T) {
	m := MapOf("zebra", 1, "apple", 2)
	if got := m.SortedKeys(); !equalStrings(got, []string{"apple", "zebra"}) {
		t.Errorf("SortedKeys = %v", got)
	}
	// The declaration order is untouched by asking for the sorted one.
	if got := m.StringKeys(); !equalStrings(got, []string{"zebra", "apple"}) {
		t.Errorf("SortedKeys disturbed the declaration order: %v", got)
	}
}

func TestNilMapIsUsable(t *testing.T) {
	var m *Map
	if m.Len() != 0 || m.Keys() != nil || m.StringKeys() != nil || m.Entries() != nil {
		t.Error("a nil map should read as empty")
	}
	if _, ok := m.Get("k"); ok {
		t.Error("a nil map should hold nothing")
	}
	if m.Has("k") {
		t.Error("a nil map should hold nothing")
	}
	if _, ok := m.Entry("k"); ok {
		t.Error("a nil map should hold nothing")
	}
	if m.Delete("k") {
		t.Error("a nil map should delete nothing")
	}
	if m.Clone() != nil {
		t.Error("cloning a nil map should stay nil")
	}
}

func TestMapOfPanicsOnAnOddCount(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MapOf with an odd argument count should panic; it can only be a bug at the call site")
		}
	}()
	MapOf("a", 1, "b")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTraverseSearchesMappingsInsideALisT(t *testing.T) {
	// Salt's colon traversal descends into a list by searching the
	// mappings inside it for the key, and a pillar written as a list of
	// single-key mappings depends on it. Without this, `pillar.get`
	// returned nothing and the template rendered the empty value into a
	// state argument — an account with no password rather than an error.
	root := MapOf("users", MapOf("ed", []any{
		MapOf("password", "hashed"),
		MapOf("shell", "/bin/sh"),
		MapOf("keys", []any{"first", "second"}),
	}))

	cases := map[string]any{
		"users:ed:password":   "hashed",
		"users:ed:shell":      "/bin/sh",
		"users:ed:keys:1":     "second",
		"users:ed:0:password": "hashed",
	}
	for path, want := range cases {
		got, ok := Traverse(root, path, ":")
		if !ok || got != want {
			t.Errorf("Traverse(%q) = %#v, %v; want %#v", path, got, ok, want)
		}
	}

	// A key no mapping in the list has is still absent, rather than the
	// first mapping's value or a panic.
	if v, ok := Traverse(root, "users:ed:absent", ":"); ok {
		t.Errorf("an absent key resolved to %#v", v)
	}
	// An index out of range stays out of range.
	if _, ok := Traverse(root, "users:ed:9", ":"); ok {
		t.Error("an index past the end resolved")
	}
}
