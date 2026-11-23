package treesign

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/jobsign"
)

var issued = time.Date(2026, 11, 22, 18, 4, 5, 123456789, time.UTC)

func sampleManifest(t *testing.T) []byte {
	t.Helper()
	raw, err := Encode(&Manifest{Env: "base", Issued: FormatTime(issued), HashType: "sha256",
		Source: "commit 3f1c refs/heads/production",
		Files: []Entry{
			{Path: "top.sls", Hash: strings.Repeat("a", 64), Size: 3, Mode: "0644"},
			{Path: "nginx/init.sls", Hash: strings.Repeat("b", 64), Size: 9, Mode: "0644"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func key(t *testing.T, curve elliptic.Curve) (*ecdsa.PrivateKey, jobsign.SignerKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k, jobsign.SignerKey{Name: curve.Params().Name, Key: &k.PublicKey}
}

func TestAManifestSignedWithEitherCurveVerifies(t *testing.T) {
	raw := sampleManifest(t)
	for _, curve := range []elliptic.Curve{elliptic.P256(), elliptic.P384()} {
		priv, pub := key(t, curve)
		line, err := SignatureLine(priv, raw)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := Verify([]jobsign.SignerKey{pub}, raw, []byte(line+"\n"))
		if err != nil || signer != pub.Name {
			t.Fatalf("%s: signer %q, %v", curve.Params().Name, signer, err)
		}
	}
}

func TestAFlippedByteInEitherFileFailsVerification(t *testing.T) {
	raw := sampleManifest(t)
	priv, pub := key(t, elliptic.P256())
	line, _ := SignatureLine(priv, raw)
	changed := append([]byte(nil), raw...)
	changed[len(changed)/2] ^= 1
	if _, err := Verify([]jobsign.SignerKey{pub}, changed, []byte(line)); err == nil {
		t.Error("a changed manifest verified")
	}
	der, _ := base64.StdEncoding.DecodeString(line)
	der[len(der)-1] ^= 1
	if _, err := Verify([]jobsign.SignerKey{pub}, raw, []byte(base64.StdEncoding.EncodeToString(der))); err == nil {
		t.Error("a changed signature verified")
	}
}

func TestAKeyTheNodeDoesNotTrustIsRefused(t *testing.T) {
	raw := sampleManifest(t)
	priv, _ := key(t, elliptic.P256())
	_, other := key(t, elliptic.P256())
	line, _ := SignatureLine(priv, raw)
	if _, err := Verify([]jobsign.SignerKey{other}, raw, []byte(line)); err == nil {
		t.Fatal("a signature by an untrusted key verified")
	}
	if _, err := Verify(nil, raw, []byte(line)); err == nil || !strings.Contains(err.Error(), "state_signer_keys") {
		t.Fatalf("no keys: %v", err)
	}
}

// The two domains keep the two signatures apart: a signature over the same
// 32 bytes a job signer would sign is not a tree signature, and the reverse.
func TestATreeSignatureAndAJobSignatureCannotStandInForEachOther(t *testing.T) {
	raw := sampleManifest(t)
	priv, pub := key(t, elliptic.P256())
	// A signature over the manifest with no domain, or with the job's.
	for _, prefix := range []string{"", "halite-job-v2\x00"} {
		sum := sha256.Sum256(append([]byte(prefix), raw...))
		der, _ := ecdsa.SignASN1(rand.Reader, priv, sum[:])
		if _, err := Verify([]jobsign.SignerKey{pub}, raw, []byte(base64.StdEncoding.EncodeToString(der))); err == nil {
			t.Errorf("a signature under the domain %q verified as a tree signature", prefix)
		}
	}
	// And a tree signature presented as a job's.
	payload := jobsign.Payload{JID: "1", Target: "*", TargetKind: "glob", Fun: "test.ping", Env: "base",
		Expires: time.Now().Add(time.Minute)}
	line, _ := SignatureLine(priv, jobsign.Canonical(payload))
	if _, err := jobsign.Verify([]jobsign.SignerKey{pub}, payload, line); err == nil {
		t.Error("a tree signature verified as a job signature")
	}
}

// One valid line is enough, and more than one is read: the format a later
// M-of-N rule needs, accepted now so an older node does not refuse it.
func TestAnyOneVerifyingLineOfSeveralIsEnough(t *testing.T) {
	raw := sampleManifest(t)
	priv, pub := key(t, elliptic.P256())
	other, _ := key(t, elliptic.P256())
	good, _ := SignatureLine(priv, raw)
	foreign, _ := SignatureLine(other, raw)
	if _, err := Verify([]jobsign.SignerKey{pub}, raw, []byte(foreign+"\n"+good+"\n")); err != nil {
		t.Fatalf("two lines, one trusted: %v", err)
	}
	if _, err := Verify([]jobsign.SignerKey{pub}, raw, []byte(good+"\nnot base64!\n")); err == nil {
		t.Error("a line that is not base64 was ignored rather than refused")
	}
	if _, err := Verify([]jobsign.SignerKey{pub}, raw, []byte("\n\n")); err == nil {
		t.Error("an empty signature file verified")
	}
}

// The format is checked against a tool other than its own producer, as the
// job format is: openssl signs the domain-prefixed bytes.
func TestASignatureOpenSSLMadeVerifies(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is not on this machine, so the foreign check cannot run here")
	}
	dir := t.TempDir()
	priv, pub := key(t, elliptic.P256())
	pemBytes, _ := jobsign.EncodePrivateKey(priv)
	keyPath := filepath.Join(dir, "k.pem")
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	raw := sampleManifest(t)
	signed := filepath.Join(dir, "signed")
	if err := os.WriteFile(signed, append([]byte("halite-tree-v1\x00"), raw...), 0o600); err != nil {
		t.Fatal(err)
	}
	der, err := exec.Command(openssl, "dgst", "-sha256", "-sign", keyPath, signed).Output()
	if err != nil {
		t.Fatalf("openssl dgst -sign: %v", err)
	}
	if _, err := Verify([]jobsign.SignerKey{pub}, raw, []byte(base64.StdEncoding.EncodeToString(der))); err != nil {
		t.Fatalf("a signature openssl made over the documented bytes does not verify: %v", err)
	}
}

func TestParseRefusesEveryBrokenRule(t *testing.T) {
	good := string(sampleManifest(t))
	cases := map[string]string{
		"an unknown field":    strings.Replace(good, `"schema"`, `"extra": 1, "schema"`, 1),
		"an unknown schema":   strings.Replace(good, Schema, "halite.tree/2", 1),
		"md5":                 strings.Replace(good, `"hash_type": "sha256"`, `"hash_type": "md5"`, 1),
		"sha1":                strings.Replace(good, `"hash_type": "sha256"`, `"hash_type": "sha1"`, 1),
		"unsorted":            strings.Replace(strings.Replace(good, "nginx/init.sls", "zz.sls", 1), "zz.sls", "zz.sls", 1),
		"an absolute path":    strings.Replace(good, `"top.sls"`, `"/top.sls"`, 1),
		"a dot-dot segment":   strings.Replace(good, `"top.sls"`, `"x/../top.sls"`, 1),
		"an empty segment":    strings.Replace(good, `"top.sls"`, `"x//top.sls"`, 1),
		"a backslash":         strings.Replace(good, `"top.sls"`, `"x\\top.sls"`, 1),
		"a reserved path":     strings.Replace(good, `"top.sls"`, `"_halite/top.sls"`, 1),
		"a short digest":      strings.Replace(good, strings.Repeat("a", 64), "aa", 1),
		"an upper digest":     strings.Replace(good, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		"a bad mode":          strings.Replace(good, `"0644"`, `"644"`, 1),
		"a non-octal mode":    strings.Replace(good, `"0644"`, `"0648"`, 1),
		"a local time":        strings.Replace(good, "123456789Z", "123456789+01:00", 1),
		"no files list":       strings.Replace(good, `"files"`, `"filez"`, 1),
		"trailing content":    good + "{}",
		"an environment /":    strings.Replace(good, `"env": "base"`, `"env": "a/b"`, 1),
		"an empty env":        strings.Replace(good, `"env": "base"`, `"env": ""`, 1),
		"a duplicate entry":   strings.Replace(good, "nginx/init.sls", "top.sls", 1),
		"a negative size":     strings.Replace(good, `"size": 3`, `"size": -3`, 1),
		"expiry before issue": strings.Replace(good, `"hash_type"`, `"expires": "2020-01-01T00:00:00Z", "hash_type"`, 1),
	}
	for name, raw := range cases {
		if raw == good {
			t.Errorf("%s: the case did not change the manifest", name)
			continue
		}
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatalf("the good manifest: %v", err)
	}
}

func TestLookupFindsOnlyNamedPaths(t *testing.T) {
	m, err := Parse(sampleManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Lookup("top.sls"); !ok {
		t.Error("top.sls not found")
	}
	for _, p := range []string{"nginx", "nginx/init", "missing.sls", ""} {
		if _, ok := m.Lookup(p); ok {
			t.Errorf("%q found", p)
		}
	}
}

func TestFreshnessRules(t *testing.T) {
	raw := sampleManifest(t)
	m, _ := Parse(raw)
	id := ID(raw)
	record := func(at time.Time, id string) *Record {
		return &Record{Env: "base", Issued: FormatTime(at), ID: id}
	}
	cases := []struct {
		name   string
		env    string
		now    time.Time
		maxAge time.Duration
		prev   *Record
		want   Reason
	}{
		{"first ever", "base", issued, 0, nil, ""},
		{"another environment's", "dev", issued, 0, nil, ReasonEnv},
		{"older than the mark", "base", issued, 0, record(issued.Add(time.Second), "sha256:x"), ReasonRollback},
		{"the same revision again", "base", issued, 0, record(issued, id), ""},
		{"same instant, other content", "base", issued, 0, record(issued, "sha256:x"), ReasonEquivocation},
		{"newer than the mark", "base", issued, 0, record(issued.Add(-time.Second), "sha256:x"), ""},
		{"age inside the skew", "base", issued.Add(time.Hour + 59*time.Second), time.Hour, nil, ""},
		{"age past the skew", "base", issued.Add(time.Hour + 61*time.Second), time.Hour, nil, ReasonAge},
		{"no age bound", "base", issued.Add(1000 * time.Hour), 0, nil, ""},
		{"from the future", "base", issued.Add(-2 * time.Minute), 0, nil, ReasonFuture},
		{"future inside the skew", "base", issued.Add(-30 * time.Second), 0, nil, ""},
	}
	for _, c := range cases {
		got := ReasonOf(CheckFresh(m, id, c.env, c.now, c.maxAge, c.prev))
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAnExpiryIsHonouredEitherSideOfTheSkew(t *testing.T) {
	raw, _ := Encode(&Manifest{Env: "base", Issued: FormatTime(issued), HashType: "sha256",
		Expires: FormatTime(issued.Add(time.Hour)), Files: []Entry{}})
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckFresh(m, ID(raw), "base", issued.Add(time.Hour+59*time.Second), 0, nil); err != nil {
		t.Errorf("inside the skew: %v", err)
	}
	if r := ReasonOf(CheckFresh(m, ID(raw), "base", issued.Add(time.Hour+61*time.Second), 0, nil)); r != ReasonExpired {
		t.Errorf("past it: %q", r)
	}
}

func TestTheStoreKeepsTheMarkAndTheBytesItCameFrom(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "tree-revisions")}
	if r, err := s.Load("base"); r != nil || err != nil {
		t.Fatalf("an empty store: %v, %v", r, err)
	}
	first := sampleManifest(t)
	rec := Record{Env: "release.1", Issued: FormatTime(issued), ID: ID(first), Signer: "ci"}
	if err := s.Save(rec, first, []byte("sig\n")); err != nil {
		t.Fatal(err)
	}
	second := append([]byte(nil), first...)
	second = append(second, ' ')
	rec2 := rec
	rec2.ID = ID(second)
	if err := s.Save(rec2, second, []byte("sig2\n")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("release.1")
	if err != nil || got.ID != rec2.ID {
		t.Fatalf("load: %+v, %v", got, err)
	}
	m, sig, err := s.Saved("release.1")
	if err != nil || string(m) != string(second) || string(sig) != "sig2\n" {
		t.Fatalf("saved: %q %q %v", m, sig, err)
	}
	prev, err := s.Previous("release.1")
	if err != nil || string(prev) != string(first) {
		t.Fatalf("previous: %v", err)
	}
	all, err := s.Records()
	if err != nil || len(all) != 1 || all[0].Env != "release.1" {
		t.Fatalf("records: %+v, %v", all, err)
	}
}
