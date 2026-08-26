package account

import (
	"strings"
	"testing"
	"time"
)

func TestAPasswordVerifiesAndAWrongOneDoesNot(t *testing.T) {
	hash, err := Hash("correct horse", MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	a := &Account{Name: "ed", Hash: hash}
	if !a.Verify("correct horse") {
		t.Error("the right password was refused")
	}
	if a.Verify("correct horse ") {
		t.Error("a password with a trailing space was accepted")
	}
	if a.Verify("") {
		t.Error("an empty password was accepted")
	}
}

// The hash carries its own parameters, so the cost can be raised
// without invalidating what is already stored.
func TestAHashCarriesItsOwnCost(t *testing.T) {
	hash, err := Hash("pw", MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha512$") {
		t.Fatalf("the hash is %q", hash)
	}
	v, err := parseHash(hash)
	if err != nil {
		t.Fatal(err)
	}
	if v.iterations != MinIterations {
		t.Errorf("the cost came back as %d", v.iterations)
	}
	if len(v.salt) != saltLength || len(v.key) != keyLength {
		t.Errorf("salt %d bytes, key %d bytes", len(v.salt), len(v.key))
	}

	// Two hashes of the same password differ, because the salt does.
	other, err := Hash("pw", MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	if other == hash {
		t.Error("two hashes of one password are identical")
	}
}

// A record below the floor is refused rather than accepted and quietly
// re-hashed: an operator has to know it is there, and accepting it
// silently is how it stays.
func TestAHashBelowTheFloorIsRefused(t *testing.T) {
	if _, err := Hash("pw", 1000); err == nil {
		t.Error("a cheap hash was produced")
	}
	weak := "pbkdf2-sha512$1000$AAAAAAAAAAAAAAAAAAAAAA$AAAA"
	if _, err := parseHash(weak); err == nil {
		t.Fatal("a cheap hash was accepted")
	} else if !strings.Contains(err.Error(), "floor") {
		t.Errorf("the refusal says %q", err)
	}
	a := &Account{Name: "ed", Hash: weak}
	if a.Verify("pw") {
		t.Error("a cheap hash verified")
	}
}

// A nil account still pays for the hash, so that a login endpoint does
// not answer faster for a name that does not exist.
func TestVerifyingANilAccountDoesTheWork(t *testing.T) {
	var missing *Account
	started := time.Now()
	if missing.Verify("anything") {
		t.Error("a missing account verified")
	}
	if elapsed := time.Since(started); elapsed < time.Millisecond {
		t.Errorf("verifying a missing account took %s, which is no work at all", elapsed)
	}
}

func loadAccounts(t *testing.T, src string) *File {
	t.Helper()
	f, err := Load([]byte(src), "accounts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAnAccountFileParses(t *testing.T) {
	hash, err := Hash("pw", MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	f := loadAccounts(t, `
accounts:
  ed:
    hash: "`+hash+`"
    roles:
      - administrator
  ci:
    hash: "`+hash+`"
    roles: [deployer]
    disabled: true
`)
	if len(f.Accounts) != 2 {
		t.Fatalf("parsed %d accounts", len(f.Accounts))
	}
	ed, ok := f.Lookup("ed")
	if !ok {
		t.Fatalf("the accounts are %v", f.Names())
	}
	// The principal is prefixed, so an account called `ed` and a
	// certificate whose common name is `ed` are different identities.
	if ed.Principal() != "local:ed" {
		t.Errorf("the principal is %q", ed.Principal())
	}
	if len(ed.Roles) != 1 || ed.Roles[0] != "administrator" {
		t.Errorf("the roles are %v", ed.Roles)
	}
	ci, _ := f.Lookup("ci")
	if !ci.Disabled {
		t.Error("a disabled account did not parse as one")
	}
}

// An account file that will not parse is refused, rather than
// authenticating a subset of the people it names.
func TestABadAccountFileIsRefused(t *testing.T) {
	cases := map[string]string{
		"no hash":        "accounts:\n  ed:\n    roles: [a]\n",
		"unknown field":  "accounts:\n  ed:\n    hash: x\n    nonsense: 1\n",
		"a bad hash":     "accounts:\n  ed:\n    hash: 'not-a-hash'\n",
		"roles not list": "accounts:\n  ed:\n    hash: x\n    roles: administrator\n",
		"not a mapping":  "accounts:\n  - ed\n",
	}
	for name, src := range cases {
		if _, err := Load([]byte(src), "accounts.yaml"); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// An absent file is an empty set rather than an error: an estate on
// OIDC alone has none.
func TestAnEmptyAccountFileIsNotAnError(t *testing.T) {
	f := loadAccounts(t, "")
	if len(f.Accounts) != 0 {
		t.Errorf("an empty file produced %v", f.Names())
	}
	f = loadAccounts(t, "accounts: {}\n")
	if len(f.Accounts) != 0 {
		t.Errorf("an empty mapping produced %v", f.Names())
	}
}

// RFC 6238, with one step either side allowed for a clock that is
// slightly out. More than that turns a thirty-second code into a
// minute-and-a-half one.
func TestTOTPAcceptsTheCurrentStepAndItsNeighbours(t *testing.T) {
	// The RFC 4226 test secret, base32.
	a := &Account{Name: "ed", TOTP: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"}
	if !a.NeedsSecondFactor() {
		t.Fatal("an account with a secret does not need a second factor")
	}
	now := time.Unix(59, 0)
	if !TOTPAvailable() {
		// A FIPS build has no HMAC-SHA-1, so the second factor cannot
		// be checked. What matters is the direction of the failure:
		// every code is refused and the account still declares that it
		// needs one. The other way round would let a password alone
		// through on exactly the accounts that asked for two.
		if a.VerifyTOTP("000000", now) {
			t.Error("a code was accepted with no way to check it")
		}
		if !a.NeedsSecondFactor() {
			t.Error("an account was silently downgraded to one factor")
		}
		if len((&File{Accounts: map[string]*Account{"ed": a}}).LockedOut()) != 1 {
			t.Error("an account that cannot log in is not reported as locked out")
		}
		return
	}

	secret, err := decodeBase32(a.TOTP)
	if err != nil {
		t.Fatal(err)
	}
	counter := now.Unix() / 30
	for _, skew := range []int64{-1, 0, 1} {
		code := totpCode(secret, counter+skew)
		if !a.VerifyTOTP(code, now) {
			t.Errorf("the code for step %+d was refused", skew)
		}
	}
	// Two steps out is refused.
	if a.VerifyTOTP(totpCode(secret, counter+2), now) {
		t.Error("a code two steps out was accepted")
	}
	if a.VerifyTOTP("000000", now) && a.VerifyTOTP("999999", now) {
		t.Error("every code is accepted")
	}
	// An account with no secret needs no code and accepts none.
	none := &Account{Name: "x"}
	if none.NeedsSecondFactor() || none.VerifyTOTP("000000", now) {
		t.Error("an account with no secret accepted a code")
	}
}

// The secret is read the way an authenticator app shows it: unpadded,
// and often in spaced groups.
func TestASpacedTOTPSecretIsRead(t *testing.T) {
	if !TOTPAvailable() {
		t.Skip("this build cannot check a TOTP code; SPEC 27.4")
	}
	plain := &Account{TOTP: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"}
	spaced := &Account{TOTP: "gezd gnbv gy3t qojq gezd gnbv gy3t qojq"}
	now := time.Unix(1234567890, 0)
	secret, _ := decodeBase32(plain.TOTP)
	code := totpCode(secret, now.Unix()/30)
	if !spaced.VerifyTOTP(code, now) {
		t.Error("a spaced, lower-case secret was not read")
	}
}
