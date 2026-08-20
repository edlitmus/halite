package yaml

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// The YAML Test Suite, required by SPEC section 31: "the parser runs
// against the YAML test suite, with the expected-failure set for the
// constructs section 10.1.2 rejects recorded explicitly rather than
// skipped silently."
//
// Recorded explicitly is the whole design here. Every case where halite
// disagrees with the suite has a row in the table below saying which way
// it disagrees and why, and the disagreement is one of two things:
//
//   - a deliberate restriction of SPEC section 10.1.2 or a resolution rule
//     of 10.1.3, in which case halite is right and the suite's YAML 1.2
//     expectation does not apply. Marked bySpec.
//
//   - a defect. Marked as a gap, and counted, so that the number is a
//     visible fact rather than a vague sense that the parser is roughly
//     fine.
//
// The table is enforced in both directions. A case that deviates without a
// row fails, and a row whose case has stopped deviating fails as stale, so
// fixing a defect forces the row out and the count down.
//
// The suite is data, not a dependency: nothing here is imported, so the
// zero-dependency property of SPEC section 4.2 is untouched. Refresh it
// with tools/vendor-yaml-test-suite.sh.

const suiteFile = "testdata/yaml-test-suite/cases.json"

type suiteCase struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	YAML  string `json:"yaml"`
	Error bool   `json:"error"`
	JSON  string `json:"json"`
}

// devKind is the direction of a disagreement.
type devKind int

const (
	// devRejects: the suite says the document is valid and halite errors.
	devRejects devKind = iota
	// devAccepts: the suite says the document is invalid and halite parses
	// it. Being lenient is the less dangerous direction for a Salt tree,
	// but it is still a disagreement.
	devAccepts
	// devValue: halite parses it, and the tree differs from in.json.
	devValue
)

func (k devKind) String() string {
	switch k {
	case devRejects:
		return "rejects a valid document"
	case devAccepts:
		return "accepts an invalid document"
	}
	return "produces a different value"
}

// reason explains a class of disagreement. bySpec marks the ones where
// halite is deliberately not YAML 1.2.
type reason struct {
	bySpec bool
	text   string
}

var reasons = map[string]reason{
	specTag: {true,
		"SPEC 10.1.2 admits only the tags of the nine types. A local, custom, or verbatim " +
			"tag is refused by design: a parser that can construct an arbitrary type is how " +
			"a YAML file becomes code execution."},
	specTab: {true,
		"SPEC 10.1.2 rejects tab characters used for indentation."},
	specComplexKey: {true,
		"SPEC 10.1.2 rejects a mapping or a sequence used as a mapping key."},
	specDuplicateKey: {true,
		"SPEC 10.1.2 rejects duplicate keys in one mapping. YAML 1.2 permits them and " +
			"PyYAML silently keeps the last, which is a frequent invisible cause of a state " +
			"that does nothing."},

	gapChomping: {false,
		"block scalar chomping drops trailing line breaks that should be kept. SPEC 10.1.1 " +
			"names this as mattering for file.managed contents, so it is the most damaging " +
			"gap in this table."},
	gapMultilinePlain: {false,
		"a plain scalar continued onto following lines is not read as one scalar when it is " +
			"a mapping value."},
	gapDirective: {false,
		"a %YAML or %TAG directive is emitted as a scalar document instead of being consumed."},
	gapExplicitKey: {false,
		"an explicit `? ` key with no `:` line is legal and means a null value; halite " +
			"requires the `:` line."},
	gapAfterDocument: {false,
		"content that belongs to the document is read as trailing content after it."},
	gapFlow: {false,
		"a flow collection construct is not parsed. What remains is a multi-line plain " +
			"scalar used as a flow mapping key, which YAML allows in a mapping and forbids " +
			"in a sequence: letting it fold in both places fixes two cases and breaks two " +
			"valid ones, so it needs the two contexts told apart rather than one rule."},
	gapMappingKey: {false,
		"a mapping key is not recognised, mostly around whitespace and quoting before the colon."},
	gapPlainScalar: {false,
		"a plain scalar is cut short by a character that is only special elsewhere."},
	gapLenient: {false,
		"halite parses a document the suite requires to be an error. Leniency is the safer " +
			"direction for an existing tree, so these rank below the rejections."},
	gapOther: {false,
		"an unclassified parse refusal."},
	gapValueOther: {false,
		"an unclassified difference between the parsed tree and in.json."},
}

