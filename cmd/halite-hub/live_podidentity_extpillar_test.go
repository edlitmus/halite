package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/roster"
	"github.com/edlitmus/halite/internal/value"
)

// Issue 613, item 1, through the extension runtime: aws_secrets_manager in
// an EKS pod, reading a real secret with Pod Identity credentials.
//
// This is the case the environment path cannot reach. An extension is
// started with PATH and HOME and nothing else, so the two variables EKS
// sets never arrive; the block names them instead, through
// `container_credentials_full_uri` and `container_authorization_token_file`.
// The hub's own compiler runs it, as TestAgentlessPillarIsTheHubsPillar
// does with a stand-in -- so the sandbox, the clean environment and the
// account drop are the ones a hub applies.
//
// Gated on HALITE_SYSTEM_LIVE=1 in a pod with a Pod Identity association,
// and skipped, saying why, anywhere else. The extension is not built here:
// a pod has no Go toolchain, so HALITE_EXT_AWS_SECRETS_EXE names a binary
// built for it. HALITE_EXTENSION_USER, when set, is the account the
// extension drops to, which is what decides whether it can read the token
// file; unset, it runs as the hub does. Nothing is changed: the secret is
// read, and the value checked is HALITE_POD_IDENTITY_SECRET_ANSWER.
func TestLiveAWSSecretsReadsThroughPodIdentity(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 in an EKS pod with a Pod Identity association")
	}
	need := map[string]string{}
	for _, k := range []string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", "AWS_REGION",
		"HALITE_EXT_AWS_SECRETS_EXE", "HALITE_POD_IDENTITY_SECRET", "HALITE_POD_IDENTITY_SECRET_ANSWER",
	} {
		v := os.Getenv(k)
		if v == "" {
			t.Skipf("%s is not set, so this is not a configured pod with the extension built for it", k)
		}
		need[k] = v
	}
	user := os.Getenv("HALITE_EXTENSION_USER")
	t.Logf("extension_user %q", user)

	root := t.TempDir()
	// An extension that drops to another account has to reach its own
	// executable; t.TempDir's directories are the test's alone.
	for _, d := range []string{filepath.Dir(root), root} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pillarRoot := filepath.Join(root, "pillar")
	if err := os.MkdirAll(pillarRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pillarRoot, "top.sls"), []byte("base: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cache := filepath.Join(root, "cache")
	// cmd/halite-ext-aws-secrets's Version, as in installAWSExtension: the
	// hub refuses an executable laid out as any other.
	const version = "1.3.0"
	dir := filepath.Join(cache, "aws_secrets_manager", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	copyExecutable(t, need["HALITE_EXT_AWS_SECRETS_EXE"], filepath.Join(dir, "aws-secrets"))
	manifest, err := extension.Build(dir, extension.Manifest{
		Name: "aws_secrets_manager", Version: version, Kind: "pillar",
		Declares:    []string{"network"},
		Executables: map[string]string{runtime.GOOS + "/" + runtime.GOARCH: "aws-secrets"},
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

	userLine := ""
	if user != "" {
		userLine = "extension_user: " + user + "\n"
	}
	conf := fmt.Sprintf(`pillar_roots:
  base:
    - %s
state_dir: %s
cache_dir: %s
extension_dir: %s
extension_require_signature: false
%sext_pillar:
  - aws_secrets_manager:
      region: %s
      container_credentials_full_uri: %s
      container_authorization_token_file: %s
      secrets:
        - name: podidentity
          secret_id: %s
`, pillarRoot, filepath.Join(root, "state"), filepath.Join(root, "hubcache"), cache, userLine,
		need["AWS_REGION"], need["AWS_CONTAINER_CREDENTIALS_FULL_URI"],
		need["AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE"], need["HALITE_POD_IDENTITY_SECRET"])
	if err := os.WriteFile(filepath.Join(root, "hub.yaml"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	// HALITE_PREPARE_WORKDIR=1 makes the extension's working directory,
	// owned by HALITE_EXTENSION_USER, before the hub does. It exists to
	// test one explanation of a failure: run first without it, a dropped
	// extension failed `fork/exec ...: permission denied` in an EKS pod
	// (2026-11-25), and the runtime makes that directory as the hub, mode
	// 0700, and starts the extension in it as another account.
	if user != "" && os.Getenv("HALITE_PREPARE_WORKDIR") == "1" {
		prepareWorkDir(t, filepath.Join(root, "state", "ext-work", "aws_secrets_manager"), user)
	}

	args := &cli.Args{Flags: map[string]string{"root": root}}
	h := openHubForConfig(args)
	t.Cleanup(h.closeAgentlessPillar)

	got, _, err := inlinePillar(h, roster.Target{ID: "pod.example"}, args)
	if err != nil {
		t.Fatalf("compiling the pillar through aws_secrets_manager: %v", err)
	}
	decoded, err := value.DecodeJSON(got)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := value.Traverse(decoded.(*value.Map), "aws_secrets:podidentity:answer", ":")
	if v != need["HALITE_POD_IDENTITY_SECRET_ANSWER"] {
		t.Fatalf("aws_secrets:podidentity:answer is %v, not the secret's value", v)
	}
	t.Logf("read the secret through Pod Identity as %q", orHub(user))
}

func orHub(user string) string {
	if user == "" {
		return "the hub's own account"
	}
	return user
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
