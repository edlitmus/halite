package account

import "testing"

// derivation is one call of derive, as the key derivation saw it.
type derivation struct {
	iterations int
	saltLength int
}

// countDerivations replaces derive for the test with one that records
// what it was asked and does none of the work, so the paths can be
// compared without timing PBKDF2 against itself.
func countDerivations(t *testing.T) *[]derivation {
	t.Helper()
	var seen []derivation
	real := derive
	derive = func(password string, salt []byte, iterations int) ([]byte, error) {
		seen = append(seen, derivation{iterations: iterations, saltLength: len(salt)})
		return make([]byte, keyLength), nil
	}
	t.Cleanup(func() { derive = real })
	return &seen
}

// DIVERGENCE 5.290: an unknown name was checked at MinIterations while
// every hash `halite-api account hash` writes is at DefaultIterations,
// so the login answered about 75 ms sooner for a name with no account.
// A login for an unknown name must ask the derivation for exactly what a
// login for an account hashed by default does: the same iteration count,
// the same salt length, one call each, and be compared against a key of
// the same length.
func TestAnUnknownAccountIsVerifiedWithTheWorkOfADefaultHash(t *testing.T) {
	seen := countDerivations(t)

	// As the CLI makes it (cmd/halite-api/account.go passes
	// DefaultIterations), and as Hash's own default does.
	for _, asked := range []int{DefaultIterations, 0} {
		hash, err := Hash("pw", asked)
		if err != nil {
			t.Fatal(err)
		}
		*seen = nil
		known := &Account{Name: "ed", Hash: hash}
		known.Verify("guess")
		if len(*seen) != 1 {
			t.Fatalf("a known account's check derived %d times", len(*seen))
		}
		forKnown := (*seen)[0]

		*seen = nil
		var unknown *Account
		if unknown.Verify("guess") {
			t.Fatal("an unknown account verified")
		}
		if len(*seen) != 1 {
			t.Fatalf("an unknown account's check derived %d times", len(*seen))
		}
		if forUnknown := (*seen)[0]; forUnknown != forKnown {
			t.Errorf("Hash(%d): an unknown account asked for %+v, a known one for %+v",
				asked, forUnknown, forKnown)
		}

		v, err := parseHash(hash)
		if err != nil {
			t.Fatal(err)
		}
		if len(unknownAccount.key) != len(v.key) {
			t.Errorf("the unknown account's key is %d bytes and a real one %d",
				len(unknownAccount.key), len(v.key))
		}
	}
}

// No password matches the unknown account, the empty one included, with
// the real derivation: its key is never a success however it compares.
func TestNoPasswordVerifiesAnUnknownAccount(t *testing.T) {
	var unknown *Account
	if unknown.Verify("") {
		t.Error("the empty password verified an unknown account")
	}
}
