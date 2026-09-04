package winsec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A restricted file is one nobody but its owner, SYSTEM and
// Administrators can reach. The check has to hold against a real
// security descriptor rather than a synthesised mode, because the mode
// is what was wrong.
func TestRestrictLeavesNobodyElseWithAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restrict(path); err != nil {
		t.Fatalf("restricting the file: %v", err)
	}
	others, err := Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 0 {
		t.Errorf("after Restrict, these can still reach it: %v", others)
	}
	// And it is still the owner's own file.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the owner can no longer read it: %v", err)
	}
	if string(body) != "s3cret\n" {
		t.Errorf("contents = %q", body)
	}
}

// The check has to see an account that a state or an installer added,
// or it is not a check. Everyone is the widest such account and the one
// a careless icacls grants.
func TestOthersNamesAnAccountThatWasGrantedAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restrict(path); err != nil {
		t.Fatal(err)
	}
	if err := grantEveryone(t, path); err != nil {
		t.Fatalf("granting Everyone: %v", err)
	}
	others, err := Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) == 0 {
		t.Fatal("a file granted to Everyone was reported as private")
	}
	joined := strings.Join(others, ", ")
	if !strings.Contains(joined, "Everyone") {
		t.Errorf("the accounts reported are %q; Everyone is not among them", joined)
	}
}

// A file with an ordinary inherited list is not private, which is the
// state every file halite writes was in.
func TestAnOrdinaryFileIsNotAssumedPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plain")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := grantEveryone(t, path); err != nil {
		t.Fatal(err)
	}
	others, err := Others(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) == 0 {
		t.Fatal("a file anyone can read was reported as private")
	}
}
