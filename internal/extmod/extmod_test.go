package extmod

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/modules"
)

// writeModule drops an executable shell script into dir.
func writeModule(t *testing.T, dir, name, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("external module tests need a POSIX shell")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestModuleReceivesTheRequestAndItsResultIsUsed(t *testing.T) {
	dir := t.TempDir()
	// Echo the request back inside the comment so the test can inspect it.
	writeModule(t, dir, "demo", `
request=$(cat)
printf '{"result": true, "changed": true, "comment": %s, "changes": {"did": "%s"}}\n' \
  "$(printf '%s' "$request" | tr -d '\n' | sed 's/"/\\"/g; s/^/"/; s/$/"/')" "$1"
`)
	fn, ok := Lookup(dir)("demo.configured")
	if !ok {
		t.Fatal("module not found")
	}

	ctx := &modules.Ctx{
		Test:   true,
		Grains: map[string]any{"os": "FreeBSD"},
		Pillar: map[string]any{"port": "8080"},
	}
	res := fn(ctx, "my_state", map[string]any{"name": "thing"})

	if !res.Ok || !res.Changed {
		t.Fatalf("result = %+v", res)
	}
	if res.Changes["did"] != "configured" {
		t.Errorf("the function name was not passed as argv[1]: %v", res.Changes)
	}

	var request Request
	if err := json.Unmarshal([]byte(res.Comment), &request); err != nil {
		t.Fatalf("the module did not receive JSON: %v (%q)", err, res.Comment)
	}
	if request.Function != "configured" || request.ID != "my_state" {
		t.Errorf("request = %+v", request)
	}
	if !request.Test {
		t.Error("test mode was not passed through")
	}
	if request.Args["name"] != "thing" {
		t.Errorf("args = %v", request.Args)
	}
	if request.Grains["os"] != "FreeBSD" || request.Pillar["port"] != "8080" {
		t.Errorf("grains or pillar missing: %+v", request)
	}
}

func TestBuiltinModulesWinOverExternalOnes(t *testing.T) {
	dir := t.TempDir()
	// A module that would shadow a built-in must not be reached.
	writeModule(t, dir, "file", `printf '{"result": true, "comment": "external"}\n'`)

	fn, ok := Lookup(dir)("file.managed")
	if !ok {
		t.Fatal("file.managed did not resolve")
	}
	target := filepath.Join(t.TempDir(), "x")
	res := fn(&modules.Ctx{Test: true}, "s", map[string]any{"name": target, "contents": "hi"})
	if res.Comment == "external" {
		t.Error("an external module shadowed the built-in file.managed")
	}
}

func TestUnknownModulesDoNotResolve(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "demo", `printf '{"result": true}\n'`)
	lookup := Lookup(dir)

	for _, name := range []string{
		"missing.thing",  // no such program
		"demo",           // no function
		"demo.",          // empty function
		".configured",    // empty module
		"../demo.thing",  // path traversal
		"sub/demo.thing", // separator
	} {
		if _, ok := lookup(name); ok {
			t.Errorf("%q resolved, want not found", name)
		}
	}
}

func TestANonExecutableFileIsNotAModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes"), []byte("just a file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(dir)("notes.thing"); ok {
		t.Error("a non-executable file resolved as a module")
	}
}

func TestNonZeroExitIsAFailedStateWithStderr(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "demo", `echo "the thing broke" >&2; exit 3`)

	fn, _ := Lookup(dir)("demo.configured")
	res := fn(&modules.Ctx{}, "s", nil)
	if res.Ok {
		t.Fatal("a non-zero exit must fail the state")
	}
	if !strings.Contains(res.Comment, "the thing broke") {
		t.Errorf("stderr was not reported: %q", res.Comment)
	}
}

func TestSilentSuccessIsAFailure(t *testing.T) {
	// Exiting 0 with no result is a broken module, not a no-op success:
	// reporting it as ok would mean silently skipping a state.
	dir := t.TempDir()
	writeModule(t, dir, "demo", `exit 0`)

	fn, _ := Lookup(dir)("demo.configured")
	res := fn(&modules.Ctx{}, "s", nil)
	if res.Ok {
		t.Fatal("a module that wrote nothing reported success")
	}
	if !strings.Contains(res.Comment, "no result") {
		t.Errorf("comment = %q", res.Comment)
	}
}

func TestNonJSONOutputIsReportedWithWhatItSaid(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "demo", `echo "Configuring things..."; echo "done"`)

	fn, _ := Lookup(dir)("demo.configured")
	res := fn(&modules.Ctx{}, "s", nil)
	if res.Ok {
		t.Fatal("unparseable output reported success")
	}
	if !strings.Contains(res.Comment, "Configuring things") {
		t.Errorf("the comment should show what the module printed: %q", res.Comment)
	}
}

func TestAFailedResultCarriesStderr(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "demo", `
echo "could not reach the api" >&2
printf '{"result": false, "comment": "not configured"}\n'
`)
	fn, _ := Lookup(dir)("demo.configured")
	res := fn(&modules.Ctx{}, "s", nil)
	if res.Ok {
		t.Fatal("result=false must fail")
	}
	if !strings.Contains(res.Comment, "not configured") ||
		!strings.Contains(res.Comment, "could not reach the api") {
		t.Errorf("comment = %q, want both the module's comment and its stderr", res.Comment)
	}
}

func TestASucceedingModuleGetsADefaultComment(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "demo", `printf '{"result": true}\n'`)

	fn, _ := Lookup(dir)("demo.configured")
	res := fn(&modules.Ctx{}, "s", nil)
	if !res.Ok {
		t.Fatalf("res = %+v", res)
	}
	if res.Comment == "" {
		t.Error("a result with no comment should still say something")
	}
}

func TestATimedOutModuleFails(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "demo", `sleep 30`)

	resolver := &Resolver{Dir: dir, Timeout: 200 * time.Millisecond}
	fn, ok := resolver.Lookup("demo.configured")
	if !ok {
		t.Fatal("module not found")
	}
	start := time.Now()
	res := fn(&modules.Ctx{}, "s", nil)
	if res.Ok {
		t.Fatal("a module that never finished reported success")
	}
	if !strings.Contains(res.Comment, "timed out") {
		t.Errorf("comment = %q", res.Comment)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the timeout did not bite: took %s", elapsed)
	}
}

func TestNamesListsExecutableModulesOnly(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "nginx", `printf '{"result": true}\n'`)
	writeModule(t, dir, "postgres", `printf '{"result": true}\n'`)
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	names := (&Resolver{Dir: dir}).Names()
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "nginx") || !strings.Contains(joined, "postgres") {
		t.Errorf("names = %v", names)
	}
	if strings.Contains(joined, "README") {
		t.Errorf("a non-executable file was listed: %v", names)
	}
}

func TestNoDirectoryMeansNoExternalModules(t *testing.T) {
	if _, ok := Lookup("")("demo.thing"); ok {
		t.Error("an unset directory resolved a module")
	}
	if _, ok := Lookup(filepath.Join(t.TempDir(), "absent"))("demo.thing"); ok {
		t.Error("a missing directory resolved a module")
	}
}
