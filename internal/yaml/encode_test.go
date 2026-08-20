package yaml

import (
	"reflect"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

func TestEncodeQuotesWhatWouldChangeType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"yes", `"yes"`},
		{"true", `"true"`},
		{"0644", `"0644"`},
		{"42", `"42"`},
		{"1.5", `"1.5"`},
		{"", `""`},
		{"has: colon", `"has: colon"`},
		{"# hash", `"# hash"`},
		{"trailing ", `"trailing "`},
		{"1:30", `"1:30"`},
		{"nginx-1.0", "nginx-1.0"},
	}
	for _, c := range cases {
		if got := EncodeScalar(c.in); got != c.want {
			t.Errorf("EncodeScalar(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	src := `nginx_installed:
  pkg.installed:
    - name: nginx
    - version: '1.24'
    - refresh: true
/etc/nginx/nginx.conf:
  file.managed:
    - mode: '0644'
    - count: 3
    - ratio: 1.5
    - absent: null
    - require:
      - pkg: nginx_installed
`
	first := parse(t, src)
	out := Encode(first, EncodeOptions{})
	second, _, err := Parse([]byte(out), DefaultOptions("round.sls"))
	if err != nil {
		t.Fatalf("re-parsing the encoder's output failed: %v\n%s", err, out)
	}
	if !sameValue(first, second) {
		t.Errorf("round trip changed the value.\nencoded:\n%s", out)
	}
}

func TestEncodeFlow(t *testing.T) {
	v := value.MapOf("a", int64(1), "b", []any{"x", "y"})
	if got := Encode(v, EncodeOptions{Flow: true}); got != `{a: 1, b: [x, y]}` {
		t.Errorf("flow encode = %s", got)
	}
}

func TestEncodeEmptyCollections(t *testing.T) {
	v := value.MapOf("m", value.NewMap(0), "s", []any{})
	got := Encode(v, EncodeOptions{})
	want := "m: {}\ns: []\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, _, err := Parse([]byte(got), DefaultOptions("e.sls")); err != nil {
		t.Errorf("empty collections do not re-parse: %v", err)
	}
}

func TestSingleQuoteEscapes(t *testing.T) {
	if got := SingleQuote("it's"); got != "'it''s'" {
		t.Errorf("SingleQuote = %s", got)
	}
}

// sameValue compares two parsed trees, including mapping order.
func sameValue(a, b any) bool {
	am, aok := a.(*value.Map)
	bm, bok := b.(*value.Map)
	if aok != bok {
		return false
	}
	if aok {
		if am.Len() != bm.Len() {
			return false
		}
		ae, be := am.Entries(), bm.Entries()
		for i := range ae {
			if !reflect.DeepEqual(ae[i].Key, be[i].Key) || !sameValue(ae[i].Val, be[i].Val) {
				return false
			}
		}
		return true
	}
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if aok != bok {
		return false
	}
	if aok {
		if len(as) != len(bs) {
			return false
		}
		for i := range as {
			if !sameValue(as[i], bs[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}