const (
	specTag          = "specTag"
	specTab          = "specTab"
	specComplexKey   = "specComplexKey"
	specDuplicateKey = "specDuplicateKey"

	gapAfterDocument  = "gapAfterDocument"
	gapChomping       = "gapChomping"
	gapDirective      = "gapDirective"
	gapExplicitKey    = "gapExplicitKey"
	gapFlow           = "gapFlow"
	gapLenient        = "gapLenient"
	gapMappingKey     = "gapMappingKey"
	gapMultilinePlain = "gapMultilinePlain"
	gapOther          = "gapOther"
	gapPlainScalar    = "gapPlainScalar"
	gapValueOther     = "gapValueOther"
)

type deviation struct {
	ID     string
	Kind   devKind
	Reason string
}

// deviations is the expected-failure set. Every row is a case where halite
// and the suite disagree, and why.
var deviations = []deviation{
	{"26DV", devRejects, gapMappingKey},
	{"2JQS", devRejects, specDuplicateKey},
	{"2XXW", devRejects, gapExplicitKey},
	{"3HFZ", devAccepts, gapLenient},
	{"4FJ6", devRejects, gapFlow},
	{"4JVG", devAccepts, gapLenient},
	{"52DL", devRejects, specTag},
	{"565N", devValue, gapChomping},
	{"57H4", devRejects, specTag},
	{"5LLU", devAccepts, gapLenient},
	{"5TRB", devAccepts, gapLenient},
	{"5TYM", devRejects, specTag},
	{"5U3A", devAccepts, gapLenient},
	{"6BCT", devRejects, gapAfterDocument},
	{"6CA3", devRejects, specTab},
	{"6CK3", devRejects, specTag},
	{"6FWR", devValue, gapChomping},
	{"6HB6", devRejects, specTab},
	{"6PBE", devRejects, gapExplicitKey},
	{"6WLZ", devRejects, specTag},
	{"7FWL", devRejects, specTag},
	{"7LBH", devAccepts, gapLenient},
	{"87E4", devRejects, gapMappingKey},
	{"8KB6", devRejects, gapFlow},
	{"8MK2", devRejects, specTag},
	{"9C9N", devAccepts, gapLenient},
	{"9JBA", devAccepts, gapLenient},
	{"9KAX", devValue, gapValueOther},
	{"9KBC", devAccepts, gapLenient},
	{"9MAG", devAccepts, gapLenient},
	{"9MMW", devRejects, gapFlow},
	{"9MQT/01", devAccepts, gapLenient},
	{"9WXW", devRejects, specTag},
	{"A2M4", devRejects, gapMultilinePlain},
	{"AB8U", devRejects, gapAfterDocument},
	{"BU8L", devRejects, gapMultilinePlain},
	{"C4HZ", devRejects, specTag},
	{"CC74", devRejects, specTag},
	{"CN3R", devValue, gapValueOther},
	{"CT4Q", devRejects, gapFlow},
	{"CTN5", devAccepts, gapLenient},
	{"CUP7", devRejects, specTag},
	{"CVW2", devAccepts, gapLenient},
	{"CXX2", devAccepts, gapLenient},
	{"D49Q", devAccepts, gapLenient},
	{"DK95/00", devRejects, specTab},
	{"DK95/01", devAccepts, gapLenient},
	{"DK95/03", devRejects, specTab},
	{"DK95/07", devRejects, specTab},
	{"G5U8", devAccepts, gapLenient},
	{"J7PZ", devRejects, specTag},
	{"K54U", devValue, gapValueOther},
	{"KK5P", devRejects, specComplexKey},
	{"L24T/00", devValue, gapChomping},
	{"L24T/01", devValue, gapChomping},
	{"LQZ7", devRejects, gapMappingKey},
	{"LX3P", devRejects, gapOther},
	{"M2N8/00", devRejects, gapPlainScalar},
	{"M2N8/01", devRejects, specComplexKey},
	{"M5C3", devRejects, specTag},
	{"M5DY", devRejects, specComplexKey},
	{"MUS6/01", devAccepts, gapLenient},
	{"N782", devAccepts, gapLenient},
	{"NJ66", devRejects, gapFlow},
	{"P76L", devRejects, specTag},
	{"Q5MG", devRejects, specTab},
	{"Q9WF", devRejects, gapOther},
	{"QB6E", devAccepts, gapLenient},
	{"RXY3", devAccepts, gapLenient},
	{"RZP5", devRejects, specComplexKey},
	{"S4JQ", devRejects, specTag},
	{"S98Z", devAccepts, gapLenient},
	{"SBG9", devRejects, specComplexKey},
	{"SKE5", devRejects, gapAfterDocument},
	{"SR86", devAccepts, gapLenient},
	{"SU74", devAccepts, gapLenient},
	{"SY6V", devAccepts, gapLenient},
	{"U3C3", devRejects, specTag},
	{"U99R", devAccepts, gapLenient},
	{"UGM3", devRejects, specTag},
	{"UKK6/02", devRejects, specTag},
	{"UT92", devRejects, gapFlow},
	{"V9D5", devRejects, gapPlainScalar},
	{"VJP3/00", devAccepts, gapLenient},
	{"W4TN", devValue, gapDirective},
	{"W9L4", devAccepts, gapLenient},
	{"WZ62", devValue, gapValueOther},
	{"X38W", devRejects, gapFlow},
	{"X4QW", devAccepts, gapLenient},
	{"XW4D", devRejects, specComplexKey},
	{"Y79Y/002", devRejects, specTab},
	{"Y79Y/004", devAccepts, gapLenient},
	{"Y79Y/005", devAccepts, gapLenient},
	{"Y79Y/006", devAccepts, gapLenient},
	{"Y79Y/008", devAccepts, gapLenient},
	{"Y79Y/010", devValue, gapValueOther},
	{"YJV2", devAccepts, gapLenient},
	{"Z67P", devRejects, specTag},
	{"Z9M4", devRejects, specTag},
	{"ZCZ6", devAccepts, gapLenient},
	{"ZL4Z", devAccepts, gapLenient},
}

