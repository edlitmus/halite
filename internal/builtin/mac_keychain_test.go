package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `security find-certificate -a -Z` output: a SHA-1 hash line before each
// certificate's attribute block, `labl` giving the name, and one name in
// hex for the non-ASCII case.
const findCertificateOutput = `SHA-1 hash: 0CC91C390D656832486518BB0297C91D5085C408
keychain: "/Library/Keychains/System.keychain"
    "alis"<blob>="com.apple.systemdefault"
    "labl"<blob>="com.apple.systemdefault"
SHA-1 hash: AAFC12991A84C4F2A8E5C90DD653062EA06A0EC3
    "labl"<blob>="com.apple.kerberos.kdc"
SHA-1 hash: 6D342A803B262D573AE04AEE5769E62F892F169F
    "labl"<blob>=0x63616665 "cafe"
`

func macKeychainCtx(t *testing.T, responses map[string]exec.Result) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	runner := &exec.RecordingRunner{Responses: responses}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "security" || name == "openssl" {
			return "/usr/bin/" + name
		}
		return ""
	}
	return c, runner
}

func TestParseFindCertificatePairsHashesAndNames(t *testing.T) {
	certs := parseFindCertificate(findCertificateOutput)
	if len(certs) != 3 {
		t.Fatalf("read %d certs, want 3: %+v", len(certs), certs)
	}
	if certs[0].Name != "com.apple.systemdefault" || certs[0].Hash != "0CC91C390D656832486518BB0297C91D5085C408" {
		t.Errorf("cert 0 = %+v", certs[0])
	}
	if certs[1].Name != "com.apple.kerberos.kdc" || certs[1].Hash != "AAFC12991A84C4F2A8E5C90DD653062EA06A0EC3" {
		t.Errorf("cert 1 = %+v", certs[1])
	}
	// The hex name decodes.
	if certs[2].Name != "cafe" {
		t.Errorf("cert 2 name = %q, want the decoded \"cafe\"", certs[2].Name)
	}
}

func TestMacKeychainListCerts(t *testing.T) {
	c, _ := macKeychainCtx(t, map[string]exec.Result{
		"security find-certificate -a -Z": {Stdout: findCertificateOutput},
	})
	certs, err := macKeychainCerts(c, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 3 {
		t.Fatalf("got %d", len(certs))
	}
}

func TestMacKeychainListKeychains(t *testing.T) {
	c, runner := macKeychainCtx(t, map[string]exec.Result{
		"security list-keychains":           {Stdout: "    \"/Users/x/Library/Keychains/login.keychain-db\"\n    \"/Library/Keychains/System.keychain\"\n"},
		"security list-keychains -d system": {Stdout: "    \"/Library/Keychains/System.keychain\"\n"},
	})
	all, err := macKeychainList(c, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0] != "/Users/x/Library/Keychains/login.keychain-db" {
		t.Errorf("list = %v", all)
	}
	sys, err := macKeychainList(c, "system")
	if err != nil {
		t.Fatal(err)
	}
	if len(sys) != 1 || sys[0] != "/Library/Keychains/System.keychain" {
		t.Errorf("system list = %v", sys)
	}
	if !ranArgv(runner, "security", "list-keychains", "-d", "system") {
		t.Errorf("domain was not passed: %v", runner.RanCommands())
	}
}

func TestMacKeychainInstallArgv(t *testing.T) {
	c, runner := macKeychainCtx(t, nil)
	err := macKeychainInstall(c, "/tmp/id.p12", "s3cret", "/Library/Keychains/System.keychain", true,
		[]string{"/usr/bin/codesign", "/usr/bin/productsign"})
	if err != nil {
		t.Fatal(err)
	}
	if !ranArgv(runner, "security", "import", "/tmp/id.p12",
		"-k", "/Library/Keychains/System.keychain", "-P", "s3cret", "-A",
		"-T", "/usr/bin/codesign", "-T", "/usr/bin/productsign") {
		t.Errorf("import argv = %v", runner.RanCommands())
	}
}

func TestMacKeychainInstallToleratesAlreadyExists(t *testing.T) {
	c, _ := macKeychainCtx(t, map[string]exec.Result{
		"security import /tmp/id.p12 -P pw": {Code: 1, Stderr: "security: SecKeychainItemImport: The specified item already exists in the keychain.\n"},
	})
	if err := macKeychainInstall(c, "/tmp/id.p12", "pw", "", false, nil); err != nil {
		t.Errorf("an already-present certificate was an error: %v", err)
	}
}

func TestMacKeychainUninstall(t *testing.T) {
	c, runner := macKeychainCtx(t, map[string]exec.Result{
		"security delete-certificate -c gone": {Code: 1, Stderr: "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain.\n"},
	})
	// A missing certificate is the converged state, not a failure.
	if err := macKeychainUninstall(c, "gone", ""); err != nil {
		t.Errorf("uninstall of a missing cert errored: %v", err)
	}
	if err := macKeychainUninstall(c, "present", "/Library/Keychains/System.keychain"); err != nil {
		t.Fatal(err)
	}
	if !ranArgv(runner, "security", "delete-certificate", "-c", "present", "/Library/Keychains/System.keychain") {
		t.Errorf("uninstall argv = %v", runner.RanCommands())
	}
}

func TestMacKeychainGetHashByName(t *testing.T) {
	c, runner := macKeychainCtx(t, map[string]exec.Result{
		"security find-certificate -c com.apple.kerberos.kdc -Z": {
			Stdout: "SHA-1 hash: AAFC12991A84C4F2A8E5C90DD653062EA06A0EC3\n    \"labl\"<blob>=\"com.apple.kerberos.kdc\"\n",
		},
	})
	certs, err := macKeychainCerts(c, "", "com.apple.kerberos.kdc")
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 1 || certs[0].Hash != "AAFC12991A84C4F2A8E5C90DD653062EA06A0EC3" {
		t.Errorf("by-name lookup = %+v", certs)
	}
	if !ranArgv(runner, "security", "find-certificate", "-c", "com.apple.kerberos.kdc", "-Z") {
		t.Errorf("by-name argv = %v", runner.RanCommands())
	}
}

func TestMacKeychainRegisteredAndRestricted(t *testing.T) {
	r := New()
	for _, name := range []string{
		"mac_keychain.list_keychains", "mac_keychain.default_keychain", "mac_keychain.list_certs",
		"mac_keychain.get_hash", "mac_keychain.friendly_name", "mac_keychain.install", "mac_keychain.uninstall",
	} {
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if len(sig.Platforms) != 1 || sig.Platforms[0] != "darwin" {
			t.Errorf("%s platforms = %v", name, sig.Platforms)
		}
	}
}

// What `security` says when there is nothing to delete, and what it says
// when there is nothing to find. They are different sentences from
// different subcommands, and the module used to confuse them.
//
// Captured from the real `security` on macOS 27.0 (build 26A5425a).
const (
	securityDeleteMissing = "Unable to delete certificate matching \"halite-no-such-cert\"\n"
	securityFindMissing   = "security: SecKeychainSearchCopyNext: The specified item " +
		"could not be found in the keychain.\n"
)

// Uninstalling a certificate that is already gone is the state that was
// asked for, not a failure -- or `mac_keychain.uninstall` under
// `module.run` fails on every run after the first.
func TestMacKeychainUninstallToleratesAnAbsentCertificate(t *testing.T) {
	for name, out := range map[string]string{
		"what delete-certificate actually says": securityDeleteMissing,
		"the find spelling, kept as well":       securityFindMissing,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := macKeychainCtx(t, map[string]exec.Result{
				"security delete-certificate -c halite-no-such-cert": {Code: 1, Stderr: out},
			})
			if err := macKeychainUninstall(c, "halite-no-such-cert", ""); err != nil {
				t.Errorf("uninstalling an absent certificate was an error: %v", err)
			}
		})
	}
}

