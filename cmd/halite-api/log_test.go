package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The api's logger is built from SPEC 26.1's settings by the names those
// settings have, the same as the hub's and the node's.
//
// It was not. The api read `log_fmt` -- a key declared nowhere -- and
// handed the logger no file, so an api.yaml with `log_format: console`
// and a `log_file` logged JSON to stderr and created nothing. Neither
// symptom is loud: the loader warns about an *unknown* key in the file,
// not about a declared one the program never asks for, and a JSON line on
// a terminal looks like a choice rather than a refusal.
//
// This drives the real binary rather than the function that builds the
// logger, because the defect was in which keys the binary consulted, and
// a test of a helper handed the right values would have passed. The line
// it watches is the inert-setting warning `setup` writes, which every
// subcommand that reads the configuration emits before doing anything
// else, so the test needs no hub and no listener.
func TestTheAPIHonoursLogFormatAndLogFile(t *testing.T) {
	root := t.TempDir()
	logFile := filepath.Join(root, "api.log")
	conf := "state_dir: " + filepath.Join(root, "state") + "\n" +
		"log_format: console\n" +
		"log_file: " + logFile + "\n" +
		// Inert, so setup warns about it: a line the test can look at.
		// Not `log_level_file`, which reads as the obvious choice and is
		// in UnreadKeys rather than InertKeys, so nothing warns about it.
		"socket_dir: " + filepath.Join(root, "sockets") + "\n"
	if err := os.WriteFile(filepath.Join(root, "api.yaml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}

	_, errb, code := run(t, "token", "list", "--root", root)
	if code != 0 {
		t.Fatalf("token list = %d: %s", code, errb)
	}

	var warning string
	for _, line := range strings.Split(errb, "\n") {
		if strings.Contains(line, "this setting is accepted and does nothing") {
			warning = line
		}
	}
	if warning == "" {
		t.Fatalf("no inert-setting warning on stderr, so nothing was checked:\n%s", errb)
	}
	if json.Valid([]byte(warning)) {
		t.Errorf("log_format: console logged JSON:\n%s", warning)
	}
	if !strings.HasPrefix(warning, "warn: ") {
		t.Errorf("not a console line: %q", warning)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("log_file was not written: %v", err)
	}
	if !strings.Contains(string(data), warning) {
		t.Errorf("the log file does not hold the line stderr got.\nfile: %q\nstderr line: %q",
			data, warning)
	}

	// And the command line still wins over the file, through the real
	// argument parser rather than a struct a test filled in.
	_, errb, code = run(t, "token", "list", "--root", root, "--log-fmt", "json")
	if code != 0 {
		t.Fatalf("token list --log-fmt json = %d: %s", code, errb)
	}
	first := strings.SplitN(errb, "\n", 2)[0]
	if !json.Valid([]byte(first)) || !strings.Contains(first, `"component":"api"`) {
		t.Errorf("--log-fmt json over log_format: console gave %q", first)
	}
}

// A format that is not one is refused, as the hub and the node refuse
// it. The api used to read anything other than exactly `json` as
// console, so `log_format: JSON` -- or a typo -- quietly changed the
// format an aggregator was parsing.
func TestTheAPIRefusesALogFormatThatIsNotOne(t *testing.T) {
	root := t.TempDir()
	conf := "state_dir: " + filepath.Join(root, "state") + "\nlog_format: yaml\n"
	if err := os.WriteFile(filepath.Join(root, "api.yaml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	_, errb, code := run(t, "token", "list", "--root", root)
	if code == 0 || !strings.Contains(errb, `log_format "yaml" is not a format`) {
		t.Errorf("token list with log_format: yaml = %d: %s", code, errb)
	}
}