func loadSuite(t *testing.T) []suiteCase {
	t.Helper()
	raw, err := os.ReadFile(suiteFile)
	if err != nil {
		t.Fatalf("%s: %v\nrun tools/vendor-yaml-test-suite.sh to fetch it", suiteFile, err)
	}
	var cases []suiteCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("%s: %v", suiteFile, err)
	}
	if len(cases) == 0 {
		t.Fatalf("%s holds no cases", suiteFile)
	}
	return cases
}

func deviationIndex(t *testing.T) map[string]deviation {
	t.Helper()
	out := make(map[string]deviation, len(deviations))
	for _, d := range deviations {
		if prev, dup := out[d.ID]; dup {
			t.Fatalf("case %s has two rows in the deviation table: %v and %v", d.ID, prev.Reason, d.Reason)
		}
		if _, ok := reasons[d.Reason]; !ok {
			t.Fatalf("case %s cites reason %q, which is not defined", d.ID, d.Reason)
		}
		out[d.ID] = d
	}
	return out
}

// outcome is what halite made of one case.
type outcome struct {
	kind devKind
	// agrees reports that halite and the suite say the same thing.
	agrees bool
	detail string
}

func runSuiteCase(c suiteCase) outcome {
	docs, _, err := ParseStream([]byte(c.YAML), Options{File: c.ID, Bool11: true})

	if c.Error {
		if err != nil {
			return outcome{agrees: true}
		}
		return outcome{kind: devAccepts, detail: "parsed without error"}
	}
	if err != nil {
		return outcome{kind: devRejects, detail: err.Error()}
	}
	if c.JSON == "" {
		// No expected value to compare against; parsing was the assertion.
		return outcome{agrees: true}
	}

	want, jerr := decodeJSONStream(c.JSON)
	if jerr != nil {
		// The suite's own file did not decode, which is not halite's
		// business to fail over.
		return outcome{agrees: true}
	}
	got := make([]any, len(docs))
	for i, d := range docs {
		got[i] = jsonShape(d)
	}
	if len(got) == len(want) && sameJSON(any(got), any(want)) {
		return outcome{agrees: true}
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	return outcome{kind: devValue, detail: fmt.Sprintf("want %s\n     got  %s", clip(string(w)), clip(string(g)))}
}

// Every case either agrees with the suite or has a row saying why not. The
// check runs both ways: an undocumented disagreement fails, and so does a
// row whose case has stopped disagreeing.
func TestYAMLTestSuite(t *testing.T) {
	cases := loadSuite(t)
	index := deviationIndex(t)
	seen := map[string]bool{}

	// Fixing a gap moves many rows at once, so the table can be rebuilt
	// from what the parser now does:
	//
	//	HALITE_YAML_SUITE_REGEN=1 go test ./internal/yaml/ -run TestYAMLTestSuite
	//
	// It prints the rows and fails, deliberately: the reasons it carries
	// forward are the ones already recorded, and a newly deviating case
	// gets a placeholder that a person has to classify.
	if os.Getenv("HALITE_YAML_SUITE_REGEN") != "" {
		regenerateTable(t, cases, index)
		return
	}

	for _, c := range cases {
		c := c
		t.Run(strings.ReplaceAll(c.ID, "/", "_"), func(t *testing.T) {
			got := runSuiteCase(c)
			d, recorded := index[c.ID]
			if recorded {
				seen[c.ID] = true
			}

			switch {
			case got.agrees && !recorded:
				// The ordinary case.
			case got.agrees && recorded:
				t.Errorf("%s (%s) no longer deviates, but the table still records it as %q.\n"+
					"Remove its row: a stale expected failure hides the next real one.",
					c.ID, c.Name, d.Reason)
			case !got.agrees && !recorded:
				t.Errorf("%s (%s) %s, and the table does not record it.\n  %s\n\ninput:\n%s\n\n"+
					"Either fix the parser or add a row to deviations with the reason.",
					c.ID, c.Name, got.kind, got.detail, indented(c.YAML))
			case d.Kind != got.kind:
				t.Errorf("%s (%s) is recorded as %q but now %s.\n  %s",
					c.ID, c.Name, d.Kind, got.kind, got.detail)
			}
		})
	}

	for id := range index {
		if seen[id] {
			continue
		}
		t.Errorf("the deviation table records case %s, which the suite does not contain.\n"+
			"It was probably renamed or dropped upstream; remove the row.", id)
	}
}

// The counts are printed rather than asserted, because the number that
// matters is the trend and the table above is what enforces it. A summary
// in the log is what makes a fix visible as progress.
func TestYAMLTestSuiteSummary(t *testing.T) {
	cases := loadSuite(t)
	index := deviationIndex(t)

	byReason := map[string]int{}
	var bySpec, gaps int
	for _, d := range index {
		byReason[d.Reason]++
		if reasons[d.Reason].bySpec {
			bySpec++
			continue
		}
		gaps++
	}

	agree := len(cases) - len(index)
	t.Logf("YAML test suite: %d cases", len(cases))
	t.Logf("  agree with the suite:            %d (%.1f%%)", agree, pct(agree, len(cases)))
	t.Logf("  deliberate, per SPEC 10.1.2/3:   %d", bySpec)
	t.Logf("  gaps:                            %d", gaps)
	t.Logf("  conformance where halite claims to conform: %.1f%%", pct(agree, len(cases)-bySpec))

	names := make([]string, 0, len(byReason))
	for k := range byReason {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if byReason[names[i]] != byReason[names[j]] {
			return byReason[names[i]] > byReason[names[j]]
		}
		return names[i] < names[j]
	})
	t.Log("  by reason:")
	for _, n := range names {
		mark := "gap "
		if reasons[n].bySpec {
			mark = "spec"
		}
		t.Logf("    %s %-20s %3d", mark, n, byReason[n])
	}

	// A reason nobody cites is dead weight that will be read as still
	// applying.
	for name := range reasons {
		if byReason[name] == 0 {
			t.Errorf("reason %q is defined but no case cites it; remove it", name)
		}
	}
}

