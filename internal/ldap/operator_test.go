package ldap

import (
	"bytes"
	"testing"
)

// The operator of a filter item is the first `=` and the character
// before it. A `>=`, `<=` or `~=` in the value is part of the value:
// RFC 4515 does not reserve those characters in a value and Escape does
// not escape them, so a username holding one reaches the parser as it
// was typed.
func TestAComparisonInTheValueIsPartOfTheValue(t *testing.T) {
	for _, tc := range []struct {
		filter string
		want   []byte
	}{
		{"(uid=a>=b)", ctxSeq(filterEqualityMatch, str("uid"), str("a>=b"))},
		{"(uid=a<=b)", ctxSeq(filterEqualityMatch, str("uid"), str("a<=b"))},
		{"(uid=a~=b)", ctxSeq(filterEqualityMatch, str("uid"), str("a~=b"))},
		{"(uid=" + Escape("x>=y") + ")", ctxSeq(filterEqualityMatch, str("uid"), str("x>=y"))},
		// The operators themselves still parse as operators.
		{"(uidNumber>=1000)", ctxSeq(filterGreaterOrEqual, str("uidNumber"), str("1000"))},
		{"(uidNumber<=1000)", ctxSeq(filterLessOrEqual, str("uidNumber"), str("1000"))},
		{"(cn~=smith)", ctxSeq(filterApproxMatch, str("cn"), str("smith"))},
		{"(description>=a>=b)", ctxSeq(filterGreaterOrEqual, str("description"), str("a>=b"))},
	} {
		got, err := ParseFilter(tc.filter)
		if err != nil {
			t.Errorf("%s: %v", tc.filter, err)
			continue
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s did not parse as the assertion it says", tc.filter)
		}
	}
	for _, bad := range []string{"(>=1)", "(<=1)", "(~=1)"} {
		if _, err := ParseFilter(bad); err == nil {
			t.Errorf("%q, an operator with no attribute, was accepted", bad)
		}
	}
}

// An operator whose username holds `>=` can log in. Against the code's
// own fake directory: no real directory was available.
func TestAUsernameHoldingAComparisonCanLogIn(t *testing.T) {
	d := directoryWith(t)
	const dn = "uid=a\\>\\=b,ou=people,dc=example,dc=com"
	d.user(dn, "pw-for-a")
	d.entry(Entry{DN: dn, Attributes: map[string][]string{"uid": {"a>=b"}}})
	c := clientFor(t, d, nil)

	identity, err := c.Authenticate("a>=b", "pw-for-a")
	if err != nil {
		t.Fatalf("the operator could not log in: %v", err)
	}
	if identity.DN != dn {
		t.Errorf("logged in as %q", identity.DN)
	}
}
