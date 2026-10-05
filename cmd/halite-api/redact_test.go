package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/fileperm/permtest"
)

// Every secret this service holds is removed from every record it logs
// and every fatal message it prints.
//
// SPEC 26.1 puts the redactor at the sink, so that a log line added
// later cannot forget to call it. The hub and the node had one; this
// service built its logger with none and left cli.Redact unset, so the
// guarantee did not hold for the process that holds the directory's bind
// password, the identity provider's client secret and every webhook's
// shared secret. No line that prints one was found -- the call sites are
// careful -- which is the case the sink exists for: careful is a property
// of today's call sites, and the next one is written by somebody else.
//
// So this checks the sink rather than a call site. Each secret reaches
// the redactor by a different route: the inline settings through setup,
// the `_file` forms where they are read, the hooks' secrets after they
// parse, one of them from a file of its own. A route that is missed
// leaves exactly that secret printable, and the assertion names it.
func TestEverySecretTheAPIHoldsIsScrubbedFromItsLog(t *testing.T) {
	root := t.TempDir()
	logFile := filepath.Join(root, "api.log")
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		// ReadSecretFile refuses a file anyone else can read, and on
		// Windows a 0600 passed to WriteFile is not that: the file
		// inherits the temporary directory's ACL. The refusal is a
		// cli.Fatalf, which takes the test binary with it.
		permtest.MakePrivate(t, path)
		return path
	}
	secrets := map[string]string{
		"ldap_bind_password":      "inline-bind-pw-7f3a",
		"oidc_client_secret_file": "file-client-secret-91c2",
		"hook secret":             "hook-shared-secret-5d10",
		"hook secret_file":        "hook-file-secret-ab44",
	}
	oidcFile := write("oidc.secret", secrets["oidc_client_secret_file"]+"\n")
	hookFile := write("hook.secret", secrets["hook secret_file"]+"\n")
	write("api.yaml", "state_dir: "+filepath.Join(root, "state")+"\n"+
		"log_file: "+logFile+"\n"+
		"ldap_bind_password: "+secrets["ldap_bind_password"]+"\n"+
		"oidc_client_secret_file: "+oidcFile+"\n"+
		"hooks:\n"+
		"  deploy:\n    auth: hmac\n    secret: "+secrets["hook secret"]+"\n"+
		"  build:\n    auth: token\n    secret_file: "+hookFile+"\n")

	args, err := cli.Parse([]string{"serve", "--root", root})
	if err != nil {
		t.Fatal(err)
	}
	saved := cli.Redact
	t.Cleanup(func() { cli.Redact = saved })

	s := setup(args)
	t.Cleanup(func() { _ = s.log.Close() })
	// The routes serve takes, short of listening.
	loadHooks(s)
	if got := oidcSecret(s); got != secrets["oidc_client_secret_file"] {
		t.Fatalf("the client secret file read as %q", got)
	}

	var all []string
	for _, v := range secrets {
		all = append(all, v)
	}
	line := "the provider said: " + strings.Join(all, " / ")
	s.log.Warn(line, "detail", strings.Join(all, ","))

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "the provider said") {
		t.Fatalf("the record was not written, so nothing was checked:\n%s", data)
	}
	if cli.Redact == nil {
		t.Error("cli.Redact is unset, so a fatal message is printed unscrubbed")
	}
	for route, v := range secrets {
		if strings.Contains(string(data), v) {
			t.Errorf("the %s reached the log:\n%s", route, data)
		}
		if cli.Redact != nil && strings.Contains(cli.Redact(line), v) {
			t.Errorf("the %s survives cli.Redact", route)
		}
	}
}

// The bind password's file form, which cannot share a configuration with
// the inline form the test above uses: the file is preferred, and the
// inline value would be seeded whatever happened to the file's.
func TestTheLDAPBindPasswordFileIsScrubbed(t *testing.T) {
	root := t.TempDir()
	const secret = "file-bind-pw-c0d3"
	pwFile := filepath.Join(root, "bind.secret")
	if err := os.WriteFile(pwFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	permtest.MakePrivate(t, pwFile)
	conf := "state_dir: " + filepath.Join(root, "state") + "\n" +
		"ldap_bind_password_file: " + pwFile + "\n"
	if err := os.WriteFile(filepath.Join(root, "api.yaml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	args, err := cli.Parse([]string{"serve", "--root", root})
	if err != nil {
		t.Fatal(err)
	}
	saved := cli.Redact
	t.Cleanup(func() { cli.Redact = saved })

	s := setup(args)
	t.Cleanup(func() { _ = s.log.Close() })
	if got := ldapBindPassword(s); got != secret {
		t.Fatalf("the bind password file read as %q", got)
	}
	if got := s.secrets.Scrub("bind failed with " + secret); strings.Contains(got, secret) {
		t.Errorf("the bind password from its file is not scrubbed: %q", got)
	}
}
