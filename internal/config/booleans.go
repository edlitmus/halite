package config

import (
	"fmt"
	"strconv"

	"github.com/edlitmus/halite/internal/value"
)

// failClosedBooleans are the boolean keys whose reader takes a value it
// cannot read as true rather than as the default, because the default is
// the unsafe side. Only `quiesce` is one: a node that was asked to stop
// automation and could not read the request must not run everything
// (cmd/halite-node/quiesce.go). The loader's warning has to say what the
// reader does, so the two are listed together here.
var failClosedBooleans = map[string]bool{"quiesce": true}

// readBool is the one reading of a boolean setting that Bool and the
// loader's check share: a YAML boolean, or a string strconv.ParseBool
// accepts. An integer is not one, though `1` looks like it, because
// Bool has never read it and starting to now would change what an
// existing file means without anybody deciding it should.
func readBool(v any) (b bool, ok bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		if b, err := strconv.ParseBool(t); err == nil {
			return b, true
		}
	}
	return false, false
}

// unreadableBooleans warns about every boolean setting whose value Bool
// cannot read.
//
// Bool returns its default for such a value, and nothing else looked, so
// `metrics: maybe` or `evidence: 1` loaded without a word and quietly
// became the default -- which for `quiesce` was a node that ran
// everything it was asked to stop running. A key is boolean when its
// table default is "true" or "false". defaults_audit_test.go holds each
// cfg.Bool read to its key's declared default, and
// TestEveryBooleanReadIsCheckedByTheLoader holds that default to being
// one of those two words, so a boolean read cannot escape this check.
//
// A warning and not a refusal, deliberately: a node that refuses to start
// over a typo is a node nobody can reach to fix the typo, and the change
// that would fix it arrives through the node it stopped. The warning names
// the value the process will actually use, so it is the answer to "why
// is this setting not doing what I wrote".
func unreadableBooleans(role Role, values *value.Map) []string {
	var out []string
	for _, k := range KeysFor(role) {
		if k.Default != "true" && k.Default != "false" {
			continue
		}
		v, ok := values.Get(k.Name)
		if !ok || v == nil {
			continue
		}
		if _, readable := readBool(v); readable {
			continue
		}
		applied := k.Default
		if failClosedBooleans[k.Name] {
			applied = "true"
		}
		out = append(out, fmt.Sprintf(
			"configuration key %q is set to %v, which is neither true nor false, so it is ignored and %s applies",
			k.Name, v, applied))
	}
	return out
}
