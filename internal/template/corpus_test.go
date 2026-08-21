package template

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A corpus of Jinja templates with expected output, required by SPEC
// section 31: "a corpus of Jinja templates with expected output, including
// the Jinja project's own test cases where they apply to the supported
// subset."
//
// Two corpora run here. One is lifted mechanically out of Jinja's own
// pytest suite; the other is written here, for the surface Jinja does not
// have — Salt's added filters, the strict undefined of SPEC 10.2.6, the
// deterministic seeding of 10.2.4, and the context of 10.2.7.
//
// Disagreements are recorded the same way the YAML suite records them: a
// row per case saying which way it disagrees and why, split into what the
// subset deliberately leaves out and what is a defect. The table is
// enforced in both directions, so a case that stops disagreeing fails as
// loudly as one that starts.

const (
	jinjaCorpusFile  = "testdata/jinja-corpus/cases.json"
	haliteCorpusFile = "testdata/jinja-corpus/halite.json"
)

// corpusCase is one template, its context, and what it should render to.
type corpusCase struct {
	ID       string
	Template string
	Context  map[string]any
	Expected string
	// Error, when set, is a fragment the render error must contain. A case
	// carrying it asserts a refusal rather than an output.
	Error string
	// Permissive runs the case with the undefined mode of Salt rather than
	// halite's strict default.
	Permissive bool
	// Options are the environment settings the case was written against.
	// A Jinja test that builds its own Environment carries them, and
	// without them a `lstrip_blocks` case measures the wrong engine and
	// reads as a defect in this one.
	Options map[string]any
}

// loadCorpus reads a corpus through halite's own JSON decoder, which keeps
// an integer an integer. encoding/json turns every number into a float64,
// and a template rendering 42 as "42.0" would look like a defect in the
// engine when it is one in the corpus.
func loadCorpus(t *testing.T, path string) []corpusCase {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v\nrun tools/vendor-jinja-corpus.sh to fetch it", path, err)
	}
	v, err := value.DecodeJSON(raw)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("%s does not hold a list", path)
	}
	out := make([]corpusCase, 0, len(list))
	for i, item := range list {
		m, ok := item.(*value.Map)
		if !ok {
			t.Fatalf("%s case %d is not a mapping", path, i)
		}
		var c corpusCase
		str := func(k string) string {
			if s, ok := m.Get(k); ok {
				s, _ := s.(string)
				return s
			}
			return ""
		}
		c.ID, c.Template, c.Expected, c.Error = str("id"), str("template"), str("expected"), str("error")
		if b, ok := m.Get("permissive"); ok {
			c.Permissive, _ = b.(bool)
		}
		if o, ok := m.Get("options"); ok {
			if om, ok := o.(*value.Map); ok {
				c.Options = map[string]any{}
				for _, e := range om.Entries() {
					c.Options[value.KeyString(e.Key)] = e.Val
				}
			}
		}
		c.Context = map[string]any{}
		if ctx, ok := m.Get("context"); ok {
			if cm, ok := ctx.(*value.Map); ok {
				for _, e := range cm.Entries() {
					c.Context[value.KeyString(e.Key)] = e.Val
				}
			}
		}
		if c.ID == "" {
			t.Fatalf("%s case %d has no id", path, i)
		}
		if _, ok := m.Get("error"); ok && c.Error == "" {
			// "expect some error" is not an assertion: it passes for a
			// typo in the template as readily as for the refusal the case
			// is about.
			t.Fatalf("%s case %s asks for an error but names no fragment of it", path, c.ID)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		t.Fatalf("%s holds no cases", path)
	}
	return out
}

// reason explains a class of disagreement. bySpec marks the ones where
// halite deliberately is not Jinja.
type corpusReason struct {
	bySpec bool
	text   string
}

var corpusReasons = map[string]corpusReason{
	specNotInSubset: {true,
		"the construct is outside the subset of SPEC 10.2. The subset is defined by what Salt " +
			"trees contain, and an SLS file is not HTML, so the markup filters and the i18n " +
			"statements are not there."},
	specNoAutoescape: {true,
		"SPEC 10.2.2 parses `autoescape` and does nothing with it, because SLS output is not " +
			"HTML. Escaping is available through explicit filters."},
	specStrictUndefined: {true,
		"SPEC 10.2.6 makes an undefined name an error where Jinja renders it as empty. The " +
			"case renders correctly under `undefined: permissive`, which is the transition " +
			"Salt trees migrate through."},
	specPythonMethod: {true,
		"the template calls a Python string or dict method. halite has filters, not the " +
			"Python object model, and no code path that reaches an arbitrary method."},

	gapWhitespaceControl: {false,
		"the `+` whitespace marker, which turns off `trim_blocks` or `lstrip_blocks` for one " +
			"delimiter, is not parsed."},
	gapCallResult: {false,
		"the result of a filter or a subscript cannot be called."},
	gapNumericAttribute: {false,
		"the Django-style `a.0` spelling of a subscript is not parsed."},
	gapTestArgument: {false,
		"a test taking an argument is not parsed in every position."},
	gapScoping: {false,
		"a name's scope differs from Jinja's, usually around `set` inside a loop or a block."},
	gapRendering: {false,
		"a value renders differently from Jinja: tuple and dict spelling, float formatting, " +
			"and the like."},
	gapFilterBehaviour: {false,
		"a filter halite has behaves differently from Jinja's."},
	gapOther: {false,
		"an unclassified difference."},
}

