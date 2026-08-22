package redact

import (
	"strings"
	"testing"
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
