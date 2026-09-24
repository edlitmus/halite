package redact

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

func TestScrubReplacesKnownValues(t *testing.T) {
	s := New()
	s.Add("s3cret-password-value")
	s.Add("another-secret-here")

	got := s.Scrub("the command \"mysql -p s3cret-password-value\" failed")
	if strings.Contains(got, "s3cret-password-value") {
		t.Errorf("the secret survived: %q", got)
	}
	if !strings.Contains(got, Placeholder) {
		t.Errorf("nothing was substituted: %q", got)
	}
	// The rest of the message is what makes it a diagnostic.
	if !strings.Contains(got, "mysql -p") || !strings.Contains(got, "failed") {
		t.Errorf("the message was mangled: %q", got)
	}
}

// A short value cannot be scrubbed without scrubbing everything that
// looks like it, and a one-character secret was never secret.
func TestShortValuesAreNotScrubbed(t *testing.T) {
	s := New()
	s.Add("1")
	s.Add("yes")
	s.Add("abc12") // one under the floor
	if got := s.Scrub("1 yes abc12 and 1 again"); got != "1 yes abc12 and 1 again" {
		t.Errorf("a short value was scrubbed, destroying the message: %q", got)
	}
	if s.Len() != 0 {
		t.Errorf("held %d short values, want none", s.Len())
	}
}

// A secret that contains another must be replaced whole, or the tail of
// it is left in the text.
func TestLongestValueWins(t *testing.T) {
	s := New()
	s.Add("secret-value")
	s.Add("secret-value-with-more")
	got := s.Scrub("here is secret-value-with-more")
	if strings.Contains(got, "with-more") {
		t.Errorf("the longer secret was replaced piecemeal: %q", got)
	}
	if got != "here is "+Placeholder {
		t.Errorf("got %q", got)
	}
}

// A decrypted pillar file is handed over whole: which of its values are
// secret is not knowable, and everything that arrived encrypted was
// encrypted for a reason.
func TestAddTreeWalksEverything(t *testing.T) {
	s := New()
	s.AddTree(map[string]any{
		"users": map[string]any{
			"ed": []any{"password-in-a-list", map[string]any{"token": "token-in-a-map"}},
		},
		"count": 3,
	})
	for _, secret := range []string{"password-in-a-list", "token-in-a-map"} {
		if got := s.Scrub("saw " + secret); strings.Contains(got, secret) {
			t.Errorf("%q was not recorded", secret)
		}
	}
}

func TestNilSetIsHarmless(t *testing.T) {
	var s *Set
	if got := s.Scrub("anything"); got != "anything" {
		t.Errorf("a nil set changed the text: %q", got)
	}
	if s.Len() != 0 {
		t.Error("a nil set holds something")
	}
}

func TestScrubValueKeepsTheShape(t *testing.T) {
	s := New()
	s.Add("the-secret-value")
	out := s.ScrubValue(map[string]any{
		"a": "the-secret-value", "b": []any{"the-secret-value", 7},
	})
	m := out.(map[string]any)
	if m["a"] != Placeholder {
		t.Errorf("a = %#v", m["a"])
	}
	list := m["b"].([]any)
	if list[0] != Placeholder || list[1] != 7 {
		t.Errorf("b = %#v", list)
	}
}

