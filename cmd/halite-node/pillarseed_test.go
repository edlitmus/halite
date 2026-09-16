package main

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/value"
)

// The node seeds its redactor with the pillar the hub sends, because it
// cannot tell which of those values arrived encrypted:
//
//	decoded, err := value.DecodeJSON(res.Pillar)
//	m, ok := decoded.(*value.Map)
//	n.secrets.AddTree(m)
//
// Those three lines compiled and ran and recorded nothing, for as long
// as AddTree knew `map[string]any` and a decoded pillar has never been
// one. Nothing failed: the set was empty, `Len()` agreed it was empty,
// and every decrypted pillar value stayed printable in every comment,
// job return and log record on the node.
//
// So this test asserts the seam rather than AddTree — the type that
// crosses the wire has to be a type the redactor walks, and a unit test
// of either half says nothing about that.
func TestTheHubsPillarSeedsTheRedactor(t *testing.T) {
	// The shape the hub sends: JSON on the wire, decoded as the node
	// decodes it.
	const wire = `{"base.repo":{"repo":{"artifactory":` +
		`{"username":"deploy-user","authorization":"tok3n-from-the-hub"}}},` +
		`"port":5432}`

	decoded, err := value.DecodeJSON([]byte(wire))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := decoded.(*value.Map)
	if !ok {
		t.Fatalf("a decoded pillar is %T, which is not what the node asserts", decoded)
	}

	secrets := redact.New()
	secrets.AddTree(m)

	if secrets.Len() == 0 {
		t.Fatal("the hub's pillar seeded nothing; every value in it stays printable")
	}
	got := secrets.Scrub("fetching https://deploy-user:tok3n-from-the-hub@artifacts.example.com/x")
	if strings.Contains(got, "tok3n-from-the-hub") {
		t.Errorf("the pillar's token is still printable: %q", got)
	}
}
