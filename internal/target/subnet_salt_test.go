package target

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// TestSubnetAgreesWithSalt holds `-S` to what Salt 3007.1's own ipcidr
// matcher answered for every pair in testdata/ipcidr-salt3007.json. The
// file is Salt's output, captured by the script beside it, not written
// from Salt's documentation; issue 614's own expectation for an
// IPv4-mapped expression turned out to be the opposite of Salt's
// answer, which is the reason for asking.
func TestSubnetAgreesWithSalt(t *testing.T) {
	raw, err := os.ReadFile("testdata/ipcidr-salt3007.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Salt    string `json:"salt"`
		Results []struct {
			Node   string              `json:"node"`
			Grains map[string][]string `json:"grains"`
			Expr   string              `json:"expr"`
			Match  any                 `json:"match"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.Salt != "3007.1" || len(capture.Results) < 100 {
		t.Fatalf("the capture is not the one this test was written against: salt %q, %d rows", capture.Salt, len(capture.Results))
	}

	// Salt parses a network strictly and refuses one with host bits set;
	// halite masks it. A difference kept on purpose, recorded in
	// DIVERGENCE 5.614, and named here so that it is the
	// only one.
	masked := map[string]string{"10.0.0.1/24": "10.0.0.0/24", "fe80::1/10": "fe80::/10"}

	checked := 0
	for _, r := range capture.Results {
		g := value.MapOf()
		for k, list := range r.Grains {
			items := make([]any, len(list))
			for i, s := range list {
				items[i] = s
			}
			g.Set(k, items)
		}
		n := Node{ID: r.Node, Grains: g}

		m, err := Compile(Subnet, r.Expr, nil)
		switch want := r.Match.(type) {
		case bool:
			if err != nil {
				t.Errorf("%s: Salt answered %v for %q, halite refused it: %v", r.Node, want, r.Expr, err)
				continue
			}
			if got := m.Match(n); got != want {
				t.Errorf("%s %v: -S %q = %v, Salt 3007.1 says %v", r.Node, r.Grains, r.Expr, got, want)
			}
		case string:
			if want != "error" {
				t.Fatalf("unexpected capture value %q", want)
			}
			if to, ok := masked[r.Expr]; ok {
				mm, err := Compile(Subnet, to, nil)
				if err != nil {
					t.Fatal(err)
				}
				if m == nil || m.Match(n) != mm.Match(n) {
					t.Errorf("%s: %q is not masked to %q", r.Node, r.Expr, to)
				}
				continue
			}
			// Salt logs and matches nothing; halite refuses to compile
			// it, so a job never goes out on a target that cannot mean
			// what was typed.
			if err == nil {
				t.Errorf("%s: Salt could not parse %q, halite compiled it", r.Node, r.Expr)
			}
		default:
			t.Fatalf("unexpected capture value %v", r.Match)
		}
		checked++
	}
	if checked < 100 {
		t.Fatalf("only %d rows were checked", checked)
	}
}
