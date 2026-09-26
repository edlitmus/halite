package builtin

import (
	"archive/zip"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The seventeen functions that `unconformed` listed as "reachable
// in-process and not yet written".
//
// They were the group in that table with no excuse worth keeping: nothing
// about them needs a machine somebody minds breaking. Six of them did need
// something, though — the harness itself. Its phases assume a state with
// work to do, and `test.nop`, `cmd.wait` and their kind exist to do
// nothing, so `states.Conformance.Unchanging` was added to hold them to the
// contract they actually have. DIVERGENCE 5.157.
//
// What each of these needs from the machine differs, which is why they are
// here rather than in the list in builtin_test.go: some need commands that
// really run, some need a module dispatcher, one needs git. The accounting
// subtest reads both lists, so neither can drift from what ran.

// conformanceCase is a Conformance together with what it needs.
type conformanceCase struct {
	states.Conformance
	// Context overrides the shared factory. A case that leaves it nil gets
	// the shared one.
	Context func(test bool) *exec.Context
	// Label distinguishes several cases for one function, which
	// `test.configurable_test_state` needs because its whole purpose is to
	// behave differently depending on its arguments.
	Label string
	// Requires is a program that must be on PATH for the case to mean
	// anything. A case that names one and does not find it skips, saying
	// so, which is this package's existing convention for the real git
	// binary -- and it is a weakness: the accounting counts a skipped case
	// as covered. Only `git.latest` uses it.
	Requires string
}

// reachableConformanceCases builds the cases, given the shared context
// factory to derive from.
func reachableConformanceCases(t *testing.T, r *Registries, base func(test bool) *exec.Context) []conformanceCase {
	t.Helper()
	dir := t.TempDir()

	// A context whose commands actually run. `cmd.run` against the shared
	// RecordingRunner would return success without running anything, and a
	// probe for a file the command creates would then pass by never having
	// been given the chance to fail.
	running := func(test bool) *exec.Context {
		c := base(test)
		c.Runner = &exec.OSRunner{}
		return c
	}
	// And one that can call other modules, which `module.run` is.
	dispatching := func(test bool) *exec.Context {
		c := base(test)
		c.Dispatch = dispatcherFor(r)
		return c
	}

	// absentPath is the probe for "this path does not exist", which is how
	// a case proves a command did not run.
	absentPath := func(path string) func() (string, error) {
		return func() (string, error) {
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				return "absent", nil
			} else if err != nil {
				return "", err
			}
			return "present", nil
		}
	}

	var cases []conformanceCase

	// ---- the `test` module's fakes ----
	//
	// Four of them report a fixed answer and touch nothing, which is the
	// whole of their contract. What can go wrong is that the answer
	// depends on the mode, and that is what Unchanging compares.
	for _, tc := range []struct {
		name string
		args *value.Map
		why  string
	}{
		{"test.nop", value.MapOf("name", "anchor"),
			"this state exists to do nothing and succeed, as a requisite anchor"},
		{"test.succeed_without_changes", value.MapOf("name", "anchor"),
			"this state exists to succeed and report no changes"},
		{"test.show_notification", value.MapOf("name", "notice", "text", "The deployment finished."),
			"this state puts a message in the output and changes nothing"},
		{"test.fail_without_changes", value.MapOf("name", "onfail anchor"),
			"this state exists to fail and report no changes, for exercising onfail"},
	} {
		cases = append(cases, conformanceCase{Conformance: states.Conformance{
			Name:             tc.name,
			Args:             tc.args,
			Unchanging:       true,
			UnchangingReason: tc.why,
		}})
	}

	// `test.configurable_test_state` is three cases, because it is one
	// function with three behaviours and a case for one of them would
	// leave the others unchecked. The configured result is deliberately
	// covered in both directions: a state told to fail must fail the same
	// way in test mode, which is the same contract as any other.
	cases = append(cases,
		conformanceCase{
			Label: "test.configurable_test_state, changes",
			Conformance: states.Conformance{
				Name: "test.configurable_test_state",
				Args: value.MapOf("name", "synthetic", "changes", true, "result", true),
				// The same reason `test.succeed_with_changes` has: the
				// synthetic change is reported on every run by design,
				// which is what makes it useful for exercising watch.
				SkipIdempotence:       true,
				SkipIdempotenceReason: "configured to report a change, which it does on every run by design",
			},
		},
		conformanceCase{
			Label: "test.configurable_test_state, no changes",
			Conformance: states.Conformance{
				Name:             "test.configurable_test_state",
				Args:             value.MapOf("name", "synthetic", "changes", false, "result", true),
				Unchanging:       true,
				UnchangingReason: "configured to report no changes, so it changes nothing in either mode",
			},
		},
		conformanceCase{
			Label: "test.configurable_test_state, configured to fail",
			Conformance: states.Conformance{
				Name:             "test.configurable_test_state",
				Args:             value.MapOf("name", "synthetic", "changes", false, "result", false),
				Unchanging:       true,
				UnchangingReason: "configured to fail without changes, which it must do identically in test mode",
			},
		},
	)

	// ---- cmd and module ----
	//
	// `cmd.run` and `cmd.script` cannot be idempotent on their own, and
	// that is not a defect: `creates`, `unless` and `onlyif` are lowstate
	// options, evaluated by `internal/state` before the function is
	// called, so the function reached through the registry always runs.
	// The case says so rather than passing over it, because a reader
	// finding `SkipIdempotence` here should learn where idempotence
	// actually comes from.
	ranBy := "idempotence comes from creates, unless and onlyif, which the engine evaluates " +
		"before this function is called; the function itself always runs"

	script := writeMarkerScript(t, dir)
	runMarker := filepath.Join(dir, "cmd-run-marker")
	cases = append(cases, conformanceCase{
		Context: running,
		Conformance: states.Conformance{
			Name: "cmd.run",
			// shell false, so `name` is the program and `args` its
			// arguments -- one form on every platform, where a shell line
			// would be two.
			Args:                  value.MapOf("name", script, "args", []any{runMarker}, "shell", false),
			Probe:                 absentPath(runMarker),
			Setup:                 func() error { return os.RemoveAll(runMarker) },
			SkipIdempotence:       true,
			SkipIdempotenceReason: ranBy,
		},
	})

	scriptMarker := filepath.Join(dir, "cmd-script-marker")
	cases = append(cases, conformanceCase{
		Context: running,
		Conformance: states.Conformance{
			// The source is the state's `name`, which is how a tree spells
			// it and what cmdScriptState reads.
			Name:                  "cmd.script",
			Args:                  value.MapOf("name", script, "args", []any{scriptMarker}),
			Probe:                 absentPath(scriptMarker),
			Setup:                 func() error { return os.RemoveAll(scriptMarker) },
			SkipIdempotence:       true,
			SkipIdempotenceReason: ranBy,
		},
	})

	// `cmd.wait` runs its command only when a watch requisite fires, so on
	// its own it must do nothing at all -- and the probe is what proves
	// that, rather than the state's own word for it. A `cmd.wait` that ran
	// the command anyway would run it under `--test` too, which is an
	// operator asking what a highstate would do and having it done.
	waitMarker := filepath.Join(dir, "cmd-wait-marker")
	cases = append(cases, conformanceCase{
		Context: running,
		Conformance: states.Conformance{
			Name:             "cmd.wait",
			Args:             value.MapOf("name", script, "args", []any{waitMarker}, "shell", false),
			Probe:            absentPath(waitMarker),
			Setup:            func() error { return os.RemoveAll(waitMarker) },
			Unchanging:       true,
			UnchangingReason: "it runs its command only when a watch requisite fires, and none does here",
		},
	})

	// `module.run` calls an execution module function. The one it calls
	// creates a directory, so the probe can tell whether the call
	// happened: a module.run that dispatched in test mode is the same
	// defect as any other state acting under `--test`.
	moduleDir := filepath.Join(dir, "module-run-dir")
	cases = append(cases, conformanceCase{
		Context: dispatching,
		Conformance: states.Conformance{
			Name:  "module.run",
			Args:  value.MapOf("name", "file.mkdir", "kwargs", value.MapOf("path", moduleDir)),
			Probe: absentPath(moduleDir),
			Setup: func() error { return os.RemoveAll(moduleDir) },
			// Calling the function is the change this state reports, every
			// time. Whether the called function is idempotent is the
			// called function's own conformance case.
			SkipIdempotence:       true,
			SkipIdempotenceReason: "the call is the change it reports, so it reports one on every run",
		},
	})

	waitDir := filepath.Join(dir, "module-wait-dir")
	cases = append(cases, conformanceCase{
		Context: dispatching,
		Conformance: states.Conformance{
			Name:             "module.wait",
			Args:             value.MapOf("name", "file.mkdir", "kwargs", value.MapOf("path", waitDir)),
			Probe:            absentPath(waitDir),
			Setup:            func() error { return os.RemoveAll(waitDir) },
			Unchanging:       true,
			UnchangingReason: "it calls the function only when a watch requisite fires, and none does here",
		},
	})

	// ---- archive.extracted ----
	//
	// A zip rather than a tar because archive/zip and archive/tar are both
	// in the standard library but a zip needs no external tool to inspect
	// if this ever fails on a machine somebody is holding.
	archive := filepath.Join(dir, "payload.zip")
	writeTestZip(t, archive, map[string]string{
		"one.conf":        "one\n",
		"nested/two.conf": "two\n",
	})
	extractTo := filepath.Join(dir, "extracted")
	cases = append(cases, conformanceCase{
		Conformance: states.Conformance{
			Name: "archive.extracted",
			Args: value.MapOf("name", extractTo, "source", archive),
			// The whole tree, so an extraction that loses the nested entry
			// fails rather than passing on the top level -- the same trap
			// `file.recurse` had.
			Probe: probeExtractedTree(extractTo),
			Setup: func() error { return os.RemoveAll(extractTo) },
		},
	})

	// ---- x509.certificate_managed ----
	//
	// Self-signed, so nothing outside this directory is involved. The key
	// is made once and kept: Setup removes the certificate only, because
	// removing the key as well would make every run issue a certificate
	// for a different subject key and `days_remaining` could never be the
	// thing under test.
	key := filepath.Join(dir, "subject.key")
	if res, err := r.States.Call(base(false), "x509.private_key_managed",
		value.MapOf("name", key, "algo", "ec", "keysize", int64(256))); err != nil || res.Failed() {
		t.Fatalf("making the subject key: %v %+v", err, res)
	}
	cert := filepath.Join(dir, "subject.crt")
	cases = append(cases, conformanceCase{
		Conformance: states.Conformance{
			Name: "x509.certificate_managed",
			Args: value.MapOf("name", cert, "private_key", key, "CN", "conformance.example.com",
				"days_valid", int64(365), "days_remaining", int64(30)),
			Probe: probeCertificate(cert),
			Setup: func() error { return os.RemoveAll(cert) },
		},
	})

	// ---- git.latest ----
	//
	// A bare repository in this directory is a remote, so no network is
	// involved. It needs the real git binary, which is the one case here
	// that can skip.
	remote := filepath.Join(dir, "remote.git")
	target := filepath.Join(dir, "checkout")
	if _, err := osexec.LookPath("git"); err == nil {
		seedGitRemote(t, dir, remote)
	}
	cases = append(cases, conformanceCase{
		Context:  running,
		Requires: "git",
		Conformance: states.Conformance{
			Name:  "git.latest",
			Args:  value.MapOf("name", remote, "target", target, "rev", "main"),
			Probe: absentPath(target),
			Setup: func() error { return os.RemoveAll(target) },
		},
	})

	// ---- ssh_auth and ssh_known_hosts ----
	//
	// Both take a `config` path, which is an authorized_keys or known_hosts
	// file other than the account's own. That is what makes them reachable:
	// the whole effect lands on a file this test named, and no account on
	// this machine is touched.
	const (
		pubKey  = "AAAAC3NzaC1lZDI1NTE5AAAAIGV4YW1wbGVrZXlmb3Jjb25mb3JtYW5jZXRlc3Rz"
		hostKey = "AAAAC3NzaC1lZDI1NTE5AAAAIGhvc3RrZXlmb3Jjb25mb3JtYW5jZXRlc3RpbmdY"
	)

	authFile := filepath.Join(dir, "authorized_keys")
	cases = append(cases, conformanceCase{
		Conformance: states.Conformance{
			Name: "ssh_auth.present",
			Args: value.MapOf("name", pubKey, "enc", "ssh-ed25519",
				"comment", "conformance", "config", authFile),
			Probe: probeFileBody(authFile),
			// An existing file with somebody else's key in it, so the
			// state has to add to a file rather than create one -- which
			// is the case that can lose the other key.
			Setup: func() error {
				return os.WriteFile(authFile,
					[]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG90aGVya2V5dGhhdG11c3RzdXJ2aXZldGhpcw other\n"),
					0o600)
			},
		},
	})

	authAbsentFile := filepath.Join(dir, "authorized_keys_absent")
	cases = append(cases, conformanceCase{
		Conformance: states.Conformance{
			Name:  "ssh_auth.absent",
			Args:  value.MapOf("name", pubKey, "config", authAbsentFile),
			Probe: probeFileBody(authAbsentFile),
			Setup: func() error {
				return os.WriteFile(authAbsentFile,
					[]byte("ssh-ed25519 "+pubKey+" conformance\n"+
						"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG90aGVya2V5dGhhdG11c3RzdXJ2aXZldGhpcw other\n"),
					0o600)
			},
		},
	})

	knownFile := filepath.Join(dir, "known_hosts")
	cases = append(cases, conformanceCase{
		Conformance: states.Conformance{
			Name: "ssh_known_hosts.present",
			// The key is declared rather than scanned, which is the whole
			// reason this one needs no network.
			Args: value.MapOf("name", "host.example.com", "key", hostKey,
				"enc", "ssh-ed25519", "config", knownFile),
			Probe: probeFileBody(knownFile),
			Setup: func() error {
				return os.WriteFile(knownFile,
					[]byte("other.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG90aGVyaG9zdGtleXRoYXRtdXN0c3Vydml2\n"),
					0o644)
			},
		},
	})

	knownAbsentFile := filepath.Join(dir, "known_hosts_absent")
	cases = append(cases, conformanceCase{
		Conformance: states.Conformance{
			Name:  "ssh_known_hosts.absent",
			Args:  value.MapOf("name", "host.example.com", "config", knownAbsentFile),
			Probe: probeFileBody(knownAbsentFile),
			Setup: func() error {
				return os.WriteFile(knownAbsentFile,
					[]byte("host.example.com ssh-ed25519 "+hostKey+"\n"+
						"other.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG90aGVyaG9zdGtleXRoYXRtdXN0c3Vydml2\n"),
					0o644)
			},
		},
	})

	return cases
}

