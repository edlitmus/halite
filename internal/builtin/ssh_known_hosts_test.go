package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A real ed25519 key and the fingerprint OpenSSH prints for it.
//
// Taken from `ssh-keygen -t ed25519` and `ssh-keygen -lf` rather than
// computed here, so this pins the format against the tool an operator
// reads it from. A fingerprint this build renders differently — padded,
// unprefixed, hex — would match nothing anybody pastes into a state.
const (
	testHostKey  = "AAAAC3NzaC1lZDI1NTE5AAAAIPiu0cd30umqEwp6+j0yXhj6w2GVpooNEGnrMPY2WCyP"
	testHostFP   = "SHA256:UuHzAyuSljiqidTC5vDhMgHYlQI6zXxAAkrwuYFqD3w"
	testHostType = "ssh-ed25519"
)

func TestTheFingerprintIsTheOneSSHPrints(t *testing.T) {
	if got := sha256Fingerprint(testHostKey); got != testHostFP {
		t.Errorf("sha256Fingerprint = %q, want %q", got, testHostFP)
	}
	// An operator pasting from `ssh-keygen -lf` has the prefix; one
	// pasting from a wiki table may not, and both mean the same key.
	for _, form := range []string{
		testHostFP,
		strings.TrimPrefix(testHostFP, "SHA256:"),
	} {
		if !sameFingerprint(form, testHostFP) {
			t.Errorf("%q did not match the key's own fingerprint", form)
		}
	}
	if sameFingerprint("SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", testHostFP) {
		t.Error("a different fingerprint matched")
	}
}

// Lines this build does not understand survive a rewrite.
//
// A hashed entry cannot be matched without the salt and the name being
// looked for, and a @cert-authority line means something other than "this
// host has this key". Dropping either while adding an unrelated host
// would remove trust an operator had already granted, which is a worse
// outcome than the one the state was asked for.
func TestARewriteKeepsWhatItCannotRead(t *testing.T) {
	lines := []string{
		"# managed by hand",
		"|1|F1E1s4L1u0Kx6L0Q=|abcdef= ssh-rsa AAAAB3Nz",
		"@cert-authority *.example ssh-ed25519 " + testHostKey,
		"web1.example " + testHostType + " " + testHostKey,
		"",
	}
	entries := parseKnownHosts(lines)
	readable := 0
	for _, e := range entries {
		if !e.Verbatim {
			readable++
			if e.Host != "web1.example" {
				t.Errorf("a line was parsed as host %q", e.Host)
			}
		}
	}
	if readable != 1 {
		t.Errorf("%d lines were parsed as entries, want 1 — the rest are not host records", readable)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.String())
	}
	for _, keep := range []string{"|1|", "@cert-authority", "# managed by hand"} {
		if !strings.Contains(strings.Join(out, "\n"), keep) {
			t.Errorf("a rewrite dropped %q", keep)
		}
	}
}

// A record naming several hosts against one key covers each of them.
func TestOneRecordCanCoverSeveralNames(t *testing.T) {
	if !matchesHost("web1.example,192.0.2.10", "web1.example") {
		t.Error("the name in a comma-separated record did not match")
	}
	if !matchesHost("web1.example,192.0.2.10", "192.0.2.10") {
		t.Error("the address in a comma-separated record did not match")
	}
	if matchesHost("web1.example,192.0.2.10", "web2.example") {
		t.Error("an unrelated host matched")
	}
	// A non-standard port is part of the identity, not decoration.
	if got := hostField("web1.example", 2222); got != "[web1.example]:2222" {
		t.Errorf("hostField = %q", got)
	}
	if got := hostField("web1.example", 22); got != "web1.example" {
		t.Errorf("port 22 was written into the record as %q", got)
	}
}

