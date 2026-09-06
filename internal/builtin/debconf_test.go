package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The parsers take a string for the reason dpkg's do: every function
// checks the real filesystem for the tool first, so on a machine without
// debconf a test entering through the module never reaches the parsing.

// An answer that matches but has never been seen is not converged.
//
// debconf marks a question it has actually asked with a leading
// asterisk. An unseen value is a package default that nobody confirmed,
// and the package will still ask at install time — so a state that read
// only the value would report a converged node that then prompts, which
// is the failure pre-seeding exists to prevent.
func TestAnUnseenAnswerIsNotAnAnswer(t *testing.T) {
	got := parseDebconfShow(
		"* postfix/main_mailer_type: Internet Site\n" +
			"  postfix/mynetworks: 127.0.0.0/8\n")

	if got.Len() != 2 {
		t.Fatalf("%d questions, want 2: %v", got.Len(), got.StringKeys())
	}
	for _, tc := range []struct {
		question string
		value    string
		seen     bool
	}{
		{"postfix/main_mailer_type", "Internet Site", true},
		{"postfix/mynetworks", "127.0.0.0/8", false},
	} {
		val, seen := debconfCurrent(got, tc.question)
		if val != tc.value {
			t.Errorf("%s = %q, want %q", tc.question, val, tc.value)
		}
		if seen != tc.seen {
			t.Errorf("%s seen = %v, want %v", tc.question, seen, tc.seen)
		}
	}
}

// A value containing a colon survives, because an answer often is one.
func TestAnAnswerMayContainAColon(t *testing.T) {
	got := parseDebconfShow("* thing/url: https://example.invalid:8443/path\n")
	val, _ := debconfCurrent(got, "thing/url")
	if val != "https://example.invalid:8443/path" {
		t.Errorf("the answer was truncated at a colon: %q", val)
	}
}

// The selections format is tab separated and its value may hold spaces.
func TestSelectionsAreSplitOnTabsNotSpaces(t *testing.T) {
	got := parseDebconfSelections(
		"postfix\tpostfix/main_mailer_type\tselect\tInternet Site\n"+
			"# a comment\n"+
			"tzdata\ttzdata/Areas\tselect\tEurope\n", "")
	if got.Len() != 2 {
		t.Fatalf("%d selections, want 2: %v", got.Len(), got.StringKeys())
	}
	raw, ok := got.Get("postfix postfix/main_mailer_type")
	if !ok {
		t.Fatalf("the postfix selection is missing: %v", got.StringKeys())
	}
	m := raw.(*value.Map)
	if v, _ := m.Get("value"); v != "Internet Site" {
		t.Errorf("value = %v; a space in the answer split the record", v)
	}
	if v, _ := m.Get("type"); v != "select" {
		t.Errorf("type = %v", v)
	}

	// And it can be narrowed to one package.
	only := parseDebconfSelections(
		"postfix\tpostfix/x\tselect\ta\ntzdata\ttzdata/y\tselect\tb\n", "tzdata")
	if only.Len() != 1 {
		t.Errorf("%d selections for one package, want 1: %v", only.Len(), only.StringKeys())
	}
}

// A type debconf does not have is refused here rather than by
// debconf-set-selections, which warns on stderr and exits zero: the
// answer is not stored and nothing failed, which is the worst pair.
func TestAnUnknownTypeIsRefusedBeforeItIsSent(t *testing.T) {
	err := debconfSelection{
		Package: "postfix", Question: "postfix/x", Type: "dropdown", Value: "a",
	}.validate()
	if err == nil {
		t.Fatal("a type debconf does not have was accepted")
	}
	if !strings.Contains(err.Error(), "select") {
		t.Errorf("the refusal does not name the types debconf takes: %v", err)
	}
}

// A tab or a newline in an answer would corrupt the record.
func TestAnAnswerCannotBreakTheRecordFormat(t *testing.T) {
	for _, bad := range []string{"one\ttwo", "one\ntwo"} {
		err := debconfSelection{
			Package: "postfix", Question: "postfix/x", Type: "string", Value: bad,
		}.validate()
		if err == nil {
			t.Errorf("%q was accepted as an answer", bad)
		}
	}
}

// A boolean written in YAML reaches debconf as debconf spells it.
//
// `true` in a state is a Go bool by the time it gets here, and
// fmt.Sprint would render it `true` — which happens to be right, so this
// pins it rather than trusting the coincidence. Anything else in a
// boolean question is treated as false by debconf, silently.
func TestABooleanAnswerIsSpelledTheWayDebconfSpellsIt(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{
		{true, "true"},
		{false, "false"},
		{"Internet Site", "Internet Site"},
		{int64(8443), "8443"},
		{nil, ""},
	} {
		if got := debconfScalar(tc.in); got != tc.want {
			t.Errorf("debconfScalar(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The state takes both shapes a tree is written in.
func TestTheStateTakesAQuestionMappedEitherWay(t *testing.T) {
	// A question mapped straight to its answer, with one type for all.
	got, err := debconfWanted("postfix", value.MapOf(
		"postfix/mynetworks", "127.0.0.0/8",
		"postfix/main_mailer_type", "Internet Site",
	), "select")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d selections, want 2", len(got))
	}
	for _, s := range got {
		if s.Type != "select" {
			t.Errorf("%s took type %q, not the state's default", s.Question, s.Type)
		}
	}

	// And a question carrying its own type, which wins.
	got, err = debconfWanted("thing", value.MapOf(
		"thing/enable", value.MapOf("type", "boolean", "value", true),
	), "string")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d selections, want 1", len(got))
	}
	if got[0].Type != "boolean" {
		t.Errorf("the question's own type was overridden: %q", got[0].Type)
	}
	if got[0].Value != "true" {
		t.Errorf("value = %q, want true", got[0].Value)
	}
}

// A question with no type at all is refused, naming the question.
func TestAQuestionWithNoTypeIsRefusedByName(t *testing.T) {
	_, err := debconfWanted("postfix", value.MapOf("postfix/x", "a"), "")
	if err == nil {
		t.Fatal("a question with no type was accepted")
	}
	if !strings.Contains(err.Error(), "postfix/x") {
		t.Errorf("the refusal does not name the question: %v", err)
	}
}
