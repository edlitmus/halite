package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/account"
	"github.com/edlitmus/halite/internal/policy"
)

func examplePath(name string) string {
	return filepath.Join("..", "..", "contrib", "examples", name)
}

// loadExampleAccounts reads contrib/examples/accounts.yaml with the
// parser halite-api uses. An account file that will not load is a
// service that will not start.
func loadExampleAccounts(t *testing.T) *account.File {
	t.Helper()
	src, err := os.ReadFile(examplePath("accounts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := account.Load(src, examplePath("accounts.yaml"))
	if err != nil {
		t.Fatalf("the example account file does not load: %v", err)
	}
	return f
}

// The example is documentation, so its claims are decisions here.
func TestTheExampleAccountsAreWhatTheyClaimToBe(t *testing.T) {
	f := loadExampleAccounts(t)

	want := []string{"backup-agent", "ci-deploy", "ed", "oncall"}
	if got := f.Names(); len(got) != len(want) {
		t.Fatalf("the example holds %v", got)
	}

	if a := f.Accounts["oncall"]; !a.NeedsSecondFactor() {
		t.Error("the account documented as carrying a second factor does not")
	}
	if a := f.Accounts["ci-deploy"]; a.NeedsSecondFactor() {
		t.Error("an automation identity was given a second factor nothing can type")
	}
	if a := f.Accounts["backup-agent"]; !a.Disabled {
		t.Error("the account documented as disabled is not")
	}
	if a := f.Accounts["ed"]; a.Disabled {
		t.Error("the operator account is disabled")
	}
}

// The file says its hashes cannot be logged into. That is the claim
// that matters, because an example account file is exactly the thing
// somebody copies into production intact.
//
// The passwords were 32 random bytes that were never recorded, so
// nothing matches. What is guarded here is the other failure: somebody
// replacing one of them with a hash of a password that can be guessed,
// which would ship a working account in a public repository.
//
// Each Verify is a full PBKDF2 at this build's cost, so the candidates
// are few and the accounts run in parallel. A longer list is a slower
// suite and not a better guard: anybody adding a real password here
// would use one of these or something no list would hold.
func TestNoExampleAccountCanBeLoggedInto(t *testing.T) {
	f := loadExampleAccounts(t)

	for _, name := range f.Names() {
		a := f.Accounts[name]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, guess := range []string{name, "", "password", "changeme", "halite", "hunter2"} {
				if a.Verify(guess) {
					t.Errorf("the example account %q can be logged into with %q; "+
						"replace the hash with one generated from bytes nobody kept",
						name, guess)
				}
			}
		})
	}
}

// The hashes are the ones this repository generated, and changing one is
// a deliberate act rather than a tidy-up.
//
// This is the cheap half of the guard above: it costs no PBKDF2 at all
// and it catches a replacement whether the new password is guessable or
// not. If these fail after a legitimate rotation, generate the new
// hashes the way the file says and update the digests here in the same
// commit.
func TestTheExampleHashesAreTheOnesThatWereShipped(t *testing.T) {
	f := loadExampleAccounts(t)

	shipped := map[string]string{
		"backup-agent": "8ef0e4969e2a69aa186fe43fc6f322304caddc6dc70c92bd89e93f0a50774d3e",
		"ci-deploy":    "7288a92453b6e39a2f11326f38ad577abc6ea164f372fd944eb73a5df529db69",
		"ed":           "e97593e6ccb53cf586fa78ae445b28951b7dd82dcd2c6747dbee75fcfbfd057f",
		"oncall":       "ae9a28c2707cbba90c76394f51096534c977d35e9ef08f3f5b3ba5b2c359f0b4",
	}
	for _, name := range f.Names() {
		want, ok := shipped[name]
		if !ok {
			t.Errorf("the example gained an account %q; add the digest of its hash here", name)
			continue
		}
		if got := sha256Hex(f.Accounts[name].Hash); got != want {
			t.Errorf("account %q carries a different hash from the one shipped.\n"+
				"If this is a deliberate rotation, generate it the way accounts.yaml "+
				"says and update the digest here in the same commit.\n  got  %s\n  want %s",
				name, got, want)
		}
		delete(shipped, name)
	}
	for name := range shipped {
		t.Errorf("the example no longer holds an account named %q", name)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Every role an account names has to exist in the example policy, or it
// grants nothing — silently, which is the failure the account file's own
// comment warns about. The two examples are read together because that
// is how they are deployed.
func TestEveryRoleTheExampleAccountsNameExistsInTheExamplePolicy(t *testing.T) {
	f := loadExampleAccounts(t)

	src, err := os.ReadFile(examplePath("policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := policy.Load(src, examplePath("policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, name := range f.Names() {
		for _, role := range f.Accounts[name].Roles {
			checked++
			if _, ok := p.Roles[role]; !ok {
				t.Errorf("account %q names role %q, which policy.yaml does not define",
					name, role)
			}
		}
	}
	if checked == 0 {
		t.Error("no account names a role; this check has stopped checking")
	}
}
