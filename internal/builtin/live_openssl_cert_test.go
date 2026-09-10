package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `openssl_cert`, driven end to end against the real openssl on the
// machine running the tests.
//
// # Why this one needs no gate and no container
//
// Every other live test in this package is fenced: `hostname` renames a
// machine, `sysctl` moves a kernel parameter, `netplan` writes into
// /etc/netplan, and the fleet container exists because dpkg has to be
// Debian's own. None of that applies here. This module changes exactly
// one thing — a file it is told to write — and everything below writes
// only into `t.TempDir()`. It needs no root, no network and no
// particular distribution.
//
// That is what lets the *mutating* path be demonstrated rather than
// assumed: `pkcs12_create` really packs a real bundle with the real
// openssl, and `pkcs12_info` really reads it back through the tool that
// made it. DIVERGENCE 5.31's lesson is that a fixture written in the
// module's own spelling proves nothing, and the way out of it is a round
// trip through the actual program — which here costs a temporary
// directory.
//
// # What the CA below is, and is not
//
// It is a 2048-bit RSA CA made fresh for each run in a directory that is
// deleted afterwards, and it signs one leaf that is valid for a day.
// Nothing here reaches a trust store, a network or a file the machine
// came with.

// liveOpenSSL is a throwaway CA, its leaf, and the openssl that made
// them.
type liveOpenSSL struct {
	dir  string
	ca   string
	key  string
	leaf string
	lkey string
	c    *exec.Context
}

// liveOpenSSLSetup builds the CA and leaf, or skips.
func liveOpenSSLSetup(t *testing.T) liveOpenSSL {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this module is declared unix only")
	}
	c := &exec.Context{}
	if c.Which("openssl") == "" {
		t.Skip("this host has no `openssl` on PATH")
	}

	dir := t.TempDir()
	l := liveOpenSSL{
		dir:  dir,
		ca:   filepath.Join(dir, "ca.crt"),
		key:  filepath.Join(dir, "ca.key"),
		leaf: filepath.Join(dir, "leaf.crt"),
		lkey: filepath.Join(dir, "leaf.key"),
		c:    c,
	}
	csr := filepath.Join(dir, "leaf.csr")
	for _, argv := range [][]string{
		{"openssl", "req", "-x509", "-newkey", "rsa:2048", "-keyout", l.key, "-out", l.ca,
			"-days", "1", "-nodes", "-subj", "/CN=halite test CA"},
		{"openssl", "req", "-newkey", "rsa:2048", "-keyout", l.lkey, "-out", csr,
			"-nodes", "-subj", "/CN=leaf.halite.test"},
		{"openssl", "x509", "-req", "-in", csr, "-CA", l.ca, "-CAkey", l.key,
			"-CAcreateserial", "-out", l.leaf, "-days", "1"},
	} {
		if res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true}); err != nil || res.Code != 0 {
			t.Skipf("this host's openssl could not build the fixtures (%v): %s", err, strings.TrimSpace(res.Stderr))
		}
	}
	return l
}

func (l liveOpenSSL) call(t *testing.T, fn string, args *value.Map) *value.Map {
	t.Helper()
	out, err := New().Exec.Call(l.c, fn, args)
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	m, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("%s returned %T", fn, out)
	}
	return m
}

// A real chain verifies, and the same certificate without its CA does
// not — with the numbered reason this module claims to read.
//
// This is the assertion the parser fixtures cannot make. They hold
// output somebody pasted in; this holds whatever the installed openssl
// prints today, which is the thing that will differ on the version
// nobody here has.
func TestOpenSSLVerifyReadsWhatThisHostsOpenSSLPrints(t *testing.T) {
	l := liveOpenSSLSetup(t)

	ok := value.NewMap(2)
	ok.Set("certificate", l.leaf)
	ok.Set("ca_file", l.ca)
	out := l.call(t, "openssl_cert.verify", ok)

	if verified, _ := out.GetString("verified"); verified != true {
		t.Fatalf("a leaf verified against the CA that signed it was reported as failing: %v", out.Entries())
	}
	if code, _ := out.GetString("error_code"); code != int64(0) {
		t.Errorf("a successful verification carries error code %v", code)
	}

	// Against a *different* CA, openssl cannot find the issuer. Error 20
	// is that, and it is the code this module claims to read.
	//
	// The trust file has to be a real CA rather than an empty file. An
	// empty one is a different failure — openssl refuses to load it and
	// exits before verifying anything — and this test asserted against
	// an empty file until this host's openssl said so, which is what the
	// refusal below the assertion is now about.
	other := filepath.Join(l.dir, "other-ca.crt")
	otherKey := filepath.Join(l.dir, "other-ca.key")
	res, err := l.c.Run(exec.Command{Argv: []string{
		"openssl", "req", "-x509", "-newkey", "rsa:2048", "-keyout", otherKey, "-out", other,
		"-days", "1", "-nodes", "-subj", "/CN=a different CA",
	}, IgnoreExitCode: true})
	if err != nil || res.Code != 0 {
		t.Skipf("this host's openssl could not build a second CA: %s", strings.TrimSpace(res.Stderr))
	}

	bad := value.NewMap(2)
	bad.Set("certificate", l.leaf)
	bad.Set("ca_file", other)
	out = l.call(t, "openssl_cert.verify", bad)
	if verified, _ := out.GetString("verified"); verified != false {
		t.Fatal("a certificate whose issuer is unknown was reported as verified")
	}
	code, _ := out.GetString("error_code")
	reason, _ := out.GetString("reason")
	if code == int64(0) || reason == "" {
		raw, _ := out.GetString("output")
		t.Errorf("this host's openssl reported a failure this module read no reason out of.\n"+
			"  code:   %v\n  reason: %q\n  output: %s", code, reason, raw)
	}
	t.Logf("this host's openssl refused the chain as error %v: %v", code, reason)

	// And a trust file openssl cannot load is a refusal rather than an
	// untrusted certificate. Telling an operator their certificate is
	// not trusted would send them to renew one that is fine.
	empty := filepath.Join(l.dir, "empty.pem")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	unloadable := value.NewMap(2)
	unloadable.Set("certificate", l.leaf)
	unloadable.Set("ca_file", empty)
	if _, err := New().Exec.Call(l.c, "openssl_cert.verify", unloadable); err == nil {
		t.Error("an empty trust file was reported as an untrusted certificate rather than as a refusal")
	} else if !strings.Contains(err.Error(), "rather than an untrusted") {
		t.Errorf("the refusal does not distinguish the two: %v", err)
	}
}

