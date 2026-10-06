package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// A mistyped subcommand is refused before the hub is opened.
//
// `keys`, `keys token` and `jobs` each read their subcommand only after
// opening what they would act on: openHub, which loads the configuration,
// opens the log file and the key store; openJobs, which creates the job
// cache directory. So `keys lsit` on a machine with no enrollment CA said
// there was no CA, with 1 -- an answer about the wrong thing, with the
// exit for a command that failed -- and on a hub it created the log file
// and the key store's directory before saying "unknown subcommand". Run
// as root, as these commands usually are, that is a root-owned file in a
// directory the service account has to write, which is how a hub stops
// starting. `jobs lsit` created <state_dir>/jobs the same way.
//
// So each case points state_dir and log_file into a directory of its own,
// with no CA, and requires 64, the subcommand named in the refusal, and
// nothing created.
func TestAMistypedSubcommandIsRefusedBeforeTheHubIsOpened(t *testing.T) {
	for _, tc := range [][]string{
		{"keys", "lsit"},
		{"keys", "token", "lsit"},
		{"jobs", "lsit"},
	} {
		root := t.TempDir()
		state := filepath.Join(root, "state")
		logFile := filepath.Join(root, "hub.log")
		conf := "state_dir: " + state + "\nlog_file: " + logFile + "\n" +
			"pki_dir: " + filepath.Join(root, "pki") + "\n"
		if err := os.WriteFile(filepath.Join(root, "hub.yaml"), []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
		name := strings.Join(tc, " ")
		res := run(t, append(tc, "--root", root)...)
		if res.code != cli.ExitUsage {
			t.Errorf("%s: exited %d, want %d: %s", name, res.code, cli.ExitUsage, res.stderr)
		}
		if !strings.Contains(res.stderr, `"lsit"`) {
			t.Errorf("%s: the refusal does not name the subcommand: %s", name, res.stderr)
		}
		for _, p := range []string{state, logFile, filepath.Join(root, "pki")} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("%s: created %s before refusing the subcommand", name, p)
			}
		}
	}
}

// `extensions` opened the hub's configuration and log file before
// reading its subcommand, as keys and jobs did; and `policy test` short
// of its operands exited 1. DIVERGENCE 5.221.
func TestExtensionsAndPolicyRefuseBeforeTouchingAnything(t *testing.T) {
	root := t.TempDir()
	logFile := filepath.Join(root, "hub.log")
	if err := os.WriteFile(filepath.Join(root, "hub.yaml"),
		[]byte("log_file: "+logFile+"\nstate_dir: "+filepath.Join(root, "state")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := run(t, "extensions", "lsit", "--root", root)
	if res.code != cli.ExitUsage || !strings.Contains(res.stderr, `"lsit"`) {
		t.Errorf("extensions lsit: exited %d: %s", res.code, res.stderr)
	}
	if _, err := os.Stat(logFile); err == nil {
		t.Error("extensions lsit created the log file before refusing the subcommand")
	}

	res = run(t, "policy", "test", "cert:CN=ed", "--root", root)
	if res.code != cli.ExitUsage || !strings.Contains(res.stderr, "takes a principal, a target, and a function") {
		t.Errorf("policy test with one operand: exited %d: %s", res.code, res.stderr)
	}
}
