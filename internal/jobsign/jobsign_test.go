package jobsign

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func payload() Payload {
	return Payload{
		JID:        "20260923T070000000001",
		Target:     "web*",
		TargetKind: "glob",
		Fun:        "state.apply",
		Arg:        []string{"nginx"},
		Kwarg:      map[string]any{"test": true, "pillar": map[string]any{"version": json.Number("3")}},
		Env:        "base",
		Expires:    time.Date(2026, 9, 23, 7, 15, 0, 0, time.UTC),
	}
}

func TestASignedJobVerifies(t *testing.T) {
	k, err := GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	line, err := FormatSignerKey("ops", &k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := ParseSignerKey(line)
	if err != nil {
		t.Fatal(err)
	}

	sig, err := Sign(k, payload())
	if err != nil {
		t.Fatal(err)
	}
	name, err := Verify([]SignerKey{trusted}, payload(), sig)
	if err != nil {
		t.Fatalf("a signature this package produced does not verify: %v", err)
	}
	if name != "ops" {
		t.Errorf("the signature verified as %q, expected the key's name", name)
	}
}

// Every field the signature covers changes it.
//
// Table-driven and exhaustive on purpose: a field left out of the
// canonical encoding is a field a hub can change on a signed job, and the
// failure is silent — the signature still verifies.
func TestEveryCoveredFieldChangesTheSignature(t *testing.T) {
	k, err := GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	line, _ := FormatSignerKey("ops", &k.PublicKey)
	trusted, _ := ParseSignerKey(line)
	sig, err := Sign(k, payload())
	if err != nil {
		t.Fatal(err)
	}

	tampered := map[string]func(p *Payload){
		"the job identifier":     func(p *Payload) { p.JID = "20260923T070000000002" },
		"the target":             func(p *Payload) { p.Target = "*" },
		"the target's kind":      func(p *Payload) { p.TargetKind = "list" },
		"the function":           func(p *Payload) { p.Fun = "cmd.run" },
		"an argument":            func(p *Payload) { p.Arg = []string{"apache"} },
		"an argument removed":    func(p *Payload) { p.Arg = nil },
		"an argument added":      func(p *Payload) { p.Arg = []string{"nginx", "extra"} },
		"a keyword argument":     func(p *Payload) { p.Kwarg["test"] = false },
		"a keyword name":         func(p *Payload) { delete(p.Kwarg, "test"); p.Kwarg["tset"] = true },
		"a nested value":         func(p *Payload) { p.Kwarg["pillar"] = map[string]any{"version": json.Number("4")} },
		"the environment":        func(p *Payload) { p.Env = "staging" },
		"the expiry":             func(p *Payload) { p.Expires = p.Expires.Add(time.Hour) },
		"the expiry removed":     func(p *Payload) { p.Expires = time.Time{} },
		"a keyword argument new": func(p *Payload) { p.Kwarg["sudo"] = true },
	}
	for what, change := range tampered {
		t.Run(what, func(t *testing.T) {
			p := payload()
			change(&p)
			if _, err := Verify([]SignerKey{trusted}, p, sig); err == nil {
				t.Errorf("changing %s left the signature verifying, so a hub could change it", what)
			}
		})
	}
}

// Two fields cannot be re-cut into one another.
//
// Without a length before each value, a target of `ab` and a function of
// `c` encode identically to a target of `a` and a function of `bc`, and
// one signature authorises both.
func TestFieldsCannotBeRecut(t *testing.T) {
	one, other := payload(), payload()
	one.Target, one.TargetKind = "ab", "glob"
	other.Target, other.TargetKind = "a", "bglob"
	if bytes.Equal(Canonical(one), Canonical(other)) {
		t.Error("two different payloads have the same canonical encoding")
	}
}

// A number encodes by value, not by the text it arrived as.
//
// The operator's command line, the hub and the node each decode the
// job's keyword arguments separately, and `1` may be an int64 in one and
// a json.Number in the next. If those encoded differently, a signature
// made on one side would not verify on the other, and the symptom would
// be every signed job refused with "the signature does not verify".
func TestANumberEncodesByValueNotBySpelling(t *testing.T) {
	forms := []any{json.Number("1"), int64(1), 1, float64(1), json.Number("1.0")}
	var want string
	for i, form := range forms {
		p := payload()
		p.Kwarg = map[string]any{"n": form}
		got := string(Canonical(p))
		if i == 0 {
			want = got
			continue
		}
		if got != want {
			t.Errorf("%T %v encodes differently from json.Number(\"1\")", form, form)
		}
	}
}

// A 64-bit integer keeps its last digits, which SPEC 6.4 promises and
// float64 does not.
func TestALargeIntegerSurvivesTheEncoding(t *testing.T) {
	p := payload()
	p.Kwarg = map[string]any{"n": json.Number("9007199254740993")}
	if !strings.Contains(string(Canonical(p)), "9007199254740993") {
		t.Errorf("a 64-bit integer was rounded in the canonical encoding: %s", Canonical(p))
	}
}

// A signature over one kind of thing cannot be replayed as a signature
// over another.
func TestTheDomainSeparatorIsPresent(t *testing.T) {
	if !bytes.HasPrefix(Canonical(payload()), []byte(domain)) {
		t.Error("the canonical encoding does not start with the domain separator")
	}
}

func TestAnUntrustedKeyDoesNotVerify(t *testing.T) {
	signer, _ := GenerateKey("ecdsa-p256")
	other, _ := GenerateKey("ecdsa-p256")
	line, _ := FormatSignerKey("someone-else", &other.PublicKey)
	trusted, _ := ParseSignerKey(line)

	sig, err := Sign(signer, payload())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify([]SignerKey{trusted}, payload(), sig); err == nil {
		t.Fatal("a job signed by an untrusted key verified")
	}
}

func TestVerifyingWithNoKeysSaysSo(t *testing.T) {
	_, err := Verify(nil, payload(), "irrelevant")
	if err == nil {
		t.Fatal("verification with no trusted keys succeeded")
	}
	if !strings.Contains(err.Error(), "job_signer_keys") {
		t.Errorf("the refusal does not name the setting to fix it: %v", err)
	}
}

func TestKeysRoundTripThroughTheirConfigurationLine(t *testing.T) {
	for _, algorithm := range []string{"ecdsa-p256", "ecdsa-p384"} {
		k, err := GenerateKey(algorithm)
		if err != nil {
			t.Fatalf("%s: %v", algorithm, err)
		}
		line, err := FormatSignerKey("ops", &k.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseSignerKey(line)
		if err != nil {
			t.Fatalf("%s: %v", algorithm, err)
		}
		if !parsed.Key.Equal(&k.PublicKey) {
			t.Errorf("%s: the key did not survive the round trip", algorithm)
		}

		pem, err := EncodePrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		back, err := DecodePrivateKey(pem)
		if err != nil {
			t.Fatalf("%s: %v", algorithm, err)
		}
		if !back.PublicKey.Equal(&k.PublicKey) {
			t.Errorf("%s: the private key did not survive the round trip", algorithm)
		}
	}
}

func TestAMalformedKeyLineSaysWhatIsWrong(t *testing.T) {
	cases := map[string]string{
		"ops":             "is `<name> <base64 key>`",
		"ops not-base64!": "not base64",
		"ops " + base64.StdEncoding.EncodeToString([]byte("not a key")): "SubjectPublicKeyInfo",
	}
	for line, want := range cases {
		_, err := ParseSignerKey(line)
		if err == nil {
			t.Errorf("%q was accepted as a signer key", line)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: the message is %q, expected it to mention %q", line, err, want)
		}
	}
}

// The signature is a format something other than this package can check.
//
// SPEC 25.6 allows the signer to be a bridged extension backed by a KMS,
// so the encoding has to be one another implementation can produce and
// verify. A signature verified only by the code that made it establishes
// that the code agrees with itself.
func TestOpenSSLVerifiesWhatThisPackageSigns(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is not on this machine, so the foreign check cannot run here")
	}

	k, err := GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := Sign(k, payload())
	if err != nil {
		t.Fatal(err)
	}
	der, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	keyPEM, err := EncodePrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	keyPath := write("signer.key", keyPEM)
	messagePath := write("message.bin", Canonical(payload()))
	sigPath := write("job.sig", der)

	pubPath := filepath.Join(dir, "signer.pub")
	out, err := exec.Command(openssl, "pkey", "-in", keyPath, "-pubout", "-out", pubPath).CombinedOutput()
	if err != nil {
		t.Fatalf("openssl pkey: %v: %s", err, out)
	}
	out, err = exec.Command(openssl, "dgst", "-sha256", "-verify", pubPath,
		"-signature", sigPath, messagePath).CombinedOutput()
	if err != nil {
		t.Fatalf("openssl could not verify a signature this package made: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "Verified OK") {
		t.Errorf("openssl said %q", out)
	}

	// And the other direction of the same claim: openssl must refuse a
	// message that was changed, or the check above proves only that
	// openssl ran.
	changed := write("changed.bin", append(Canonical(payload()), ' '))
	if out, err := exec.Command(openssl, "dgst", "-sha256", "-verify", pubPath,
		"-signature", sigPath, changed).CombinedOutput(); err == nil {
		t.Errorf("openssl verified a message that had been changed: %s", out)
	}
}

// And a signature openssl made verifies here, which is the direction a
// KMS-backed signer would be in.
func TestThisPackageVerifiesWhatOpenSSLSigns(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is not on this machine, so the foreign check cannot run here")
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signer.key")
	if out, err := exec.Command(openssl, "ecparam", "-name", "prime256v1",
		"-genkey", "-noout", "-out", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("openssl ecparam: %v: %s", err, out)
	}
	pubPath := filepath.Join(dir, "signer.pub")
	if out, err := exec.Command(openssl, "pkey", "-in", keyPath,
		"-pubout", "-outform", "DER", "-out", pubPath).CombinedOutput(); err != nil {
		t.Fatalf("openssl pkey: %v: %s", err, out)
	}
	der, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := ParseSignerKey("kms " + base64.StdEncoding.EncodeToString(der))
	if err != nil {
		t.Fatalf("a public key openssl wrote is not readable here: %v", err)
	}

	messagePath := filepath.Join(dir, "message.bin")
	if err := os.WriteFile(messagePath, Canonical(payload()), 0o600); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "job.sig")
	if out, err := exec.Command(openssl, "dgst", "-sha256", "-sign", keyPath,
		"-out", sigPath, messagePath).CombinedOutput(); err != nil {
		t.Fatalf("openssl dgst -sign: %v: %s", err, out)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}

	name, err := Verify([]SignerKey{trusted}, payload(),
		base64.StdEncoding.EncodeToString(sig))
	if err != nil {
		t.Fatalf("a signature openssl made does not verify here: %v", err)
	}
	if name != "kms" {
		t.Errorf("verified as %q", name)
	}
}