const (
	specNotInSubset     = "specNotInSubset"
	specNoAutoescape    = "specNoAutoescape"
	specStrictUndefined = "specStrictUndefined"
	specPythonMethod    = "specPythonMethod"

	gapCallResult        = "gapCallResult"
	gapFilterBehaviour   = "gapFilterBehaviour"
	gapNumericAttribute  = "gapNumericAttribute"
	gapOther             = "gapOther"
	gapRendering         = "gapRendering"
	gapScoping           = "gapScoping"
	gapTestArgument      = "gapTestArgument"
	gapWhitespaceControl = "gapWhitespaceControl"
)

type corpusDeviation struct {
	ID     string
	Reason string
}

// corpusDeviations is the expected-failure set for Jinja's own tests.
var corpusDeviations = []corpusDeviation{
	{"test_api::test_item_and_attribute", specPythonMethod},
	{"test_api::test_item_and_attribute#1", gapCallResult},
	{"test_api::test_custom_code_generator", gapRendering},
	{"test_api::test_custom_context", gapOther},
	{"test_core_tags::test_else", specStrictUndefined},
	{"test_core_tags::test_empty_blocks", specStrictUndefined},
	{"test_core_tags::test_loop_errors", specStrictUndefined},
	{"test_core_tags::test_with_argument_scoping", gapRendering},
	{"test_ext::test_extend_late", specNoAutoescape},
	{"test_ext::test_scoping", specNoAutoescape},
	{"test_filters::test_indent_width_string", gapFilterBehaviour},
	{"test_filters::test_title#1", gapFilterBehaviour},
	{"test_filters::test_urlize", specNotInSubset},
	{"test_filters::test_urlize#1", specNotInSubset},
	{"test_filters::test_urlize#2", specNotInSubset},
	{"test_filters::test_urlize#3", specNotInSubset},
	{"test_filters::test_urlize_rel_policy", specNotInSubset},
	{"test_filters::test_urlize_target_parameter", specNotInSubset},
	{"test_filters::test_urlize_extra_schemes_parameter", specNotInSubset},
	{"test_filters::test_sum_attributes_tuple", specPythonMethod},
	{"test_filters::test_unique", specPythonMethod},
	{"test_filters::test_groupby_tuple_index", gapNumericAttribute},
	{"test_inheritance::test_reuse_blocks", gapOther},
	{"test_lexnparse::test_call", gapOther},
	{"test_lexnparse::test_django_attr", gapNumericAttribute},
	{"test_lexnparse::test_short_conditional_expression", specStrictUndefined},
	{"test_lexnparse::test_lstrip_trim", gapRendering},
	{"test_lexnparse::test_comment_trim", gapRendering},
	{"test_lexnparse::test_comment_no_trim", gapRendering},
	{"test_lexnparse::test_multiple_comment_trim_lstrip", gapRendering},
	{"test_lexnparse::test_multiple_comment_no_trim_lstrip", gapRendering},
	{"test_lexnparse::test_raw_trim_lstrip", gapRendering},
	{"test_lexnparse::test_raw_no_trim_lstrip", gapWhitespaceControl},
	{"test_regression::test_urlize_filter_escaping", specNotInSubset},
	{"test_regression::test_urlize_filter_closing_punctuation", specNotInSubset},
	{"test_regression::test_nested_for_else", specStrictUndefined},
	{"test_regression::test_double_caller", gapRendering},
	{"test_regression::test_pass_context_loop_vars", gapScoping},
	{"test_regression::test_pass_context_scoped_loop_vars", gapScoping},
	{"test_regression::test_pass_context_in_blocks", gapScoping},
	{"test_regression::test_pass_context_block_and_loop", gapScoping},
	{"test_regression::test_pass_context_with_select", gapOther},
	{"test_security::test_basic_format_safety", specPythonMethod},
	{"test_security::test_basic_format_all_okay", specPythonMethod},
	{"test_security::test_safe_format_safety", specNoAutoescape},
	{"test_security::test_safe_format_all_okay", specNoAutoescape},
	{"test_security::test_empty_braces_format#1", specNoAutoescape},
	{"test_security::test_basic_format_safety#1", specPythonMethod},
	{"test_security::test_basic_format_all_okay#1", specPythonMethod},
	{"test_security::test_safe_format_all_okay#1", specNoAutoescape},
	{"test_tests::test_multiple_tests", gapTestArgument},
	{"test_tests::test_in", gapTestArgument},
}

