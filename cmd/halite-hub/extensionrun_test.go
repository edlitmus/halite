package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/bridge"
)

// The argument is a path, and `exec` looks a bare name up on PATH.
//
// `extensions run myext` therefore went hunting through /usr/bin for a
// file sitting in the working directory, and reported "executable file
// not found in $PATH" about it. The command resolves the path itself
// now, and checks what it found before starting anything.
func TestABareNameIsAPathAndNotAPathLookup(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "myext")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(dir)
	got, err := resolveExtensionPath("myext")
	if err != nil {
		t.Fatalf("a file in the working directory was not found: %v", err)
	}
	if !strings.HasSuffix(got, "myext") || !filepath.IsAbs(got) {
		t.Errorf("it resolved to %q", got)
	}
}

// A file that is not there says so, naming what was asked for rather
// than the absolute path it became.
func TestAMissingExtensionSaysWhatWasAskedFor(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := resolveExtensionPath("not-here")
	if err == nil {
		t.Fatal("a missing file was accepted")
	}
	if !strings.Contains(err.Error(), "not-here") {
		t.Errorf("the error is %q", err)
	}
}

// A bundle checked out without the execute bit is a common and
// confusing failure, so it is named rather than left to `exec`.
func TestAFileWithoutTheExecuteBitIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("there is no execute bit on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "myext")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveExtensionPath(script)
	if err == nil {
		t.Fatal("a file with no execute bit was accepted")
	}
	if !strings.Contains(err.Error(), "executable") {
		t.Errorf("the error is %q", err)
	}
}

// A directory is not an extension, and naming one is a mistake worth a
// sentence rather than an exec failure.
func TestADirectoryIsRefused(t *testing.T) {
	_, err := resolveExtensionPath(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("the error is %v", err)
	}
}

// The handshake check reports what surfaces far from its cause.
//
// Each of these used to be found somewhere else: a signature the host
// dropped left an extension reporting no functions, and the operator
// reading that was several steps from the parameter type that caused
// it.
func TestTheHandshakeCheckNamesWhatIsWrong(t *testing.T) {
	cases := []struct {
		name string
		info bridge.Info
		want string
	}{
		{
			"no name",
			bridge.Info{Version: "1.0.0", Functions: oneGoodFunction()},
			"did not name itself",
		},
		{
			"no version",
			bridge.Info{Name: "x", Functions: oneGoodFunction()},
			"cannot be pinned",
		},
		{
			"no functions",
			bridge.Info{Name: "x", Version: "1.0.0"},
			"announced no functions",
		},
		{
			"a kind this build does not have",
			bridge.Info{Name: "x", Version: "1.0.0", Kind: "pilar", Functions: oneGoodFunction()},
			"is not an extension kind",
		},
		{
			"a parameter with no type",
			bridge.Info{Name: "x", Version: "1.0.0", Functions: []ext.Signature{{
				Module: "x", Function: "go",
				Params: []ext.Param{{Name: "how_many"}},
			}}},
			"declares no type",
		},
		{
			"a parameter with no name",
			bridge.Info{Name: "x", Version: "1.0.0", Functions: []ext.Signature{{
				Module: "x", Function: "go",
				Params: []ext.Param{{Type: ext.TypeString}},
			}}},
			"parameter with no name",
		},
		{
			"a signature naming nothing",
			bridge.Info{Name: "x", Version: "1.0.0", Functions: []ext.Signature{{}}},
			"declares no module or no function name",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := checkHandshake(c.info)
			if !anyContains(problems, c.want) {
				t.Errorf("it reported %v, and none of it mentions %q", problems, c.want)
			}
		})
	}
}

// A correct handshake produces nothing, or the check is noise.
func TestAGoodHandshakeReportsNothing(t *testing.T) {
	problems := checkHandshake(bridge.Info{
		Name: "aws_secrets_manager", Version: "1.0.0", Kind: ext.KindPillar,
		Functions: oneGoodFunction(),
	})
	if len(problems) != 0 {
		t.Errorf("it reported %v", problems)
	}
}

// The type message says what a type is, because the mistake it catches
// is sending the number an enum serialises to.
func TestTheTypeMessageSaysATypeIsAName(t *testing.T) {
	problems := checkHandshake(bridge.Info{
		Name: "x", Version: "1.0.0", Functions: []ext.Signature{{
			Module: "x", Function: "go", Params: []ext.Param{{Name: "n"}},
		}},
	})
	if !anyContains(problems, "not a number") {
		t.Errorf("it reported %v", problems)
	}
}

func oneGoodFunction() []ext.Signature {
	return []ext.Signature{{
		Module: "x", Function: "go",
		Params: []ext.Param{{Name: "message", Type: ext.TypeString}},
	}}
}

func anyContains(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// `extensions run --timeout` took anything it could not read, and
// anything not positive, as sixty seconds without a word; `extensions
// verify` refuses the same values. And --kwargs and --args were decoded
// after the extension had been started. Each is a usage error now, before
// the extension is started: the script below leaves a mark when it is
// run, and a refused command line must leave none. DIVERGENCE 5.609.
func TestExtensionsRunReadsItsFlagsBeforeStarting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in extension is a shell script")
	}
	dir := t.TempDir()
	mark := filepath.Join(dir, "started")
	script := filepath.Join(dir, "myext")
	body := "#!/bin/sh\ntouch '" + mark + "'\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		flags []string
		says  string
	}{
		{"a timeout with no unit", []string{"--timeout", "5"}, "--timeout"},
		{"a timeout that is not a duration", []string{"--timeout", "soon"}, "--timeout"},
		{"a negative timeout", []string{"--timeout=-1s"}, "--timeout"},
		{"a timeout of zero", []string{"--timeout", "0s"}, "--timeout"},
		{"kwargs that are not JSON", []string{"--kwargs", "{nope"}, "--kwargs"},
		{"args that are not JSON", []string{"--args", "[nope"}, "--args"},
	} {
		argv := append([]string{"extensions", "run", script, "ping"}, tc.flags...)
		res := run(t, argv...)
		if res.code != 64 {
			t.Errorf("%s: exited %d, want 64: %s", tc.name, res.code, res.stderr)
		}
		if !strings.Contains(res.stderr, tc.says) {
			t.Errorf("%s: the message does not say %q: %s", tc.name, tc.says, res.stderr)
		}
		if _, err := os.Stat(mark); err == nil {
			t.Errorf("%s: the extension was started before the command line was read", tc.name)
			_ = os.Remove(mark)
		}
	}

	// The control: a readable timeout starts the extension, which is
	// how this test knows the mark means what it says.
	res := run(t, "extensions", "run", script, "ping", "--timeout", "5s")
	if res.code == 64 {
		t.Fatalf("--timeout 5s was refused: %s", res.stderr)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Fatalf("a well-formed command line did not start the extension (exit %d: %s); "+
			"the mark is not a test of anything", res.code, res.stderr)
	}
}
