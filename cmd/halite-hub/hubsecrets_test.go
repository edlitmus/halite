package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/roster"
)

// The hub's redactor is what `h.secrets.Add` is reached through when
// pillar is compiled and when an external source answers. A context
// built without one does not degrade: `Add` takes a pointer receiver
// and locks, so a nil set panics at the first secret the hub decrypts
// -- which is the first time it matters and the worst time to find out.
func TestTheHubContextCarriesARedactor(t *testing.T) {
	// A root with no configuration in it: AllowMissing means the hub
	// still builds, which is all this needs.
	args := &cli.Args{Flags: map[string]string{"root": t.TempDir()}}

	h := openHubForConfig(args)
	if h.secrets == nil {
		t.Fatal("openHubForConfig built a hub with no redactor")
	}
	// Usable, not merely non-nil.
	h.secrets.Add("a-decrypted-pillar-value")
	if got := h.secrets.Scrub("saw a-decrypted-pillar-value"); got == "saw a-decrypted-pillar-value" {
		t.Errorf("the redactor records nothing: %q", got)
	}
}

// TestAgentlessPillarUsesTheHubsKeyringAndRedactor. `inlinePillar`
// compiles pillar on the hub for a target that has no pillar tree of its
// own, so it decrypts on the hub exactly as serving an enrolled node
// does -- and it was built with neither `GPG` nor `OnSecret` assigned.
//
// Without `GPG` an encrypted pillar cannot compile for an agentless
// target at all. Without `OnSecret` the values it decrypts are unknown
// to this process's redactor, which is DIVERGENCE 5.110 still open in a
// third place. The first gap hid the second: a run that cannot decrypt
// cannot leak, so repairing decryption on its own would have opened the
// leak this asserts against.
func TestAgentlessPillarUsesTheHubsKeyringAndRedactor(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("no gpg on PATH; SPEC 12.6 drives the system binary")
	}
	const secret = "s3cret-value-for-an-agentless-target"
	home, gpg := throwawayKeyring(t)

	root := t.TempDir()
	pillarRoot := filepath.Join(root, "pillar")
	if err := os.MkdirAll(pillarRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(pillarRoot, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("top.sls", "base:\n  '*':\n    - secrets\n")
	// A plain value beside the encrypted one, in the same gpg-rendered
	// file: it is data, and the target must not be told to mask it.
	write("secrets.sls", "#!yaml|gpg\naddress: 10.11.12.13\ntoken: |\n    "+armoredFor(t, gpg, secret, "    ")+"\n")
	if err := os.WriteFile(filepath.Join(root, "hub.yaml"), []byte(
		"pillar_roots:\n  base:\n    - "+pillarRoot+"\ngpg_home: "+home+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	args := &cli.Args{Flags: map[string]string{"root": root}}
	h := openHubForConfig(args)

	got, secrets, err := inlinePillar(h, roster.Target{ID: "agentless.example.invalid"}, args)
	if err != nil {
		t.Fatalf("inlinePillar: %v", err)
	}
	// Compiled at all, and compiled to the plaintext: armor left in
	// place would mean the renderer never ran.
	if !strings.Contains(string(got), secret) {
		t.Errorf("the agentless target's pillar does not carry the decrypted value: %s", got)
	}
	if strings.Contains(string(got), "BEGIN PGP MESSAGE") {
		t.Errorf("the pillar reached the target still encrypted: %s", got)
	}
	// And the hub knows it is a secret. This is the half that cannot
	// fail visibly: a callback that is never called raises nothing.
	if scrubbed := h.secrets.Scrub("saw " + secret); strings.Contains(scrubbed, secret) {
		t.Errorf("the decrypted value never reached the hub's redactor: %q", scrubbed)
	}
	// And the target is told exactly that one, and not the plain value
	// beside it. DIVERGENCE 5.251.
	if len(secrets) != 1 || secrets[0] != secret {
		t.Errorf("the target is told to redact %q; want only the decrypted value", secrets)
	}
}