func corpusIndex(t *testing.T) map[string]corpusDeviation {
	t.Helper()
	out := make(map[string]corpusDeviation, len(corpusDeviations))
	for _, d := range corpusDeviations {
		if prev, dup := out[d.ID]; dup {
			t.Fatalf("case %s has two rows: %v and %v", d.ID, prev.Reason, d.Reason)
		}
		if _, ok := corpusReasons[d.Reason]; !ok {
			t.Fatalf("case %s cites reason %q, which is not defined", d.ID, d.Reason)
		}
		out[d.ID] = d
	}
	return out
}

// renderCase renders one case and reports whether it matched.
func renderCase(c corpusCase, undef UndefinedMode) (out string, err error) {
	opts := DefaultOptions()
	opts.Undefined = undef
	applyCorpusOptions(&opts, c.Options)
	env := NewEnvironment(nil, opts)
	res, err := env.RenderString(c.Template, c.ID, c.Context)
	if err != nil {
		return "", err
	}
	return res.Output, nil
}

// applyCorpusOptions maps the environment settings a case carries onto
// halite's own. A Jinja option halite does not have is dropped by the
// extractor, so anything arriving here is one it does.
func applyCorpusOptions(opts *Options, set map[string]any) {
	boolOpt := func(name string, dst *bool) {
		if v, ok := set[name]; ok {
			b, _ := v.(bool)
			*dst = b
		}
	}
	strOpt := func(name string, dst *string) {
		if v, ok := set[name]; ok {
			if s, ok := v.(string); ok {
				*dst = s
			}
		}
	}
	boolOpt("trim_blocks", &opts.TrimBlocks)
	boolOpt("lstrip_blocks", &opts.LstripBlocks)
	boolOpt("keep_trailing_newline", &opts.KeepTrailingNewline)
	strOpt("variable_start_string", &opts.Delimiters.VarStart)
	strOpt("variable_end_string", &opts.Delimiters.VarEnd)
	strOpt("block_start_string", &opts.Delimiters.BlockStart)
	strOpt("block_end_string", &opts.Delimiters.BlockEnd)
	strOpt("comment_start_string", &opts.Delimiters.CommentStart)
	strOpt("comment_end_string", &opts.Delimiters.CommentEnd)
}

type corpusOutcome struct {
	agrees bool
	// undefinedOnly marks a case that renders correctly under the
	// permissive undefined of Salt and not under halite's strict default,
	// which is exactly the divergence SPEC 10.2.6 declares.
	undefinedOnly bool
	detail        string
}

func runCorpusCase(c corpusCase) corpusOutcome {
	mode := Strict
	if c.Permissive {
		mode = Permissive
	}
	got, err := renderCase(c, mode)

	if c.Error != "" {
		switch {
		case err == nil:
			return corpusOutcome{detail: fmt.Sprintf("rendered %q; expected an error naming %q", got, c.Error)}
		case !strings.Contains(err.Error(), c.Error):
			return corpusOutcome{detail: fmt.Sprintf("error %q does not mention %q", err, c.Error)}
		}
		return corpusOutcome{agrees: true}
	}

	if err == nil && got == c.Expected {
		return corpusOutcome{agrees: true}
	}
	if !c.Permissive {
		if got2, err2 := renderCase(c, Permissive); err2 == nil && got2 == c.Expected {
			return corpusOutcome{undefinedOnly: true,
				detail: fmt.Sprintf("renders under permissive undefined: %q", got2)}
		}
	}
	return corpusOutcome{detail: fmt.Sprintf("want %q\n     got  %q\n     err  %v", c.Expected, got, err)}
}