// The chain listing this module parses is the one this host's openssl
// prints.
//
// It is skipped on LibreSSL, which has no `-show_chain`. Skipping is the
// honest outcome there: an empty chain from a tool that was never asked
// for one says nothing about the parser.
func TestTheChainThisHostPrintsIsTheOneThisModuleReads(t *testing.T) {
	l := liveOpenSSLSetup(t)
	info, err := openSSLFind(l.c)
	if err != nil {
		t.Fatal(err)
	}
	if info.LibreSSL {
		t.Skipf("%s has no -show_chain", info.Banner)
	}

	args := value.NewMap(2)
	args.Set("certificate", l.leaf)
	args.Set("ca_file", l.ca)
	out := l.call(t, "openssl_cert.verify", args)

	raw, _ := out.GetString("chain")
	chain, _ := raw.([]any)
	if len(chain) != 2 {
		output, _ := out.GetString("output")
		t.Fatalf("read %d chain entries from %s, want the leaf and its CA. openssl printed:\n%s",
			len(chain), info.Banner, output)
	}
	leaf := chain[0].(*value.Map)
	ca := chain[1].(*value.Map)
	if subject, _ := leaf.GetString("subject"); !strings.Contains(subject.(string), "leaf.halite.test") {
		t.Errorf("the leaf's subject read as %q", subject)
	}
	if subject, _ := ca.GetString("subject"); !strings.Contains(subject.(string), "halite test CA") {
		t.Errorf("the CA's subject read as %q", subject)
	}
	// The CA came from -CAfile, so openssl marks it trusted and the leaf
	// untrusted. Getting these the wrong way round would tell an operator
	// their CA is not installed when it is.
	if trusted, _ := ca.GetString("trusted"); trusted != true {
		t.Error("the CA was supplied as trusted and is reported as untrusted")
	}
	if trusted, _ := leaf.GetString("trusted"); trusted != false {
		t.Error("the leaf came from the file being verified and is reported as trusted")
	}
}

// A bundle this module writes is a bundle openssl reads, and the
// passphrase never appears in an argument.
//
// This is the mutating path, run for real. It is also where the
// passphrase discipline is checked against the tool rather than against
// the source: a wrong password must fail, which is only true if the
// right one was actually delivered down the pipe.
func TestAPKCS12BundleThisModuleWritesIsOneOpenSSLReads(t *testing.T) {
	l := liveOpenSSLSetup(t)
	bundle := filepath.Join(l.dir, "leaf.p12")
	const passphrase = "halite-live-test"

	create := value.NewMap(6)
	create.Set("path", bundle)
	create.Set("certificate", l.leaf)
	create.Set("private_key", l.lkey)
	create.Set("ca_certs", l.ca)
	create.Set("password", passphrase)
	create.Set("friendly_name", "halite-leaf")
	out := l.call(t, "openssl_cert.pkcs12_create", create)
	if changed, _ := out.GetString("changed"); changed != true {
		t.Fatalf("writing a bundle reported no change: %v", out.Entries())
	}

	// A bundle holds a private key, so it is nobody's business but its
	// owner's, whatever umask the agent was started with.
	info, err := os.Stat(bundle)
	if err != nil {
		t.Fatalf("the bundle was reported written and is not there: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the bundle is mode %04o, want 0600", mode)
	}

	read := value.NewMap(2)
	read.Set("path", bundle)
	read.Set("password", passphrase)
	got := l.call(t, "openssl_cert.pkcs12_info", read)

	if name, _ := got.GetString("friendly_name"); name != "halite-leaf" {
		t.Errorf("the friendly name read back as %q", name)
	}
	// The leaf and the CA both went in, so both come out.
	if n, _ := got.GetString("certificates"); n != int64(2) {
		t.Errorf("the bundle reads as holding %v certificates, want 2", n)
	}
	subjects, _ := got.GetString("subjects")
	var found bool
	for _, s := range subjects.([]any) {
		if strings.Contains(s.(string), "leaf.halite.test") {
			found = true
		}
	}
	if !found {
		t.Errorf("the leaf's subject is not among the bundle's: %v", subjects)
	}

	// The wrong passphrase must fail. If it did not, the right one was
	// never being delivered and every assertion above would pass on an
	// unprotected bundle.
	wrong := value.NewMap(2)
	wrong.Set("path", bundle)
	wrong.Set("password", passphrase+"-not")
	if _, err := New().Exec.Call(l.c, "openssl_cert.pkcs12_info", wrong); err == nil {
		t.Error("a bundle opened with the wrong passphrase, so the passphrase is not reaching openssl")
	}
}

