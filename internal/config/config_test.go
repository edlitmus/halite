package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/fileperm/permtest"
	"github.com/edlitmus/halite/internal/value"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMergesDropInsInLexicalOrder(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "node.yaml"), "hub: hub1.example\nlog_level: info\nfile_roots:\n  base:\n    - /srv/salt\n")
	write(t, filepath.Join(root, "node.d", "20-logging.yaml"), "log_level: debug\n")
	write(t, filepath.Join(root, "node.d", "10-roots.yaml"), "file_roots:\n  prod:\n    - /srv/prod\n")

	cfg, err := Load(Node, LoadOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Files) != 3 {
		t.Errorf("files = %v, want the primary plus two fragments", cfg.Files)
	}
	if got := cfg.String("log_level", ""); got != "debug" {
		t.Errorf("log_level = %q; the later fragment must win", got)
	}
	roots := cfg.Roots("file_roots")
	if len(roots["base"]) != 1 || len(roots["prod"]) != 1 {
		t.Errorf("file_roots = %v; a fragment must add an environment without restating the others", roots)
	}
}

func TestOverridesBeatEveryFile(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "node.yaml"), "log_level: info\n")
	cfg, err := Load(Node, LoadOptions{Root: root, Overrides: value.MapOf("log_level", "trace")})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.String("log_level", ""); got != "trace" {
		t.Errorf("log_level = %q, want the override", got)
	}
}

func TestMissingFileIsAnErrorUnlessAllowed(t *testing.T) {
	root := t.TempDir()
	if _, err := Load(Node, LoadOptions{Root: root}); err == nil {
		t.Error("a missing configuration file should be reported")
	}
	if _, err := Load(Node, LoadOptions{Root: root, AllowMissing: true}); err != nil {
		t.Errorf("AllowMissing should tolerate an absent file: %v", err)
	}
}

// TestSaltConfigTranslates covers SPEC section 27.5: reading a Salt minion
// configuration, reporting what was translated, and running.
func TestSaltConfigTranslates(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "minion")
	write(t, path, `master: salt.example
master_port: 4506
id: web1.prod
state_whitelist:
  - webserver.*
grains:
  role: web
nonsense_key: 1
`)
	cfg, err := Load(Node, LoadOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.String("hub", ""); got != "salt.example" {
		t.Errorf("master did not become hub: %q", got)
	}
	if got := cfg.Int("hub_port", 0); got != 4506 {
		t.Errorf("master_port did not become hub_port: %d", got)
	}
	if got := cfg.String("node_id", ""); got != "web1.prod" {
		t.Errorf("id did not become node_id: %q", got)
	}
	if got := cfg.StringSlice("state_allowlist"); len(got) != 1 || got[0] != "webserver.*" {
		t.Errorf("state_whitelist did not become state_allowlist: %v", got)
	}
	if cfg.Values.Has("master") || cfg.Values.Has("id") {
		t.Error("the Salt spellings should not survive the translation")
	}

	// Every translation is reported, once, with the removal version.
	if len(cfg.Shim.Translated) != 4 {
		t.Errorf("translated = %v, want four keys", cfg.Shim.Translated)
	}
	joined := strings.Join(cfg.Warnings, "\n")
	if !strings.Contains(joined, ShimRemovalVersion) {
		t.Errorf("warnings must name the removal version: %s", joined)
	}
	// A key that is neither halite's nor Salt's is reported, not silently
	// dropped.
	if len(cfg.Shim.Unknown) != 1 || cfg.Shim.Unknown[0] != "nonsense_key" {
		t.Errorf("unknown = %v", cfg.Shim.Unknown)
	}
	// A key halite already owns is not reported as translated.
	if got := cfg.Map("grains"); got == nil {
		t.Error("grains should pass through untouched")
	}
}

// TestAutoAcceptIsRefused is the one place the shim declines to be helpful.
func TestAutoAcceptIsRefused(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "master")
	write(t, path, "auto_accept: True\nfile_roots:\n  base: [/srv/salt]\n")

	cfg, err := Load(Hub, LoadOptions{Path: path})
	if err == nil {
		t.Fatal("auto_accept must be refused, not translated")
	}
	if !strings.Contains(err.Error(), "enrollment_mode") {
		t.Errorf("the refusal should point at the supported path: %v", err)
	}
	if cfg.Values.Has("auto_accept") {
		t.Error("auto_accept must not survive into the configuration")
	}
	if len(cfg.Shim.Refused) != 1 {
		t.Errorf("refused = %v", cfg.Shim.Refused)
	}
}

func TestACLKeysArePreservedForReview(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "master")
	write(t, path, "publisher_acl:\n  ops:\n    - test.ping\npeer:\n  '.*':\n    - grains.items\n")

	cfg, err := Load(Hub, LoadOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	// A mechanical translation into RBAC is not sound, so the keys are
	// kept under legacy_acl for the migration tool to draft from.
	legacy := cfg.Map("legacy_acl")
	if legacy == nil || !legacy.Has("publisher_acl") || !legacy.Has("peer") {
		t.Fatalf("legacy_acl = %v", legacy)
	}
	if cfg.Values.Has("policy") {
		t.Error("the shim must not fabricate a policy")
	}
}

