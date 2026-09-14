package extpillar

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// The whole chain, with nothing faked but AWS: the real extension
// binary, built here; a real bundle, signed with a real key; the real
// store verifying it; the real bridge protocol over a real pipe to a
// real process; and the real pillar compiler merging what comes back.
//
// Every piece has its own test. This one exists because the seams
// between them are where a chain of individually-correct pieces stops
// working, and because this is the path an operator actually takes.

var (
	buildOnce sync.Once
	builtPath string
	buildErr  error
)

// buildExtension compiles the reference extension.
func buildExtension(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "halite-ext-*")
		if err != nil {
			buildErr = err
			return
		}
		name := "aws-secrets"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		builtPath = filepath.Join(dir, name)
		build := exec.Command("go", "build", "-o", builtPath, "../../cmd/halite-ext-aws-secrets")
		build.Stderr = os.Stderr
		buildErr = build.Run()
	})
	if buildErr != nil {
		t.Fatalf("building the extension: %v", buildErr)
	}
	return builtPath
}

// installBundle writes a signed bundle into a cache the way
// `halite-hub extensions sync` would, and returns the cache directory
// and the key a hub must trust to run it.
func installBundle(t *testing.T, exePath string) (string, extension.TrustKey) {
	t.Helper()
	cache := t.TempDir()
	dir := filepath.Join(cache, "aws_secrets_manager", "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	exe := "aws-secrets"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	body, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, exe), body, 0o755); err != nil {
		t.Fatal(err)
	}

	platform := runtime.GOOS + "/" + runtime.GOARCH
	manifest, err := extension.Build(dir, extension.Manifest{
		Name: "aws_secrets_manager", Version: "1.0.0", Kind: "pillar",
		Declares:    []string{"network"},
		Executables: map[string]string{platform: exe},
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

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := extension.MerkleRoot(manifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, extension.SignatureName),
		extension.Sign(private, root), 0o644); err != nil {
		t.Fatal(err)
	}
	return cache, extension.TrustKey{Name: "test", Key: public}
}

// fakeAWS answers GetSecretValue, so the extension has something real
// to sign a request to.
func fakeAWS(t *testing.T, values map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		for id, val := range values {
			if strings.Contains(string(body), `"`+id+`"`) {
				fmt.Fprintf(w, `{"Name":%q,"SecretString":%q}`, id, val)
				return
			}
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"ResourceNotFoundException","message":"no such secret"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// treeLoader is a pillar tree held in memory, keyed `env|sls`.
type treeLoader map[string]string

func (m treeLoader) Source(env, sls string) ([]byte, string, error) {
	src, ok := m[env+"|"+sls]
	if !ok {
		return nil, "", fmt.Errorf("%w: %s", state.ErrNotFound, sls)
	}
	return []byte(src), sls + ".sls", nil
}

func (m treeLoader) Envs() []string { return []string{"base"} }

func (m treeLoader) Templates(env string) template.Loader { return treeTemplates{m, env} }

type treeTemplates struct {
	m   treeLoader
	env string
}

func (t treeTemplates) Load(name string) (string, string, error) {
	if src, ok := t.m[t.env+"|"+name]; ok {
		return src, name, nil
	}
	return "", "", template.ErrNotFound
}

func TestASignedExtensionSuppliesPillarEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	aws := fakeAWS(t, map[string]string{
		"vmop/dev/salt-api": `{"salt_api_pass":"s3cr3t"}`,
	})
	cache, key := installBundle(t, buildExtension(t))

	// The store, with the same verification a hub does: signature
	// required, and only this key trusted.
	store := &extension.Store{
		Dir: cache,
		Options: extension.LoadOptions{
			TrustKeys:        []extension.TrustKey{key},
			RequireSignature: true,
		},
	}
	installed, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	usable, problems := store.Usable(installed)
	for _, p := range problems {
		t.Fatalf("the bundle was refused: %v", p)
	}
	bundle, ok := usable["aws_secrets_manager"]
	if !ok {
		t.Fatalf("the bundle did not load; usable = %v", usable)
	}

	// One directory, computed once. t.TempDir() makes a fresh one on
	// every call, and the runtime asks three times -- so a closure
	// calling it directly creates the directory it then does not use,
	// and the process fails to start with the binary's name on it.
	work := t.TempDir()
	rt := &extension.Runtime{
		WorkDirFor: func(name string) string { return filepath.Join(work, name) },
		// The extension reaches a link-local address for credentials
		// unless the environment carries them, and the test's AWS is
		// neither. Handed in the block below instead.
		PoolSize: 1,
	}
	t.Cleanup(rt.Close)
	if err := rt.Add(bundle); err != nil {
		t.Fatal(err)
	}

	// A static key, in a file, because that is the only inline form the
	// extension takes: a secret in the configuration guarding the other
	// secrets is a poor trade, and a real hub uses its instance role
	// and configures none of this.
	keyFile := filepath.Join(t.TempDir(), "secret-key")
	if err := os.WriteFile(keyFile, []byte("wJalrXUtnFEMI/K7MDENG/bPxRfiCY\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The block an operator writes, which the hub passes through
	// without reading.
	block := fmt.Sprintf(`
ext_pillar:
  - aws_secrets_manager:
      endpoint: %s
      region: us-east-1
      access_key_id: AKIAIOSFODNN7EXAMPLE
      secret_access_key_file: %s
      secrets:
        - name: salt_api_secret_id
          secret_id: vmop/dev/salt-api
`, aws.URL, keyFile)
	doc, _, err := yaml.Parse([]byte(block), yaml.Options{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := doc.(*value.Map).Get("ext_pillar")
	specs, err := ParseList(raw, false)
	if err != nil {
		t.Fatal(err)
	}

	var redacted []string
	sources, err := Sources(specs, rt, func(v string) { redacted = append(redacted, v) })
	if err != nil {
		t.Fatal(err)
	}

	tree := treeLoader{
		"base|top": "base:\n  '*':\n    - common\n",
		// The tree holds a placeholder the source must win over, which
		// also pins the ordering.
		"base|common": "java_home: /usr/lib/jdk17\n" +
			"aws_secrets:\n  salt_api_secret_id:\n    salt_api_pass: placeholder\n",
	}
	c := &pillar.Compiler{
		Loader: tree,
		Config: pillar.Config{
			NodeID: "web1.prod",
			Grains: value.MapOf("id", "web1.prod", "os", "Ubuntu"),
			Ext:    sources,
		},
	}
	out := c.Compile()
	if err := out.Err(); err != nil {
		t.Fatalf("compilation failed: %v", err)
	}

	got, ok := value.Traverse(out.Pillar, "aws_secrets:salt_api_secret_id:salt_api_pass", ":")
	if !ok {
		t.Fatalf("the secret is absent; pillar is %v", out.Pillar.StringKeys())
	}
	if got != "s3cr3t" {
		t.Errorf("the secret is %v", got)
	}
	if java, _ := out.Pillar.Get("java_home"); java != "/usr/lib/jdk17" {
		t.Errorf("the tree's own value is %v", java)
	}
	if len(out.Ext) != 1 || out.Ext[0] != "aws_secrets_manager" {
		t.Errorf("the contributing sources are %v", out.Ext)
	}
	var offered bool
	for _, v := range redacted {
		if v == "s3cr3t" {
			offered = true
		}
	}
	if !offered {
		t.Errorf("the secret never reached the redactor; offered %v", redacted)
	}
}

// A source naming an extension nobody installed is an error that says
// what to do, not a pillar quietly missing its secrets.
func TestAnUninstalledSourceIsRefusedWithInstructions(t *testing.T) {
	rt := &extension.Runtime{}
	_, err := Sources([]Spec{{Name: "vault"}}, rt, nil)
	if err == nil {
		t.Fatal("a source with no extension behind it was accepted")
	}
	for _, want := range []string{"vault", "kind `pillar`", "_ext/", "extensions sync"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}