// A credential embedded in a `source:` URL is the one secret no set of
// known values can hold: it was never a pillar value, so nothing ever
// handed it to Add. It arrives in a failing state's own comment.
func TestURLCredentialsAreScrubbedWithoutBeingKnown(t *testing.T) {
	s := New()
	got := s.Scrub(`open https://deploy:hunter2sekrit@artifacts.example.com/vmop/agent.tgz: no such file`)
	if strings.Contains(got, "hunter2sekrit") || strings.Contains(got, "deploy:") {
		t.Errorf("the credential survived: %q", got)
	}
	want := `open https://` + URLPlaceholder + `@artifacts.example.com/vmop/agent.tgz: no such file`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// The hub that has no encrypted pillar registers no values at all, and
// it is not the hub that is safe — it is the one that will print an
// operator's credentialed URL with nothing standing in the way. An
// empty set and a nil set must both still strip it.
func TestURLCredentialsAreScrubbedByAnEmptyAndANilSet(t *testing.T) {
	const line = `fetching ftp://svc:p4ssw0rd@files.example.com/pkg`
	for _, tc := range []struct {
		name string
		set  *Set
	}{
		{"empty", New()},
		{"nil", nil},
	} {
		got := tc.set.Scrub(line)
		if strings.Contains(got, "p4ssw0rd") {
			t.Errorf("%s set: the credential survived: %q", tc.name, got)
		}
		if !strings.Contains(got, "files.example.com/pkg") {
			t.Errorf("%s set: the diagnostic was lost: %q", tc.name, got)
		}
	}
}

// Salt's own regex is `(https?)://.*@`, greedy and unanchored: on this
// line it matches from the first "://" to the last "@" and takes the
// first URL's host, the second URL and the words between them with it.
// Bounding the match to the authority is the difference between a
// redacted diagnostic and no diagnostic.
func TestRedactionStopsAtTheAuthority(t *testing.T) {
	got := New().Scrub(`https://bob:pw123456@a.example.com/x failed over to https://c.example.com/y for ops@example.com`)
	want := `https://` + URLPlaceholder + `@a.example.com/x failed over to https://c.example.com/y for ops@example.com`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// Every scheme, not just http and https as Salt does: the same
// credential is the same credential.
func TestEverySchemeIsCovered(t *testing.T) {
	for _, url := range []string{
		"https://u:sekrit-value@h/p",
		"http://u:sekrit-value@h/p",
		"HTTPS://u:sekrit-value@h/p",
		"git+ssh://u:sekrit-value@h/p",
		"ftp://u:sekrit-value@h/p",
		"s3://u:sekrit-value@h/p",
	} {
		if got := New().Scrub(url); strings.Contains(got, "sekrit-value") {
			t.Errorf("%s: the credential survived: %q", url, got)
		}
	}
}

// A URL with no userinfo carries no secret, and rewriting it would make
// every diagnostic in the estate say something the operator did not
// write.
func TestURLsWithoutCredentialsAreUntouched(t *testing.T) {
	for _, text := range []string{
		"open https://artifacts.example.com/vmop/agent.tgz: no such file",
		// A plain URL and an unrelated address on one line: Salt's
		// greedy regex rewrites this to "https://<redacted>@example.com"
		// and loses the whole diagnostic.
		"open https://artifacts.example.com/vmop/agent.tgz then mail ops@example.com",
		"mail ops@example.com about it",
		"the user root@localhost is not a URL",
		"a path /srv/salt/x@y is not a URL",
	} {
		if got := New().Scrub(text); got != text {
			t.Errorf("%q was rewritten to %q", text, got)
		}
	}
}

// The logger hands structured fields through ScrubValue, and a nil set
// is the ordinary case there too.
func TestScrubValueStripsURLCredentials(t *testing.T) {
	var s *Set
	out := s.ScrubValue(map[string]any{
		"source": "https://svc:tok3n-value@h/p",
		"list":   []any{"https://svc:tok3n-value@h/p", 7},
	})
	m := out.(map[string]any)
	if strings.Contains(m["source"].(string), "tok3n-value") {
		t.Errorf("source = %#v", m["source"])
	}
	list := m["list"].([]any)
	if strings.Contains(list[0].(string), "tok3n-value") {
		t.Errorf("list = %#v", list)
	}
	if list[1] != 7 {
		t.Errorf("the shape changed: %#v", list)
	}
}

// A pillar arrives as a *value.Map and never as a map[string]any: it is
// what the hub sends and what DecodeJSON returns. While AddTree knew
// only the plain map, `n.secrets.AddTree(pillar)` recorded nothing, and
// the failure was silent in the worst way — the set reported itself
// empty rather than wrong, and every decrypted pillar value stayed
// printable in every comment, log record and job return.
func TestAddTreeRecordsAPillarAsItActuallyArrives(t *testing.T) {
	s := New()
	s.AddTree(value.MapOf(
		"base.repo", value.MapOf(
			"repo", value.MapOf(
				"artifactory", value.MapOf(
					"username", "deploy-user",
					"authorization", "tok3n-from-the-pillar",
				),
			),
		),
		"nested_list", []any{
			value.MapOf("inner", "secret-inside-a-list"),
			"bare-string-secret",
		},
	))

	if s.Len() == 0 {
		t.Fatal("the pillar was handed over and nothing was recorded")
	}
	for _, secret := range []string{
		"tok3n-from-the-pillar", "deploy-user",
		"secret-inside-a-list", "bare-string-secret",
	} {
		if got := s.Scrub("saw " + secret); strings.Contains(got, secret) {
			t.Errorf("%q was not recorded: %q", secret, got)
		}
	}
}

// Once the redactor was actually seeded, it began eating the schema: a
// node's pillar holds ordinary words, the floor is six characters, and
// a pillar value of "recurse" turns `file.recurse` into
// `file.**********` in every diagnostic that names the state.
func TestScrubExceptSparesTheIdentifiersItIsGiven(t *testing.T) {
	s := New()
	s.Add("recurse")
	s.Add("a-real-secret-value")

	text := "          ID: salt-minion-config\n" + // lexicon:allow — a state ID
		"    Function: file.recurse\n" +
		"     Comment: wrote a-real-secret-value, and recurse was asked for\n"
	got := s.ScrubExcept(text, []string{"salt-minion-config", "file.recurse"}) // lexicon:allow — a state ID

	if !strings.Contains(got, "Function: file.recurse") {
		t.Errorf("the function name was scrubbed: %q", got)
	}
	if strings.Contains(got, "a-real-secret-value") {
		t.Errorf("a secret survived: %q", got)
	}
	// The exemption is a span, not the word: the same value outside a
	// kept identifier is still a secret.
	if !strings.Contains(got, "and "+Placeholder+" was asked for") {
		t.Errorf("a secret was spared outside the identifiers: %q", got)
	}
}

// The exemption is from the value set, not from redaction. This estate
// declares a state whose ID *is* a credentialed URL, so an exemption
// that turned scrubbing off entirely would put the credential back into
// every job return.
func TestScrubExceptStillStripsURLCredentials(t *testing.T) {
	s := New()
	id := "https://deploy:hunter2sekrit@artifacts.example.com/agent.tgz"
	got := s.ScrubExcept("ID: "+id+"\n", []string{id})
	if strings.Contains(got, "hunter2sekrit") {
		t.Errorf("a kept span carried a credential through: %q", got)
	}
	if !strings.Contains(got, "artifacts.example.com/agent.tgz") {
		t.Errorf("the identifier was lost: %q", got)
	}
}

func TestScrubExceptWithNothingKeptIsScrub(t *testing.T) {
	s := New()
	s.Add("the-secret-value")
	text := "saw the-secret-value here"
	if got := s.ScrubExcept(text, nil); got != s.Scrub(text) {
		t.Errorf("got %q", got)
	}
	// An empty literal must not protect everything.
	if got := s.ScrubExcept(text, []string{""}); strings.Contains(got, "the-secret-value") {
		t.Errorf("an empty keep protected the whole text: %q", got)
	}
}

// Overlapping and repeated identifiers are ordinary: the same function
// name appears on every state in a run.
func TestScrubExceptHandlesRepeatedAndOverlappingSpans(t *testing.T) {
	s := New()
	s.Add("recurse")
	text := "file.recurse file.recurse recurse"
	got := s.ScrubExcept(text, []string{"file.recurse", "recurse"})
	// Every occurrence is inside a kept span here, so nothing goes.
	if got != text {
		t.Errorf("got %q, want %q", got, text)
	}
	// And with only the longer one kept, the bare word still goes.
	got = s.ScrubExcept(text, []string{"file.recurse"})
	if !strings.HasPrefix(got, "file.recurse file.recurse ") {
		t.Errorf("a kept span was damaged: %q", got)
	}
	if strings.HasSuffix(got, "recurse") {
		t.Errorf("the bare secret was spared: %q", got)
	}
}

// A short identifier must not be able to switch redaction off.
//
// The first version of ScrubExcept split the text at every kept span
// and scrubbed the pieces. An SLS named `s` -- which the node's own
// end-to-end test uses -- protected every "s" in the output, so a
// decrypted pillar value split across two fragments matched nothing and
// was printed in full. A secret that merely touches an identifier has
// to win.
func TestAShortIdentifierCannotDisableScrubbing(t *testing.T) {
	s := New()
	s.Add("s3cret-bearer-token-value")

	text := `Comment: The command "/bin/echo s3cret-bearer-token-value" ran.`
	got := s.ScrubExcept(text, []string{"s", "call_the_api", "cmd.run"})

	if strings.Contains(got, "s3cret-bearer-token-value") {
		t.Errorf("a one-character identifier spared the secret: %q", got)
	}
	if !strings.Contains(got, Placeholder) {
		t.Errorf("nothing was replaced: %q", got)
	}
}

// Containment, not overlap: only a secret the identifier wholly
// contains is spared.
func TestOnlyAWhollyContainedSecretIsSpared(t *testing.T) {
	s := New()
	s.Add("recurse")

	// Wholly inside `file.recurse` -- spared.
	if got := s.ScrubExcept("file.recurse", []string{"file.recurse"}); got != "file.recurse" {
		t.Errorf("a contained secret was scrubbed: %q", got)
	}
	// Overlapping a kept span by a character -- the secret wins, and the
	// identifier is damaged, which is the safe direction.
	if got := s.ScrubExcept("xrecurse", []string{"xrecur"}); strings.Contains(got, "recurse") {
		t.Errorf("an overlapping secret was spared: %q", got)
	}
}

// Two secrets in one line, neither inside an identifier, both replaced
// and neither swallowing the other.
func TestScrubExceptReplacesEveryOccurrence(t *testing.T) {
	s := New()
	s.Add("first-secret-value")
	s.Add("second-secret-value")
	got := s.ScrubExcept(
		"a first-secret-value b second-secret-value c first-secret-value",
		[]string{"file.recurse"})
	want := "a " + Placeholder + " b " + Placeholder + " c " + Placeholder
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// A secret that contains another is still replaced whole.
func TestScrubExceptReplacesTheLongerSecretWhole(t *testing.T) {
	s := New()
	s.Add("secret-value")
	s.Add("secret-value-with-more")
	got := s.ScrubExcept("here is secret-value-with-more", []string{"nothing"})
	if got != "here is "+Placeholder {
		t.Errorf("got %q", got)
	}
}

// The ordered map of the nine-type model is scrubbed, keys and all.
//
// It was not: the type switch handled `map[string]any` and fell through on
// `*value.Map`, which is what a state's changes, a pillar fragment, a grain
// set and a `--out json` report all are. Every caller that handed one over
// got it back unchanged, having done the right thing. DIVERGENCE 5.134.
func TestScrubValueScrubsTheOrderedMap(t *testing.T) {
	s := New()
	s.Add("hunter2")
	s.Add("s3cret-key-name")

	in := value.MapOf(
		"password", "hunter2",
		"nested", value.MapOf("inner", "prefix-hunter2-suffix"),
		"list", []any{"hunter2", "safe"},
		// A key can carry a secret too.
		"s3cret-key-name", "value",
	)
	out, ok := s.ScrubValue(in).(*value.Map)
	if !ok {
		t.Fatalf("ScrubValue returned %T, want *value.Map", s.ScrubValue(in))
	}

	if v, _ := out.Get("password"); v != Placeholder {
		t.Errorf("password = %v, want it scrubbed", v)
	}
	nested, _ := out.Get("nested")
	inner, _ := nested.(*value.Map).Get("inner")
	if strings.Contains(inner.(string), "hunter2") {
		t.Errorf("the nested value still holds the secret: %v", inner)
	}
	list, _ := out.Get("list")
	if got := list.([]any)[0]; got != Placeholder {
		t.Errorf("the list member is %v, want it scrubbed", got)
	}
	if _, present := out.Get("s3cret-key-name"); present {
		t.Error("a key that is itself a secret survived unscrubbed")
	}
	// The shape is kept: a redactor that flattened its input would be
	// unusable for a state's changes.
	if out.Len() != in.Len() {
		t.Errorf("the scrubbed map holds %d keys, the original held %d", out.Len(), in.Len())
	}
	// And the original is untouched, because a caller logging a value it
	// still needs must not have it rewritten underneath.
	if v, _ := in.Get("password"); v != "hunter2" {
		t.Errorf("the input was modified: %v", v)
	}
}