// Every case from Jinja's own tests either agrees or has a row saying why
// not, checked in both directions.
func TestJinjaCorpus(t *testing.T) {
	cases := loadCorpus(t, jinjaCorpusFile)
	index := corpusIndex(t)
	seen := map[string]bool{}

	// Fixing a gap moves many rows at once, so the table can be rebuilt:
	//
	//	HALITE_JINJA_CORPUS_REGEN=1 go test ./internal/template/ -run TestJinjaCorpus
	//
	// It carries forward the reason already recorded and marks a newly
	// deviating case for a person to classify, then fails deliberately.
	if os.Getenv("HALITE_JINJA_CORPUS_REGEN") != "" {
		regenerateCorpusTable(t, cases, index)
		return
	}

	for _, c := range cases {
		c := c
		t.Run(corpusSubtestName(c.ID), func(t *testing.T) {
			got := runCorpusCase(c)
			d, recorded := index[c.ID]
			if recorded {
				seen[c.ID] = true
			}

			switch {
			case got.agrees && !recorded:
			case got.agrees && recorded:
				t.Errorf("%s no longer deviates, but the table still records it as %q.\n"+
					"Remove its row: a stale expected failure hides the next real one.", c.ID, d.Reason)
			case !got.agrees && !recorded:
				t.Errorf("%s disagrees and the table does not record it.\n  template %q\n  %s\n\n"+
					"Either fix the engine or add a row to corpusDeviations with the reason.",
					c.ID, c.Template, got.detail)
			case got.undefinedOnly && d.Reason != specStrictUndefined:
				t.Errorf("%s is recorded as %q but differs only by the undefined policy.\n  %s",
					c.ID, d.Reason, got.detail)
			case !got.undefinedOnly && d.Reason == specStrictUndefined:
				t.Errorf("%s is recorded as a strict-undefined difference but fails permissively too.\n  %s",
					c.ID, got.detail)
			}
		})
	}

	for id := range index {
		if !seen[id] {
			t.Errorf("the table records case %s, which the corpus does not contain.\n"+
				"It was probably renamed or dropped upstream; remove the row.", id)
		}
	}
}

// The corpus written here covers what Jinja's tests cannot: Salt's added
// filters, the strict undefined of SPEC 10.2.6, deterministic seeding, and
// the refusals the subset owes an operator. Every case is expected to
// pass; there is no deviation table, because a case that does not pass is
// a case this project got wrong.
func TestHaliteTemplateCorpus(t *testing.T) {
	for _, c := range loadCorpus(t, haliteCorpusFile) {
		c := c
		t.Run(corpusSubtestName(c.ID), func(t *testing.T) {
			if got := runCorpusCase(c); !got.agrees {
				t.Errorf("%s\n  template %q\n  %s", c.ID, c.Template, got.detail)
			}
		})
	}
}

func TestJinjaCorpusSummary(t *testing.T) {
	cases := loadCorpus(t, jinjaCorpusFile)
	index := corpusIndex(t)

	byReason := map[string]int{}
	var bySpec, gaps int
	for _, d := range index {
		byReason[d.Reason]++
		if corpusReasons[d.Reason].bySpec {
			bySpec++
			continue
		}
		gaps++
	}
	agree := len(cases) - len(index)
	t.Logf("Jinja's own test cases: %d", len(cases))
	t.Logf("  agree:                           %d (%.1f%%)", agree, corpusPct(agree, len(cases)))
	t.Logf("  outside the subset, per SPEC 10.2: %d", bySpec)
	t.Logf("  gaps:                            %d", gaps)
	t.Logf("  conformance where halite claims to conform: %.1f%%",
		corpusPct(agree, len(cases)-bySpec))

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
		if corpusReasons[n].bySpec {
			mark = "spec"
		}
		t.Logf("    %s %-22s %3d", mark, n, byReason[n])
	}

	for name := range corpusReasons {
		if byReason[name] == 0 {
			t.Errorf("reason %q is defined but no case cites it; remove it", name)
		}
	}

	t.Logf("halite's own corpus: %d cases", len(loadCorpus(t, haliteCorpusFile)))
}

func regenerateCorpusTable(t *testing.T, cases []corpusCase, index map[string]corpusDeviation) {
	t.Helper()
	var b strings.Builder
	var unclassified []string
	for _, c := range cases {
		got := runCorpusCase(c)
		if got.agrees {
			continue
		}
		reason := "REASON_UNCLASSIFIED"
		switch {
		case got.undefinedOnly:
			reason = specStrictUndefined
		default:
			if d, ok := index[c.ID]; ok && d.Reason != specStrictUndefined {
				reason = d.Reason
			} else {
				unclassified = append(unclassified,
					fmt.Sprintf("%s\n    template %q\n    %s", c.ID, c.Template, got.detail))
			}
		}
		fmt.Fprintf(&b, "\t{%q, %s},\n", c.ID, reason)
	}
	t.Logf("corpus deviation table, regenerated:\n\n%s", b.String())
	if len(unclassified) > 0 {
		t.Logf("%d case(s) need a reason chosen by hand:", len(unclassified))
		for _, u := range unclassified {
			t.Logf("  %s", u)
		}
	}
	t.Fatal("regeneration mode: the table above was printed, not applied")
}

func corpusSubtestName(id string) string {
	r := strings.NewReplacer("::", "_", "/", "_", " ", "_", "#", "_")
	return r.Replace(id)
}

func corpusPct(n, of int) float64 {
	if of == 0 {
		return 0
	}
	return 100 * float64(n) / float64(of)
}

var _ = json.Marshal