// ---- comparison helpers ----

// jsonShape converts a parsed value into the shapes encoding/json
// produces, so it can be compared with the suite's in.json.
func jsonShape(v any) any {
	switch t := v.(type) {
	case *value.Map:
		out := make(map[string]any, t.Len())
		for _, e := range t.Entries() {
			out[value.KeyString(e.Key)] = jsonShape(e.Val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = jsonShape(x)
		}
		return out
	case int64:
		return float64(t)
	case int:
		return float64(t)
	case []byte:
		// The suite keeps binary as its base64 source, because JSON has no
		// binary type. Decoding is correct, so the comparison re-encodes
		// rather than calling it a difference.
		return base64.StdEncoding.EncodeToString(t)
	case time.Time:
		return t.Format(time.RFC3339Nano)
	}
	return v
}

// decodeJSONStream reads the suite's in.json, which holds one JSON value
// per document with nothing between them.
func decodeJSONStream(s string) ([]any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	var out []any
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return out, err
		}
		out = append(out, v)
	}
}

func sameJSON(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !sameJSON(xv, yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameJSON(x[i], y[i]) {
				return false
			}
		}
		return true
	case float64:
		y, ok := b.(float64)
		if !ok {
			return false
		}
		if math.IsNaN(x) && math.IsNaN(y) {
			return true
		}
		return x == y
	}
	return a == b
}

