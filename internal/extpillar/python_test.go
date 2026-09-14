package extpillar

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/bridge"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/value"
)

// The Python example, driven through the real host.
//
// `contrib/extensions/python/example_pillar.py` exists to back a claim
// -- that an extension need not be written in Go, and that the wire
// format is specified well enough to implement from the documentation.
// A claim like that is worth nothing unchecked: the example would drift
// from the protocol the first time the protocol moved, and nobody would
// find out until somebody outside this project tried to write one.
//
// So the real host starts it, handshakes with it, and asks it for
// pillar. Skipped where there is no python3, which is a statement about
// the machine rather than about the example.

func pythonExample(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		// The bundle carries a script and the host starts it directly,
		// so it runs by its shebang. Windows has no such mechanism, and
		// a bundle carrying a script for it would name the interpreter
		// instead. Said here rather than worked around: it is a real
		// limit of packaging a script, and docs/extensions.md says so.
		t.Skip("a shebang is not how Windows starts a program")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on this machine")
	}
	path, err := filepath.Abs("../../contrib/extensions/python/example_pillar.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the example is missing: %v", err)
	}
	return path
}

// startPython runs the example as an extension process.
//
// By its path, the way the host starts any bundled executable: the
// shebang is what makes a script one. Nothing here knows it is Python.
func startPython(t *testing.T, path string) *bridge.Pool {
	t.Helper()
	pool := bridge.NewPool(bridge.Options{Path: path, Kind: "pillar"}, 1)
	t.Cleanup(pool.Close)
	return pool
}

func TestThePythonExampleSpeaksTheProtocol(t *testing.T) {
	path := pythonExample(t)
	pool := startPython(t, path)

	src := &Bridged{
		SourceName: "example_pillar",
		Ext:        pool,
		Config: value.MapOf(
			"data", value.MapOf("timezone", "UTC"),
			"per_os", value.MapOf("Ubuntu", value.MapOf("package_manager", "apt")),
			"per_node", value.MapOf("web1.prod", value.MapOf("role", "web")),
		),
	}

	out, err := src.Pillar(context.Background(), pillar.ExtRequest{
		NodeID: "web1.prod",
		Env:    "base",
		Grains: value.MapOf("os", "Ubuntu"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"timezone":        "UTC", // everybody
		"package_manager": "apt", // by the os grain
		"role":            "web", // by node id
	} {
		got, ok := out.Get(key)
		if !ok {
			t.Errorf("%s is absent; got %v", key, out.StringKeys())
			continue
		}
		if got != want {
			t.Errorf("%s is %v, want %s", key, got, want)
		}
	}
}

// A node the example has nothing for gets nothing, and that is not an
// error: a source configured for other machines is a normal thing.
func TestThePythonExampleContributesNothingForAnUnknownNode(t *testing.T) {
	path := pythonExample(t)
	pool := startPython(t, path)

	src := &Bridged{SourceName: "example_pillar", Ext: pool}
	out, err := src.Pillar(context.Background(), pillar.ExtRequest{NodeID: "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && out.Len() != 0 {
		t.Errorf("it produced %v", out)
	}
}

// It refuses a setting it does not know, rather than accepting a
// misspelling and quietly contributing nothing -- and the refusal
// reaches the host as a failed call rather than as a hang.
func TestThePythonExampleRefusesAnUnknownSetting(t *testing.T) {
	path := pythonExample(t)
	pool := startPython(t, path)

	src := &Bridged{
		SourceName: "example_pillar",
		Ext:        pool,
		Config:     value.MapOf("dta", value.MapOf("timezone", "UTC")),
	}
	_, err := src.Pillar(context.Background(), pillar.ExtRequest{NodeID: "web1.prod"})
	if err == nil {
		t.Fatal("a misspelt setting was accepted")
	}
	if !strings.Contains(err.Error(), "dta") {
		t.Errorf("the error does not name the setting: %v", err)
	}
}

// The handshake carries what the host needs to use it as a pillar
// source: the kind, and a function named ext_pillar whose parameters
// declare types by name.
func TestThePythonExampleHandshakes(t *testing.T) {
	path := pythonExample(t)
	pool := startPython(t, path)

	if err := pool.Warm(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, ok := pool.Info()
	if !ok {
		t.Fatal("the handshake produced nothing")
	}
	if info.Name != "example_pillar" {
		t.Errorf("it named itself %q", info.Name)
	}
	if len(info.Functions) != 1 || info.Functions[0].Function != "ext_pillar" {
		t.Fatalf("it announced %+v", info.Functions)
	}
	for _, p := range info.Functions[0].Params {
		if p.Type == "" {
			t.Errorf("the %s parameter declares no type", p.Name)
		}
	}
	if len(info.Declares) != 0 {
		t.Errorf("it declares %v, and it needs nothing", info.Declares)
	}
}