// Test mode writes no bundle.
func TestWritingAPKCS12BundleInTestModeWritesNothing(t *testing.T) {
	l := liveOpenSSLSetup(t)
	bundle := filepath.Join(l.dir, "untouched.p12")

	args := value.NewMap(4)
	args.Set("path", bundle)
	args.Set("certificate", l.leaf)
	args.Set("private_key", l.lkey)
	args.Set("password", "unused")

	out, err := New().Exec.Call(&exec.Context{Test: true}, "openssl_cert.pkcs12_create", args)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Error("test mode did not predict the change it would make")
	}
	if _, err := os.Stat(bundle); err == nil {
		t.Error("a test run wrote the bundle")
	}
}

// A real revocation list is read: its issuer, its dates, and what is on
// it.
//
// The CA is built here rather than reused, because generating a CRL
// needs an index and a serial file that the shared fixture has no reason
// to carry.
func TestARealRevocationListIsReadByThisModule(t *testing.T) {
	l := liveOpenSSLSetup(t)

	caDir := filepath.Join(l.dir, "cadb")
	if err := os.MkdirAll(filepath.Join(caDir, "newcerts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"index.txt": "",
		"crlnumber": "01\n",
		"serial":    "1000\n",
	} {
		if err := os.WriteFile(filepath.Join(caDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	conf := filepath.Join(l.dir, "ca.cnf")
	if err := os.WriteFile(conf, []byte(""+
		"[ ca ]\ndefault_ca = CA_default\n\n[ CA_default ]\n"+
		"dir = "+caDir+"\ndatabase = $dir/index.txt\nnew_certs_dir = $dir/newcerts\n"+
		"certificate = "+l.ca+"\nprivate_key = "+l.key+"\nserial = $dir/serial\n"+
		"crlnumber = $dir/crlnumber\ndefault_md = sha256\ndefault_crl_days = 30\n"+
		"policy = policy_any\n\n[ policy_any ]\ncommonName = optional\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	crl := filepath.Join(l.dir, "test.crl")
	for _, argv := range [][]string{
		{"openssl", "ca", "-batch", "-config", conf, "-revoke", l.leaf},
		{"openssl", "ca", "-batch", "-config", conf, "-gencrl", "-out", crl},
	} {
		res, err := l.c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		if err != nil || res.Code != 0 {
			t.Skipf("this host's openssl could not build a revocation list: %s", strings.TrimSpace(res.Stderr))
		}
	}

	args := value.NewMap(1)
	args.Set("path", crl)
	out := l.call(t, "openssl_cert.crl_info", args)

	issuer, _ := out.GetString("issuer")
	if !strings.Contains(issuer.(string), "halite test CA") {
		t.Errorf("the issuer read as %q", issuer)
	}
	for _, field := range []string{"last_update", "next_update", "crl_number"} {
		v, ok := out.GetString(field)
		if !ok || strings.TrimSpace(v.(string)) == "" {
			t.Errorf("%s came back empty from a real revocation list", field)
		}
	}
	if form, _ := out.GetString("format"); form != "PEM" {
		t.Errorf("a PEM revocation list was read as %v", form)
	}

	revoked, _ := out.GetString("revoked")
	list, _ := revoked.([]any)
	if len(list) != 1 {
		t.Fatalf("one certificate was revoked and the list reads as %d entries", len(list))
	}
	entry := list[0].(*value.Map)
	serial, _ := entry.GetString("serial")
	date, _ := entry.GetString("revoked")
	if serial == "" || date == "" {
		t.Errorf("the revoked entry reads as serial %q revoked %q", serial, date)
	}
	t.Logf("read a real revocation list: %v revoked %v", serial, date)
}
