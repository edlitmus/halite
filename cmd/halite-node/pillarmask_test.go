package main

import (
	"testing"

	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/value"
)

// Pillar output masks the secrets in it on the way to the screen, and
// nothing else. DIVERGENCE 5.256.
//
// `pillar items` once printed every secret the tree carries in clear
// (5.88). The fix for that replaced every non-empty string, which on an
// estate whose pillar is mostly addresses, host names and paths made the
// command useless for checking what a state will see. What is masked now
// is what the redactor holds: what the hub decrypted, and what an external
// pillar source such as AWS Secrets Manager returned.

func held(values ...string) *redact.Set {
	s := redact.New()
	for _, v := range values {
		s.Add(v)
	}
	return s
}

func TestPillarOutputMasksOnlyTheSecrets(t *testing.T) {
	in := value.MapOf(
		"smtp_password", "s3cret-value",
		"pin", "4821",
		"listen", "127.0.0.1:9093",
		"nested", value.MapOf(
			"host", "mail.example",
			"dsn", "postgres://app:s3cret-value@db.example/app",
			"empty", ""),
		"list", []any{"s3cret-value", "plain", int64(7)},
		"port", int64(5432),
		"enabled", true,
		"absent", nil,
	)
	// "4821" is shorter than the six characters Scrub will remove from
	// text, and is still a whole pillar value that was encrypted.
	got := maskPillar(in, false, held("s3cret-value", "4821")).(*value.Map)

	want := map[string]any{
		"smtp_password": redact.Placeholder,
		"pin":           redact.Placeholder,
		"listen":        "127.0.0.1:9093",
		"port":          int64(5432),
		"enabled":       true,
		"absent":        nil,
	}
	for k, w := range want {
		if v, _ := got.GetString(k); v != w {
			t.Errorf("%s = %v, want %v", k, v, w)
		}
	}

	nested, _ := got.GetString("nested")
	nm := nested.(*value.Map)
	if v, _ := nm.GetString("host"); v != "mail.example" {
		t.Errorf("a plain host name was masked into %v", v)
	}
	if v, _ := nm.GetString("dsn"); v != "postgres://<redacted>@db.example/app" {
		t.Errorf("a string with a secret inside it = %v, want the secret gone and the host kept", v)
	}
	if v, _ := nm.GetString("empty"); v != "" {
		t.Errorf("an empty string became %v", v)
	}

	list, _ := got.GetString("list")
	items := list.([]any)
	if items[0] != redact.Placeholder || items[1] != "plain" || items[2] != int64(7) {
		t.Errorf("list = %#v, want the secret masked and the rest as it was", items)
	}
}

// A hub too old to name its secrets leaves the node holding every value
// (seedPillarSecrets), and then every string is masked, as before: the
// safe side of not knowing.
func TestWithEveryValueHeldEveryStringIsMasked(t *testing.T) {
	in := value.MapOf("listen", "127.0.0.1:9093", "nested", value.MapOf("host", "mail.example"), "port", int64(1))
	all := redact.New()
	all.AddTree(in)

	got := maskPillar(in, false, all).(*value.Map)
	if v, _ := got.GetString("listen"); v != redact.Placeholder {
		t.Errorf("listen = %v, want it masked", v)
	}
	nested, _ := got.GetString("nested")
	if v, _ := nested.(*value.Map).GetString("host"); v != redact.Placeholder {
		t.Errorf("host = %v, want it masked", v)
	}
	if v, _ := got.GetString("port"); v != int64(1) {
		t.Errorf("port = %v, want the number unchanged", v)
	}
}

// The keys are the half of the tree the command is actually for: a state
// that cannot find `foxpass:api_key` is debugged by seeing the key
// exist, not by reading it.
func TestPillarMaskingKeepsTheShape(t *testing.T) {
	in := value.MapOf("foxpass", value.MapOf("api_key", "s3cret", "bind_pw", "other"))
	got := maskPillar(in, false, held("s3cret", "other")).(*value.Map)

	outer, ok := got.GetString("foxpass")
	if !ok {
		t.Fatal("the foxpass key did not survive masking")
	}
	inner, ok := outer.(*value.Map)
	if !ok {
		t.Fatalf("foxpass is %T, want a map", outer)
	}
	for _, k := range []string{"api_key", "bind_pw"} {
		if v, ok := inner.GetString(k); !ok || v != redact.Placeholder {
			t.Errorf("the %s key did not survive masking with its value masked: %v", k, v)
		}
	}
}

// `--reveal` prints everything, secrets included. Asking for it is the
// point: the disclosure becomes deliberate.
func TestRevealReturnsThePillarUntouched(t *testing.T) {
	in := value.MapOf("api_key", "s3cret-value")
	got, ok := maskPillar(in, true, held("s3cret-value")).(*value.Map)
	if !ok {
		t.Fatalf("reveal returned %T, want a map", maskPillar(in, true, nil))
	}
	if v, _ := got.GetString("api_key"); v != "s3cret-value" {
		t.Errorf("--reveal gave %v, want the real value", v)
	}
}