func pct(n, of int) float64 {
	if of == 0 {
		return 0
	}
	return 100 * float64(n) / float64(of)
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

func indented(s string) string {
	var b strings.Builder
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("    | " + ln + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// regenerateTable prints the deviation table as it would be for the
// parser's current behaviour, carrying forward the reason already recorded
// for each case.
func regenerateTable(t *testing.T, cases []suiteCase, index map[string]deviation) {
	t.Helper()
	var b strings.Builder
	var unclassified []string
	counts := map[string]int{}

	for _, c := range cases {
		got := runSuiteCase(c)
		if got.agrees {
			continue
		}
		reason := "gapAnchor"
		if d, ok := index[c.ID]; ok && d.Kind == got.kind {
			reason = d.Reason
		} else {
			unclassified = append(unclassified, fmt.Sprintf("%s (%s) %s\n  %s", c.ID, c.Name, got.kind, got.detail))
		}
		counts[reason]++
		fmt.Fprintf(&b, "\t{%q, %s, %s},\n", c.ID, kindName(got.kind), reason)
	}

	t.Logf("deviation table, regenerated:\n\n%s", b.String())
	if len(unclassified) > 0 {
		t.Logf("%d case(s) need a reason chosen by hand:", len(unclassified))
		for _, u := range unclassified {
			t.Logf("  %s", u)
		}
	}
	t.Fatal("regeneration mode: the table above was printed, not applied")
}

func kindName(k devKind) string {
	switch k {
	case devRejects:
		return "devRejects"
	case devAccepts:
		return "devAccepts"
	}
	return "devValue"
}
