package value

import (
	"fmt"
	"strconv"
	"time"
)

// ParseDuration reads a duration the way a tree writes one.
//
// Two spellings, both accepted, because both appear in a real tree and
// `signature.Duration` promises both — "a Go duration string or a bare
// number of seconds":
//
//   - `15m`, `2h30m`, `90s`: Go's own form, which is what an operator
//     writing a new tree tends to use.
//   - `900`, `"900"`, `900.5`: a bare number of seconds, which is what
//     Salt takes and therefore what a migrated tree carries.
//
// The quoted form is not a curiosity. A template produces strings and
// nothing else, so `timeout: {{ pillar['deploy_timeout'] }}` reaches a
// module as `"900"` however the pillar spelled it — which is how a tree
// that looks entirely numeric arrives here as text.
//
// # Why this is one function
//
// It was two, and they disagreed. `internal/state`'s compiler accepted a
// bare number and `internal/builtin`'s `cmd.run` did not, so the same word
// in the same SLS file meant a deadline to the compiler and an error to the
// module — and the module *discarded* the error, leaving `Timeout` at zero,
// which `exec.OSRunner` reads as "no deadline at all". A state asking for
// `timeout: "900"` got an unbounded command: the one outcome the module's
// own refusal of `bg` with `timeout` says it must never produce, on the
// grounds that "a tree that asked for a bounded run and got an unbounded
// one has been told the opposite of the truth". DIVERGENCE 5.137.
//
// So the rule lives once, in the package every caller already imports, and
// a caller that cannot use a value says so rather than proceeding without
// it.
func ParseDuration(v any) (time.Duration, error) {
	switch t := v.(type) {
	case time.Duration:
		return t, nil
	case int:
		return time.Duration(t) * time.Second, nil
	case int64:
		return time.Duration(t) * time.Second, nil
	case float64:
		return time.Duration(t * float64(time.Second)), nil
	case string:
		if d, err := time.ParseDuration(t); err == nil {
			return d, nil
		}
		// A bare number is seconds. Parsed as a float so that `0.5` means
		// half a second rather than being refused: the number came from a
		// YAML scalar or a template, and neither promises an integer.
		if n, err := strconv.ParseFloat(t, 64); err == nil {
			return time.Duration(n * float64(time.Second)), nil
		}
		return 0, fmt.Errorf("%q is not a duration: write it as a number of seconds, or with a unit such as 15m", t)
	case nil:
		return 0, fmt.Errorf("a duration is missing")
	}
	return 0, fmt.Errorf("%s is not a duration: write it as a number of seconds, or with a unit such as 15m",
		TypeName(v))
}
