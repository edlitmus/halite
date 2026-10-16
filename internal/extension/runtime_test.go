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

	"github.com/edlitmus/halite/ext"
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
		extPath = filepath.Join(dir, exeName("echoext"))
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
	return installRealAs(t, cache, "echo", "1.0.0")
}

// installRealAs signs the same binary -- which says it is echo 1.0.0 --
// under whatever name and version the caller chooses, which is what an
// operator does with `extensions sign --name --ext-version`.
func installRealAs(t *testing.T, cache, name, version string) TrustKey {
	t.Helper()
	dir := filepath.Join(cache, name, version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(echoBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, exeName("echoext")), binary, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := Build(dir, Manifest{
		Name: name, Version: version, Kind: "module",
		Description: "returns what it was given",
		Executables: map[string]string{Platform(runtime.GOOS, runtime.GOARCH): exeName("echoext")},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, manifest)

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := manifest.SigningRoot()
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
	sig, err := parseSignature(ext.Signature{
		Module: "echo", Function: "say", Doc: "d",
		Params: []ext.Param{{Name: "message", Type: ext.TypeString, Required: true}},
	})
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

	// And "signed" is checked rather than asserted: the same cached
	// bundle with `network` written into its manifest afterwards is
	// refused by the store, so it never reaches Add. Before DIVERGENCE
	// 5.333 this comment's first line was false, and this half of the
	// test would have loaded the rewritten bundle as signed by "release"
	// with the network granted.
	path := filepath.Join(cache, "echo", "1.0.0", ManifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	m.Declares = []string{"network"}
	rewritten, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, rewritten, 0o644); err != nil {
		t.Fatal(err)
	}
	again, _ := store.Load()
	usable, problems := store.Usable(again)
	if b := usable["echo"]; b != nil {
		t.Fatalf("a bundle whose manifest gained `network` after signing is usable, signed by %q, declaring %v",
			b.SignedBy, b.Manifest.Declares)
	}
	if len(problems) == 0 {
		t.Error("the rewritten bundle was dropped without a problem being reported")
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

// The working directory has to exist before the process starts.
//
// Without this the failure is `fork/exec <executable>: no such file or
// directory`, which names the executable — a file that is present and
// correct — and sends whoever reads it entirely the wrong way. Found by
// running a node against a signed bundle in the lab.
func TestTheWorkingDirectoryIsCreated(t *testing.T) {
	cache := t.TempDir()
	key := installReal(t, cache)
	store := &Store{Dir: cache, Options: LoadOptions{
		TrustKeys: []TrustKey{key}, RequireSignature: true,
	}}
	installed, _ := store.Load()
	usable, _ := store.Usable(installed)

	// A directory that does not exist and is several levels deep.
	work := filepath.Join(t.TempDir(), "ext", "echo", "work")
	rt := &Runtime{
		WorkDirFor: func(string) string { return work },
		Timeout:    20 * time.Second,
	}
	defer rt.Close()
	if err := rt.Add(usable["echo"]); err != nil {
		t.Fatal(err)
	}
	loaded, _ := rt.Get("echo")

	if _, err := loaded.Call(context.Background(), "say", nil,
		map[string]any{"message": "hi"}, nil); err != nil {
		t.Fatalf("the extension could not start: %v", err)
	}
	if info, err := os.Stat(work); err != nil || !info.IsDir() {
		t.Errorf("the working directory was not created: %v", err)
	}
}

// An executable is only run as the extension it says it is.
//
// The echo binary's handshake says echo 1.0.0. Signed as 1.1.0, the pin
// and `sys.list_extensions` would both have said 1.1.0 about code that
// says otherwise, and signed under another name it would have been
// keyed, configured and pinned as something it is not. Both loaded
// before DIVERGENCE 5.325. The bundle verifies in each case -- the
// signature is honest about the bytes -- which is why this is checked
// at the handshake and not at load.
func TestAnExecutableIsRunOnlyAsTheExtensionItSaysItIs(t *testing.T) {
	cases := []struct{ name, version, wantSigned string }{
		{"echo", "1.1.0", "echo 1.1.0"},
		{"other", "1.0.0", "other 1.0.0"},
	}
	for _, c := range cases {
		t.Run(c.name+"-"+c.version, func(t *testing.T) {
			cache := t.TempDir()
			key := installRealAs(t, cache, c.name, c.version)
			store := &Store{Dir: cache, Options: LoadOptions{
				TrustKeys: []TrustKey{key}, RequireSignature: true,
			}}
			installed, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			usable, problems := store.Usable(installed)
			if len(problems) != 0 {
				t.Fatalf("the bundle should verify -- the signature is over honest bytes: %v", problems)
			}
			work := t.TempDir()
			rt := &Runtime{WorkDirFor: func(string) string { return work }, Timeout: 20 * time.Second, PoolSize: 1}
			defer rt.Close()
			if err := rt.Add(usable[c.name]); err != nil {
				t.Fatal(err)
			}
			loaded, _ := rt.Get(c.name)

			err = loaded.Start(context.Background())
			if err == nil {
				t.Fatalf("the echo 1.0.0 binary signed as %s was started", c.wantSigned)
			}
			for _, want := range []string{"signed as " + c.wantSigned, "says it is echo 1.0.0"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q: %v", want, err)
				}
			}
			if _, err := loaded.Call(context.Background(), "say", nil,
				map[string]any{"message": "x"}, nil); err == nil {
				t.Error("a call ran after the start was refused")
			}
		})
	}
}
