package builtin

import (
	"regexp"
	"runtime"
	"testing"
)

// mac_keychain reads, against the real `security` on this Mac.
//
// # Why it needs no gate
//
// `security list-keychains`, `default-keychain` and `find-certificate
// -a -Z` read and change nothing. `import` and `delete-certificate`
// change a keychain and need root for a system one, so they are not
// exercised and no CI leg is a Mac. `evidence.go` records the module
// `assumed`.
//
// # What it establishes
//
// That the search list and default keychain read back as file paths,
// that `find-certificate -a -Z` on the System keychain — which every
// Mac has, with certificates in it — parses into name/hash pairs whose
// hashes are 40 hex digits, and that a by-name lookup finds the same
// hash the bulk read did.

var sha1Hex = regexp.MustCompile(`^[0-9A-Fa-f]{40}$`)

func TestLiveMacKeychainReadsThisMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_keychain is macOS's, and this is %s", runtime.GOOS)
	}
	c := realCtx(t)
	if c.Which("security") == "" {
		t.Skip("no `security` on this machine")
	}

	list, err := macKeychainList(c, "")
	if err != nil {
		t.Fatalf("list-keychains: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("the keychain search list is empty")
	}

	def, err := macKeychainPaths(c, "default-keychain")
	if err != nil || len(def) == 0 {
		t.Fatalf("default-keychain: %v (%v)", def, err)
	}

	const system = "/Library/Keychains/System.keychain"
	certs, err := macKeychainCerts(c, system, "")
	if err != nil {
		t.Fatalf("find-certificate on the System keychain: %v", err)
	}
	if len(certs) == 0 {
		t.Fatal("the System keychain read back with no certificates")
	}
	var named string
	for _, cert := range certs {
		if cert.Hash != "" && !sha1Hex.MatchString(cert.Hash) {
			t.Errorf("hash %q is not 40 hex digits", cert.Hash)
		}
		if named == "" && cert.Name != "" && cert.Hash != "" {
			named = cert.Name
		}
	}
	if named == "" {
		t.Fatal("no certificate had both a name and a hash")
	}

	// A by-name lookup finds a hash for one the bulk read named.
	one, err := macKeychainCerts(c, system, named)
	if err != nil {
		t.Fatalf("by-name lookup of %q: %v", named, err)
	}
	if len(one) == 0 || one[0].Hash == "" {
		t.Errorf("by-name lookup of %q returned %+v", named, one)
	}
}