// Trust on first use is refused rather than performed.
//
// Salt scans the host and accepts whatever answers, which pins whatever
// was listening on the day the tree first ran. This build asks for the
// key or for the fingerprint of the key, and says which when it has
// neither — including in test mode, where a scan would already have made
// the decision it was asked to preview.
func TestAKeyIsNotTrustedJustBecauseItAnswered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	r := New()

	for _, test := range []bool{true, false} {
		res := run(t, r, "ssh_known_hosts.present",
			value.MapOf("name", "web1.example", "config", path), test)
		if res.Succeeded() {
			t.Fatalf("test=%v: a host with no key and no fingerprint was accepted", test)
		}
		for _, want := range []string{"key", "fingerprint"} {
			if !strings.Contains(res.Comment, want) {
				t.Errorf("test=%v: the refusal does not mention %q: %s", test, want, res.Comment)
			}
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a refused state wrote the file anyway")
	}
}

// A declared key is added, is idempotent, and a different key for a
// known host is reported as the replacement it is.
func TestADeclaredKeyIsAddedAndThenLeftAlone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	r := New()
	args := value.MapOf(
		"name", "web1.example",
		"key", testHostKey,
		"enc", testHostType,
		"config", path,
	)

	res := run(t, r, "ssh_known_hosts.present", args, false)
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("the key was not added: %+v", res)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "web1.example " + testHostType + " " + testHostKey; !strings.Contains(string(body), want) {
		t.Errorf("known_hosts holds %q", string(body))
	}

	// Again, and nothing happens. A state that rewrote the file every
	// run would churn a file ssh reads on every connection.
	res = run(t, r, "ssh_known_hosts.present", args, false)
	if res.HasChanges() {
		t.Errorf("a second run changed the file: %+v", res.Changes)
	}

	// A different key for a host already known is the case known_hosts
	// exists to notice, so both fingerprints reach the change set.
	other := "AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	res = run(t, r, "ssh_known_hosts.present", value.MapOf(
		"name", "web1.example", "key", other, "enc", testHostType, "config", path), false)
	if !res.HasChanges() {
		t.Fatalf("a changed host key was not reported: %+v", res)
	}
	change, _ := res.Changes.Get("web1.example")
	pair, ok := change.(*value.Map)
	if !ok {
		t.Fatalf("the change is %T, not a change pair", change)
	}
	was, _ := pair.Get("old")
	if got, _ := was.(string); got != testHostFP {
		t.Errorf("the change says the old key was %q, want the fingerprint %q", got, testHostFP)
	}
}

// A declared key without its type is refused, because the type is part
// of the record and guessing it writes a line ssh will not read.
func TestADeclaredKeyNeedsItsType(t *testing.T) {
	dir := t.TempDir()
	res := run(t, New(), "ssh_known_hosts.present", value.MapOf(
		"name", "web1.example",
		"key", testHostKey,
		"config", filepath.Join(dir, "known_hosts"),
	), false)
	if res.Succeeded() {
		t.Fatalf("a key with no type was accepted: %+v", res)
	}
	if !strings.Contains(res.Comment, "enc") {
		t.Errorf("the refusal does not name the missing argument: %s", res.Comment)
	}
}

// Absent removes the host and leaves everything else where it was.
func TestAbsentRemovesOnlyTheHostNamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	body := "# keep me\n" +
		"web1.example " + testHostType + " " + testHostKey + "\n" +
		"web2.example " + testHostType + " " + testHostKey + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New()

	res := run(t, r, "ssh_known_hosts.absent",
		value.MapOf("name", "web1.example", "config", path), false)
	if !res.HasChanges() {
		t.Fatalf("the host was not removed: %+v", res)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "web1.example") {
		t.Errorf("web1 is still there: %s", after)
	}
	for _, keep := range []string{"web2.example", "# keep me"} {
		if !strings.Contains(string(after), keep) {
			t.Errorf("%q was removed as well: %s", keep, after)
		}
	}

	// And again, with nothing to do.
	res = run(t, r, "ssh_known_hosts.absent",
		value.MapOf("name", "web1.example", "config", path), false)
	if res.HasChanges() {
		t.Errorf("removing an absent host reported a change: %+v", res.Changes)
	}
}
