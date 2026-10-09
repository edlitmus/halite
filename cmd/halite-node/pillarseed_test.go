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

// The hub names the values it decrypted, and only those are masked: a
// plain address in the same pillar stays readable in what the node
// prints. With no list, from a hub older than it, every value is masked
// as before. DIVERGENCE 5.251.
func TestOnlyTheValuesTheHubNamesAreMasked(t *testing.T) {
	decoded, err := value.DecodeJSON([]byte(`{"address":"172.29.231.69","token":"tok3n-from-the-hub"}`))
	if err != nil {
		t.Fatal(err)
	}
	pillar := decoded.(*value.Map)
	line := "deploying with tok3n-from-the-hub to 172.29.231.69"

	n := &node{secrets: redact.New()}
	named := []string{"tok3n-from-the-hub"}
	n.seedPillarSecrets(pillar, &named)
	got := n.secrets.Scrub(line)
	if strings.Contains(got, "tok3n-from-the-hub") {
		t.Errorf("a value the hub named is printable: %q", got)
	}
	if !strings.Contains(got, "172.29.231.69") {
		t.Errorf("a plain pillar value was masked although the hub did not name it: %q", got)
	}

	old := &node{secrets: redact.New()}
	old.seedPillarSecrets(pillar, nil)
	if got := old.secrets.Scrub(line); strings.Contains(got, "tok3n-from-the-hub") || strings.Contains(got, "172.29.231.69") {
		t.Errorf("with no list every value should be masked, as before: %q", got)
	}
}

// The agentless path installed the pillar the hub pushed and seeded the
// redactor with nothing, so a decrypted value was printable in that run's
// output. DIVERGENCE 5.251.
func TestAnAgentlessRunSeedsTheRedactorFromThePushedPillar(t *testing.T) {
	n := &node{secrets: redact.New()}
	named := []string{"tok3n-pushed-by-the-hub"}
	err := applyOneshotContent(n, OneshotRequest{
		Pillar:  []byte(`{"address":"10.11.12.13","token":"tok3n-pushed-by-the-hub"}`),
		Secrets: &named,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := n.secrets.Scrub("tok3n-pushed-by-the-hub at 10.11.12.13")
	if strings.Contains(got, "tok3n-pushed-by-the-hub") {
		t.Errorf("the pushed secret is printable in an agentless run: %q", got)
	}
	if !strings.Contains(got, "10.11.12.13") {
		t.Errorf("a plain value was masked: %q", got)
	}
}
