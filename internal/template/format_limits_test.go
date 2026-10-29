package template

import (
	"strings"
	"testing"
)

// A width or precision too large to allocate is an error, not an
// allocation. Before the bound, strconv.Atoi's error was discarded and
// `{:.9223372036854775807f}` asked for a MaxInt-digit string; run under
// a 1G memory limit it was killed (exit 137), and without one it took
// the machine's memory with it.
func TestFormatSpecNumbersAreBounded(t *testing.T) {
	for _, src := range []string{
		"{{ '{:>99999999999999999999}'.format('x') }}",
		"{{ '%99999999999999999999s'|format('x') }}",
		"{{ '{:.99999999999999999999}'.format('x') }}",
		"{{ '{:>9223372036854775807}'.format('x') }}",
		"{{ '{:.9223372036854775807f}'.format(1.5) }}",
		"{{ '%.9223372036854775807f'|format(1.5) }}",
		"{{ '{:>67108865}'.format('x') }}",
	} {
		_, err := NewEnvironment(nil, DefaultOptions()).RenderString(src, "t", nil)
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Errorf("%s: err = %v, want a 'too large' refusal", src, err)
		}
	}
}

// The bound does not refuse what a render could always do.
func TestFormatSpecNumbersBelowTheBoundStillWork(t *testing.T) {
	res, err := NewEnvironment(nil, DefaultOptions()).RenderString(
		"{{ '{:>8}'.format('x') }}|{{ '%.3f'|format(1.5) }}", "t", nil)
	if err != nil || res.Output != "       x|1.500" {
		t.Fatalf("got %q, %v", res.Output, err)
	}
}
