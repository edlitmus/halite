package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/roster"
	"github.com/edlitmus/halite/internal/value"
)

// installAWSExtension builds the shipped Secrets Manager extension and
// installs it, unsigned, in an extension cache, as `extensions sync`
// would lay it out. Signing has its own end-to-end test
// (internal/extpillar); this one is about which compiler runs it.
func installAWSExtension(t *testing.T) string {
	t.Helper()
	exe := "aws-secrets"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cache := t.TempDir()
	dir := filepath.Join(cache, "aws_secrets_manager", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(dir, exe), "../halite-ext-aws-secrets")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building the extension: %v", err)
	}
	manifest, err := extension.Build(dir, extension.Manifest{
		Name: "aws_secrets_manager", Version: "1.0.0", Kind: "pillar",
		Declares:    []string{"network"},
		Executables: map[string]string{runtime.GOOS + "/" + runtime.GOARCH: exe},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return cache
}

// A roster target's pillar is the one an enrolled node with its grains
// would be sent: compiled with the hub's own options, external sources
// included. The agentless path built its own compiler with neither the
// sources nor the `salt` dispatcher, so a target never received what
// Secrets Manager held, and a pillar file calling `salt['grains.get']`
// did not compile for it at all. DIVERGENCE 5.265.
func TestAgentlessPillarIsTheHubsPillar(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	const secret = "from-secrets-manager-for-a-roster-target"
	aws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"Name":"prod/db","SecretString":%q}`, `{"password":"`+secret+`"}`)
	}))
	t.Cleanup(aws.Close)

	root := t.TempDir()
	pillarRoot := filepath.Join(root, "pillar")
	if err := os.MkdirAll(pillarRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"top.sls":    "base:\n  '*':\n    - common\n",
		"common.sls": "role: {{ salt['grains.get']('role', 'none') }}\n",
	} {
		if err := os.WriteFile(filepath.Join(pillarRoot, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keyFile := filepath.Join(root, "secret-key")
	if err := os.WriteFile(keyFile, []byte("wJalrXUtnFEMI/K7MDENG/bPxRfiCY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	conf := fmt.Sprintf(`pillar_roots:
  base:
    - %s
state_dir: %s
cache_dir: %s
extension_dir: %s
extension_require_signature: false
ext_pillar:
  - aws_secrets_manager:
      endpoint: %s
      region: us-east-1
      access_key_id: AKIAIOSFODNN7EXAMPLE
      secret_access_key_file: %s
      secrets:
        - name: database
          secret_id: prod/db
`, pillarRoot, filepath.Join(root, "state"), filepath.Join(root, "cache"),
		installAWSExtension(t), aws.URL, keyFile)
	if err := os.WriteFile(filepath.Join(root, "hub.yaml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	args := &cli.Args{Flags: map[string]string{"root": root}}
	h := openHubForConfig(args)
	t.Cleanup(h.closeAgentlessPillar)

	target := roster.Target{ID: "db1.example", Grains: value.MapOf("role", "database")}
	got, secrets, err := inlinePillar(h, target, args)
	if err != nil {
		t.Fatalf("inlinePillar: %v", err)
	}
	decoded, err := value.DecodeJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	pillar := decoded.(*value.Map)
	if v, _ := value.Traverse(pillar, "aws_secrets:database:password", ":"); v != secret {
		t.Errorf("the target's pillar lacks what Secrets Manager held: %v", pillar.StringKeys())
	}
	if v, _ := pillar.Get("role"); v != "database" {
		t.Errorf("role = %v; salt['grains.get'] should read the target's roster grains", v)
	}
	if strings.Join(secrets, ",") != secret {
		t.Errorf("the target is told to mask %q, want only the Secrets Manager value", secrets)
	}
	if scrubbed := h.secrets.Scrub(secret); strings.Contains(scrubbed, secret) {
		t.Error("the hub's own redactor never heard the Secrets Manager value")
	}
}
