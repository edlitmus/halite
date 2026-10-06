package yaml

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A `%` in column zero is read as PyYAML reads it: a directive, wherever
// a token can start, ending the document it follows -- except inside a
// multi-line plain scalar, where it is more of the text.
//
// Each expectation is what PyYAML 6.0.3's SafeLoader, the base of Salt's
// loader, returned for the same input, as JSON documents; "" is a
// refusal. halite read the directive as a plain scalar, so MUS6/01 came
// out as the string "%YAML 1.2" rather than two null documents, and the
// suite table cannot see that -- it records only whether a document was
// accepted. DIVERGENCE 5.226.
func TestAColumnZeroDirectiveIsReadAsPyYAMLReadsIt(t *testing.T) {
	for src, want := range map[string]string{
		"---\nscalar1 # comment\n%YAML 1.2\n---\nscalar2\n": `"scalar1" "scalar2"`,
		"---\nkey: value\n%YAML 1.2\n---\n":                 `{"key":"value"} null`,
		"%YAML 1.2\n---\n%YAML 1.2\n---\n":                  `null null`,
		"a: b\n%YAML 1.2\n---\nc\n":                         `{"a":"b"} "c"`,
		"- a\n%YAML 1.2\n---\nc\n":                          `["a"] "c"`,
		"key: value\n%foo: bar\n":                           "",
		"a: |\n  x\n%y\n":                                   "",
		"---\na\n%YAML 1.2\n---\nb\n":                       `"a %YAML 1.2" "b"`,
		"a: b\n  %c\n":                                      `{"a":"b %c"}`,
	} {
		docs, _, err := ParseStream([]byte(src), Options{Stream: true})
		if want == "" {
			if err == nil {
				t.Errorf("%q: read, where PyYAML refuses it", src)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: refused (%v), where PyYAML reads %s", src, err, want)
			continue
		}
		var got []string
		for _, d := range docs {
			b, _ := value.EncodeJSON(d, 0)
			got = append(got, string(b))
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%q: read as %s, where PyYAML reads %s", src, strings.Join(got, " "), want)
		}
	}
}
