package ldap

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// roundTrip parses a filter and renders it back, so a test can say what
// the server would see.
func roundTrip(t *testing.T, filter string) string {
	t.Helper()
	encoded, err := ParseFilter(filter)
	if err != nil {
		t.Fatalf("%s: %v", filter, err)
	}
	parsed, _, err := parse(encoded)
	if err != nil {
		t.Fatalf("%s: %v", filter, err)
	}
	return describeFilter(parsed)
}

func TestFiltersRoundTrip(t *testing.T) {
	cases := []string{
		"(uid=ed)",
		"(objectClass=*)",
		"(&(objectClass=person)(uid=ed))",
		"(|(uid=ed)(uid=sam))",
		"(!(uid=ed))",
		"(&(objectClass=groupOfNames)(member=uid=ed,ou=people,dc=example,dc=com))",
		"(cn=admin*)",
		"(cn=*admin)",
		"(cn=a*b*c)",
	}
	for _, filter := range cases {
		if got := roundTrip(t, filter); got != filter {
			t.Errorf("%s became %s", filter, got)
		}
	}
}

func TestAMalformedFilterIsRefused(t *testing.T) {
	cases := []string{
		"", "uid=ed", "(uid=ed", "(&)", "(&(uid=ed)", "()", "(=ed)", "(uid=ed))",
	}
	for _, filter := range cases {
		if _, err := ParseFilter(filter); err == nil {
			t.Errorf("%q was accepted", filter)
		}
	}
}

// RFC 4515 section 3. This is the injection boundary.
func TestEscapeCoversEveryReservedCharacter(t *testing.T) {
	cases := map[string]string{
		"ed":               "ed",
		"*":                `\2a`,
		"(":                `\28`,
		")":                `\29`,
		`\`:                `\5c`,
		"*)(objectClass=*": `\2a\29\28objectClass=\2a`,
		"a\x00b":           `a\00b`,
	}
	for in, want := range cases {
		if got := Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

// A DN has a different reserved set from a filter's, and using the
// filter's escaping on a DN leaves `,` and `=` alone — which is how one
// attribute value becomes two.
func TestEscapeDNCoversItsOwnReservedSet(t *testing.T) {
	cases := map[string]string{
		"ed":             "ed",
		"Doe, Jane":      `Doe\, Jane`,
		"a=b":            `a\=b`,
		"a+b":            `a\+b`,
		" leading":       `\ leading`,
		"#hash":          `\#hash`,
		`back\slash`:     `back\\slash`,
		"admins,ou=evil": `admins\,ou\=evil`,
	}
	for in, want := range cases {
		if got := EscapeDN(in); got != want {
			t.Errorf("EscapeDN(%q) = %q, want %q", in, got, want)
		}
	}
}

// An escaped value is a value: what reaches the server in BER is the
// original bytes, not the `\xx` text.
func TestAnEscapedValueReachesTheServerAsItsBytes(t *testing.T) {
	const raw = "*)(objectClass=*"
	encoded, err := ParseFilter("(uid=" + Escape(raw) + ")")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, err := parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	children, err := parsed.children()
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("an equality match with %d parts", len(children))
	}
	if children[1].text() != raw {
		t.Errorf("the value arrived as %q, want %q", children[1].text(), raw)
	}
	// And it is one item, not a compound filter.
	if parsed.Tag != filterEqualityMatch {
		t.Errorf("the filter is choice %d, not an equality match", parsed.Tag)
	}
}

// A directory sends attribute names in whatever case it likes, and
// Active Directory does not agree with OpenLDAP.
func TestAttributeLookupIsCaseInsensitive(t *testing.T) {
	e := Entry{Attributes: map[string][]string{"memberOf": {"cn=a"}}}
	for _, name := range []string{"memberOf", "memberof", "MEMBEROF"} {
		if got := e.Values(name); len(got) != 1 {
			t.Errorf("%s found %v", name, got)
		}
	}
	if got := e.Values("member"); got != nil {
		t.Errorf("a different attribute matched: %v", got)
	}
}

func TestGroupNameIsTheFirstRDN(t *testing.T) {
	cases := map[string]string{
		"cn=platform,ou=groups,dc=example,dc=com": "platform",
		"CN=Platform Team,OU=Groups":              "Platform Team",
		"platform":                                "platform",
		"":                                        "",
	}
	for dn, want := range cases {
		if got := groupName(dn); got != want {
			t.Errorf("groupName(%q) = %q, want %q", dn, got, want)
		}
	}
}

// A result code an operator might see is named. A bare number sends
// somebody to a search engine; "invalid credentials" ends the question.
func TestAResultCodeIsNamed(t *testing.T) {
	r := &Result{Code: ResultInvalidCredentials, Message: "80090308: LdapErr"}
	if !strings.Contains(r.Error(), "invalid credentials") {
		t.Errorf("the result reads %q", r.Error())
	}
	if !strings.Contains(r.Error(), "80090308") {
		t.Errorf("the directory's own message was dropped: %q", r.Error())
	}
	unknown := &Result{Code: 9999}
	if !strings.Contains(unknown.Error(), "9999") {
		t.Errorf("an unknown code reads %q", unknown.Error())
	}
}

// A boolean "was the directory reachable" gets the commonest case
// wrong: somebody submits the form with the password blank, which this
// client refuses without asking the directory, and which is not an
// outage.
func TestAFailureIsClassifiedForTheLog(t *testing.T) {
	cases := map[string]error{
		"accepted":              nil,
		"invalid_credentials":   &Result{Code: ResultInvalidCredentials},
		"no_such_user":          ErrNoSuchUser,
		"malformed_request":     ErrMalformedRequest,
		"directory_refused":     &Result{Code: ResultInsufficientAccess},
		"directory_unreachable": errors.New("dial tcp: connection refused"),
	}
	for want, err := range cases {
		if got := Classify(err); got != want {
			t.Errorf("Classify(%v) = %q, want %q", err, got, want)
		}
	}
	// And a wrapped one is classified by what it wraps.
	wrapped := fmt.Errorf("finding the user: %w", ErrNoSuchUser)
	if got := Classify(wrapped); got != "no_such_user" {
		t.Errorf("a wrapped error classified as %q", got)
	}
}