// Any other failure is still a failure, and still says what `security`
// said.
func TestMacKeychainUninstallReportsARealFailure(t *testing.T) {
	c, _ := macKeychainCtx(t, map[string]exec.Result{
		"security delete-certificate -c halite-selftest /Library/Keychains/System.keychain": {
			Code: 1, Stderr: "SecKeychainDeleteItem: User interaction is not allowed.\n",
		},
	})
	err := macKeychainUninstall(c, "halite-selftest", "/Library/Keychains/System.keychain")
	if err == nil {
		t.Fatal("a delete that failed on permissions was reported as success")
	}
	if !strings.Contains(err.Error(), "User interaction is not allowed") {
		t.Errorf("the error lost what security said: %v", err)
	}
}

// What `security default-keychain` says for an account that has none.
// Captured from the real `security` on macOS 27.0 (build 26A5425a),
// running as root.
const securityNoDefaultKeychain = "security: SecKeychainCopyDefault: A default keychain " +
	"could not be found.\n"

// An account with no default keychain gets an answer, not an error.
//
// root is such an account on a stock Mac, and root is what halite-node
// runs as, so this is the path every real node takes.
func TestMacKeychainDefaultKeychainIsEmptyForAnAccountWithoutOne(t *testing.T) {
	r := New()
	c, _ := macKeychainCtx(t, map[string]exec.Result{
		"security default-keychain": {Code: 1, Stderr: securityNoDefaultKeychain},
	})

	got, err := r.Exec.Call(c, "mac_keychain.default_keychain", value.NewMap(0))
	if err != nil {
		t.Fatalf("default_keychain on an account with none was an error, which is what "+
			"every halite node running as root would get: %v", err)
	}
	if got != "" {
		t.Errorf("default_keychain returned %#v, want the empty string", got)
	}
}

// A `default-keychain` that fails for any other reason still fails.
func TestMacKeychainDefaultKeychainReportsARealFailure(t *testing.T) {
	c, _ := macKeychainCtx(t, map[string]exec.Result{
		"security default-keychain": {Code: 1, Stderr: "security: something else went wrong\n"},
	})
	if _, err := macKeychainPaths(c, "default-keychain"); err == nil {
		t.Fatal("an unrelated failure was reported as an empty answer")
	}
}