// writeTestZip writes a zip holding the given paths and contents.
func writeTestZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// probeExtractedTree reports every file under a root and what it holds.
func probeExtractedTree(root string) func() (string, error) {
	return func() (string, error) {
		var seen []string
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && path == root {
					return nil
				}
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if d.IsDir() {
				seen = append(seen, "dir "+rel)
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			seen = append(seen, "file "+rel+" "+string(body))
			return nil
		})
		if err != nil {
			return "", err
		}
		sort.Strings(seen)
		return strings.Join(seen, "\n"), nil
	}
}

// probeCertificate reports the certificate's bytes, which carry its serial
// and its validity window -- so a re-issue that produced an equivalent
// certificate still reads as a change.
func probeCertificate(path string) func() (string, error) {
	return func() (string, error) {
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return "absent", nil
		}
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

// probeFileBody reports a file's contents, which for authorized_keys and
// known_hosts is the whole of the managed state -- including the other
// account's key that must survive.
func probeFileBody(path string) func() (string, error) {
	return func() (string, error) {
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return "absent", nil
		}
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

// seedGitRemote makes a bare repository with one commit on `main`.
func seedGitRemote(t *testing.T, dir, remote string) {
	t.Helper()
	work := filepath.Join(dir, "seed-work")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, remote, "init", "-q", "--bare", ".")
	runGit(t, dir, "clone", "-q", remote, work)
	if err := os.WriteFile(filepath.Join(work, "f"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "f")
	runGit(t, work, "commit", "-qm", "one")
	runGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
}
