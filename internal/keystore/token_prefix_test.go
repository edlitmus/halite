package keystore

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// **A minted token never begins with a hyphen** (DIVERGENCE 5.674).
//
// Before the prefix, the secret was bare base64url and began with `-`
// one time in 64, which `halite-node enroll --token SECRET` reads as a
// flag. Sixty-four mints would catch that only about 63% of the time, so
// the deterministic half of this test is the prefix itself; the count is
// there so that a change that keeps a prefix but puts something else in
// front of it still has to get past a real sample.
//
// The rest of each secret is held to SPEC 7.3's 256 bits: it decodes,
// as unpadded base64url, to exactly tokenBytes bytes. That is what a
// redraw-until-not-hyphen fix would not have kept.
func TestAMintedTokenNeverBeginsWithAHyphen(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 64; i++ {
		_, secret, err := store.MintToken(TokenOptions{TTL: time.Hour}, now)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(secret, "-") {
			t.Fatalf("minted %q, which `--token %s` reads as a flag", secret, secret)
		}
		random, ok := strings.CutPrefix(secret, tokenPrefix)
		if !ok {
			t.Fatalf("minted %q without the %q prefix", secret, tokenPrefix)
		}
		raw, err := base64.RawURLEncoding.DecodeString(random)
		if err != nil || len(raw) != tokenBytes {
			t.Fatalf("the random part of %q is %d bytes (%v), want %d", secret, len(raw), err, tokenBytes)
		}
	}
}

// **A token minted before the prefix still enrols.** Verification hashes
// the string the node presents, and nothing parses the prefix, so a
// bare base64 token on disk from an older hub -- including one that
// begins with a hyphen, which a node can pass as `--token=` or in a
// file -- is admitted exactly as before.
func TestATokenWithoutThePrefixStillEnrols(t *testing.T) {
	a, _ := newAuthority(t)
	a.Mode = ModeToken
	now := time.Now()
	const legacy = "-zO2Bl1eEexampleTokenFromABase64Alphabet_"
	digest := digestOf(legacy)
	tok := &Token{
		ID: digest[:tokenIDLen], Digest: digest, MaxUses: 1,
		Expires: now.Add(time.Hour), Created: now,
	}
	if err := a.Store.locked(func() error {
		if err := mkdirOwned(a.Store.tokenDir()); err != nil {
			return err
		}
		return a.Store.putToken(tok)
	}); err != nil {
		t.Fatal(err)
	}
	csr, _ := request(t, "web1.example")
	res, err := a.Enroll(Request{CSR: csr, Token: legacy, RemoteAddr: "10.0.0.1:1"})
	if err != nil || res.State != Accepted {
		t.Fatalf("a token minted before the prefix: %v %v", res, err)
	}
}
