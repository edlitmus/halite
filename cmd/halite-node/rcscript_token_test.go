package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The FreeBSD rc script hands halite_node_token to `halite-node enroll`.
//
// A minted token is random base64 and begins with `-` about one time in
// 64. The CLI reads a value that begins with `-` after a space as a flag
// and refuses it, on purpose (DIVERGENCE 5.429), which is why the
// handbook says to write `--token=SECRET`. The rc script wrote
// `--token ${halite_node_token}`, so a node configured that way did not
// enrol and did not start, and chapter 31 told the operator to mint
// another token rather than the script being right.
//
// The test runs the script's own halite_node_enrol under /bin/sh, with
// rc.subr's checkyesno and warn stubbed and a stand-in binary that
// prints its arguments, so what it checks is the argument list the real
// binary would be given.
func TestTheRCScriptPassesATokenThatBeginsWithAHyphen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the rc script is FreeBSD's; this runs it under a POSIX sh, which Windows has not got")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH to run the rc script with")
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "contrib", "rc.d", "halite_node"))
	if err != nil {
		t.Fatal(err)
	}
	fn := rcFunction(t, string(src), "halite_node_enrol")

	dir := t.TempDir()
	bin := filepath.Join(dir, "halite-node")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	const token = "-zO2Bl1eEexampleTokenFromABase64Alphabet_"
	harness := strings.Join([]string{
		`checkyesno() { return 0; }`,
		`warn() { printf 'warn: %s\n' "$*" >&2; }`,
		`halite_node_config=/usr/local/etc/halite/node.yaml`,
		`halite_node_ca_file=""`,
		`halite_node_token="` + token + `"`,
		`halite_node_token_file=""`,
		`halite_node_user=root`,
		`halite_node_binary="` + bin + `"`,
		fn,
		`halite_node_enrol`,
	}, "\n")
	out, err := exec.Command(sh, "-c", harness).CombinedOutput()
	if err != nil {
		t.Fatalf("halite_node_enrol: %v\n%s", err, out)
	}
	args := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	want := "--token=" + token
	for i, a := range args {
		if a == want {
			return
		}
		if a == "--token" && i+1 < len(args) && args[i+1] == token {
			t.Fatalf("the rc script passes the token after a space, so `halite-node enroll` reads %q as a flag and refuses it:\n%s",
				token, strings.Join(args, " "))
		}
	}
	t.Fatalf("the rc script did not pass %q; it passed:\n%s", want, strings.Join(args, " "))
}

// rcFunction returns one shell function's definition from an rc script,
// from its `name()` line to the `}` that closes it in column one, which
// is how every function in contrib/rc.d is written.
func rcFunction(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "\n"+name+"()\n")
	if start < 0 {
		t.Fatalf("no function %s in the rc script", name)
	}
	rest := src[start+1:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatalf("function %s in the rc script has no closing brace in column one", name)
	}
	return rest[:end+2]
}
