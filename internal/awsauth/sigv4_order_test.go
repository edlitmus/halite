package awsauth

import (
	"net/url"
	"testing"
)

// The canonical query is sorted by the encoded name, and a repeated name
// by the encoded value, as SigV4 sorts it (issue 613, DIVERGENCE 5.736).
// `{` is 0x7B and sorts after `b`; its encoding `%7B` sorts before it.
func TestTheCanonicalQuerySortsWhatItEncodes(t *testing.T) {
	for _, c := range []struct{ query, want string }{
		{"ab=2&a%7B=1", "a%7B=1&ab=2"},
		{"z=ab&z=a%7B", "z=a%7B&z=ab"},
		// Names that need no encoding keep the order they always had.
		{"b=2&a=1&a=0", "a=0&a=1&b=2"},
	} {
		u, err := url.Parse("https://example.amazonaws.com/?" + c.query)
		if err != nil {
			t.Fatal(err)
		}
		if got := canonicalQuery(u); got != c.want {
			t.Errorf("%s: canonical query %q, want %q", c.query, got, c.want)
		}
	}
}