func TestTypedAccessors(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "node.yaml"), `hub: h
job_queue_depth: 250
parallel_jobs: true
grains_refresh_interval: 45m
hub_alive_interval: 90
extension_trust_keys:
  - key-a
  - key-b
`)
	cfg, err := Load(Node, LoadOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Int("job_queue_depth", 0); got != 250 {
		t.Errorf("Int = %d", got)
	}
	if !cfg.Bool("parallel_jobs", false) {
		t.Error("Bool = false")
	}
	if got := cfg.Duration("grains_refresh_interval", 0).Minutes(); got != 45 {
		t.Errorf("Duration from a string = %v", got)
	}
	if got := cfg.Duration("hub_alive_interval", 0).Seconds(); got != 90 {
		t.Errorf("Duration from a bare number should be seconds, got %v", got)
	}
	if got := cfg.StringSlice("extension_trust_keys"); len(got) != 2 {
		t.Errorf("StringSlice = %v", got)
	}
}

// TestRedactedHidesSecrets covers what `opts` is bound to in a template:
// a template that can read the configuration must not be able to read a
// credential out of it.
func TestRedactedHidesSecrets(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "hub.yaml"), `listen: ":4510"
ext_pillar:
  vault:
    url: https://vault.example
    token: s.verysecret
    api_key: abc123
`)
	cfg, err := Load(Hub, LoadOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	red := cfg.Redacted()
	got, _ := value.Traverse(red, "ext_pillar:vault:token", ":")
	if got != "<redacted>" {
		t.Errorf("token = %#v, want it redacted", got)
	}
	got, _ = value.Traverse(red, "ext_pillar:vault:api_key", ":")
	if got != "<redacted>" {
		t.Errorf("api_key = %#v, want it redacted", got)
	}
	got, _ = value.Traverse(red, "ext_pillar:vault:url", ":")
	if got != "https://vault.example" {
		t.Errorf("url should survive: %#v", got)
	}
	// Redaction must not damage the loaded configuration itself.
	live, _ := cfg.Get("ext_pillar:vault:token")
	if live != "s.verysecret" {
		t.Errorf("Redacted mutated the configuration: %#v", live)
	}
}

func TestKeyTableCoversTheShimTargets(t *testing.T) {
	// Every key the shim renames into must be a key some role recognises,
	// or the loader would report the translated key as unknown.
	for _, r := range Renames() {
		if r.Halite == r.Salt || r.Halite == "policy" {
			continue
		}
		known := false
		for _, role := range []Role{Node, Hub, API} {
			if IsKnownKey(role, r.Halite) {
				known = true
				break
			}
		}
		if !known {
			t.Errorf("the shim renames %q to %q, which no role recognises", r.Salt, r.Halite)
		}
	}
}

// A key that names a location holds a path, and the path is not the
// secret. Redacting it turns "the secret at /etc/halite/deploy.secret is
// mode 644" into a diagnostic with the one fact an operator needed taken
// out of it — which is how this was found.
func TestAKeyNamingAFileIsNotItselfSecret(t *testing.T) {
	secret := []string{
		"password", "smtp_passwd", "returner_webhook_secret",
		"api_token", "key_data", "private_key", "shared_secret",
		"gpg_passphrase", "priv", "priv_passwd",
	}
	notSecret := []string{
		"returner_webhook_secret_file", "secret_file", "token_file",
		"password_file", "private_key_path", "tls_cert", "state_dir",
	}
	for _, key := range secret {
		if !IsSecretKey(key) {
			t.Errorf("%s is not treated as secret", key)
		}
	}
	for _, key := range notSecret {
		if IsSecretKey(key) {
			t.Errorf("%s is redacted, and it names a path", key)
		}
	}
}

// The contents still are, wherever they are read.
func TestASecretFilesContentsAreRefusedWhenReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deploy.secret")
	if err := os.WriteFile(path, []byte("s3cret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Opened and closed the way the platform does it. A chmod is not
	// that on Windows, and while this test used one it asserted a
	// refusal that arrived for the wrong reason: os.Stat reports 0666
	// for every writable file there, so no secret file was ever
	// accepted and the message told the operator to run a chmod.
	permtest.OpenToEveryone(t, path)
	_, err := ReadSecretFile(path)
	if err == nil {
		t.Fatal("a world-readable signing key was accepted")
	}
	// The path is in the message, because the operator has to know
	// which file.
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error does not name the file: %v", err)
	}

	permtest.MakePrivate(t, path)
	got, err := ReadSecretFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The trailing newline an editor adds is removed: a secret that
	// works when pasted and fails when read from a file is a diagnosis
	// nobody enjoys.
	if got != "s3cret" {
		t.Errorf("read %q", got)
	}

	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecretFile(path); err == nil {
		t.Error("an empty secret file was accepted")
	}
}
