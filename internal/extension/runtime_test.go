package extension

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// A signed bundle holding a real extension binary, loaded through the
// store and called through the bridge.
//
// The whole path in one test, because the parts are only worth anything
// together: a bundle that verifies but cannot be run, or an extension
// that runs but was never verified, is each the failure the other half
// exists to prevent.
var (
	extOnce sync.Once
	extPath string
	extErr  error
)

func echoBinary(t *testing.T) string {
	t.Helper()
	extOnce.Do(func() {
		dir, err := os.MkdirTemp("", "halite-ext-*")
		if err != nil {
			extErr = err
			return
		}
		extPath = filepath.Join(dir, "echoext")
		build := exec.Command("go", "build", "-o", extPath, "../bridge/testdata/echoext")
		build.Stderr = os.Stderr
		extErr = build.Run()
	})
	if extErr != nil {
		t.Fatalf("building the extension: %v", extErr)
	}
	return extPath
}

// installReal puts a signed bundle carrying the real binary in a cache.
func installReal(t *testing.T, cache string) TrustKey {
	t.Helper()
	dir := filepath.Join(cache, "echo", "1.0.0")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(echoBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "echoext"), binary, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := Build(dir, Manifest{
		Name: "echo", Version: "1.0.0", Kind: "module",
		Description: "returns what it was given",
		Executables: map[string]string{Platform(runtime.GOOS, runtime.GOARCH): "echoext"},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, manifest)

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := MerkleRoot(manifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SignatureName), Sign(private, root), 0o644); err != nil {
		t.Fatal(err)
	}
	return TrustKey{Name: "release", Key: public}
}

func TestASignedExtensionIsLoadedAndCalled(t *testing.T) {
	cache := t.TempDir()
	key := installReal(t, cache)

	store := &Store{Dir: cache, Options: LoadOptions{
		TrustKeys: []TrustKey{key}, RequireSignature: true,
	}}
	installed, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	usable, problems := store.Usable(installed)
	if len(problems) != 0 {
		t.Fatalf("problems loading: %v", problems)
	}

	work := t.TempDir()
	rt := &Runtime{
		WorkDirFor: func(name string) string { return work },
		Timeout:    20 * time.Second,
		PoolSize:   2,
		Log:        func(name, level, message string) { t.Logf("%s %s: %s", name, level, message) },
	}
	defer rt.Close()
	if err := rt.Add(usable["echo"]); err != nil {
		t.Fatal(err)
	}

	loaded, ok := rt.Get("echo")
	if !ok {
		t.Fatal("echo is not loaded")
	}
	value, err := loaded.Call(context.Background(), "say", nil,
		map[string]any{"message": "through the bundle"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(value, &got); err != nil {
		t.Fatal(err)
	}
	if got["said"] != "through the bundle" {
		t.Errorf("it said %v", got["said"])
	}

	// And what an operator sees.
	described := loaded.Describe()
	if described["signed_by"] != "release" {
		t.Errorf("it is described as signed by %v", described["signed_by"])
	}
	if described["unsigned"] != false {
		t.Error("a signed extension is described as unsigned")
	}
	sandbox, _ := described["sandbox"].([]string)
	if len(sandbox) == 0 || !strings.Contains(strings.Join(sandbox, " "), "process boundary") {
		t.Errorf("the sandbox is described as %v", sandbox)
	}
	// The root is there for an operator to copy into a pin.
	if root, _ := described["root"].(string); len(root) != 64 {
		t.Errorf("the root is %q", root)
	}
	functions, _ := described["functions"].([]string)
	if len(functions) != 1 || functions[0] != "echo.say" {
		t.Errorf("its functions are %v", functions)
	}
}

// An extension is arbitrary code by construction, so a wildcard in the
// RBAC policy never grants one: SPEC 23.5's rule applied to the thing it
// most obviously covers.
func TestAnExtensionFunctionIsMarkedAsArbitraryCode(t *testing.T) {
	sig, err := parseSignature(json.RawMessage(
		`{"module":"echo","function":"say","doc":"d","params":[{"name":"message","type":"string","required":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !sig.ArbitraryCode {
		t.Error("an extension function is not marked as arbitrary code")
	}
	if sig.Name() != "echo.say" {
		t.Errorf("the name is %q", sig.Name())
	}
	if len(sig.Params) != 1 || sig.Params[0].Name != "message" {
		t.Errorf("the params are %+v", sig.Params)
	}
}

// The manifest's declarations are signed; the handshake's are not. The
// sandbox is built from the signed ones.
func TestTheSandboxComesFromTheSignedManifest(t *testing.T) {
	cache := t.TempDir()
	key := installReal(t, cache)
	store := &Store{Dir: cache, Options: LoadOptions{
		TrustKeys: []TrustKey{key}, RequireSignature: true,
	}}
	installed, _ := store.Load()
	usable, _ := store.Usable(installed)

	rt := &Runtime{WorkDirFor: func(string) string { return t.TempDir() }}
	defer rt.Close()
	if err := rt.Add(usable["echo"]); err != nil {
		t.Fatal(err)
	}
	loaded, _ := rt.Get("echo")
	// The manifest declared nothing, so the network is not granted
	// however the running process introduces itself.
	if loaded.Sandbox.Network {
		t.Error("an extension that declared nothing was granted the network")
	}
}

// An extension whose manifest asks for something this build cannot
// enforce must not be loaded as though it had been granted.
func TestAnUndeclarableNeedIsRefusedAtLoad(t *testing.T) {
	rt := &Runtime{}
	bundle := &Bundle{Manifest: &Manifest{
		Name: "odd", Version: "1", Kind: "module",
		Declares: []string{"kernel"},
	}}
	if err := rt.Add(bundle); err == nil {
		t.Fatal("an extension declaring something unenforceable was loaded")
	}
}
