package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// `serve` tells a fault a restart cannot fix from one it can, by exit
// status, and the hub's unit stops on the first and restarts on the
// second.
//
// It exited 1 for both. A v0.1.1 hub (2026-10-12, Ubuntu 22.04 arm64,
// the FIPS build) whose `ext_pillar` named aws_secrets_manager with no
// extension installed exited 1, and the unit -- Restart=on-failure, and
// unlike the API's and the node's no RestartPreventExitStatus= -- started
// it again every five seconds, 427 times and counting, each one the same
// lines in the journal. The unit could not simply add
// RestartPreventExitStatus=1, because the hub also exits 1 for a listen
// address that is not on the host yet, and that one a restart fixes.
// DIVERGENCE 5.286.
//
// Each case is a real `serve`, re-executed from this test binary, on a
// configuration that fails in exactly one way.

// serveOnce runs `serve` on a hub.yaml of extra plus throwaway pki, state
// and cache directories, and returns how it exited. None of the cases
// gets as far as serving, so it is waited for rather than stopped.
func serveOnce(t *testing.T, extra string) result {
	t.Helper()
	root := t.TempDir()
	config := filepath.Join(root, "hub.yaml")
	body := fmt.Sprintf("pki_dir: %s\nstate_dir: %s\ncache_dir: %s\n%s",
		filepath.Join(root, "pki"), filepath.Join(root, "state"), filepath.Join(root, "cache"), extra)
	if err := os.WriteFile(config, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// --names skips the hostname lookup, which is DNS and slow on some
	// runners, and is no part of what is being tested.
	return run(t, "serve", "--root", root, "--config", config, "--names", "127.0.0.1")
}

// freeAddr is a loopback address nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	return addr
}

// The configuration faults: the one that was seen, and a listen address
// that is wrong rather than early.
func TestServeExitsConfigForAFaultARestartCannotFix(t *testing.T) {
	pillarRoot := t.TempDir()
	for name, c := range map[string]struct{ extra, says string }{
		// The fault on the real hub, as it was configured: a source
		// that is not installed. pillar_roots, because without it the
		// hub compiles no pillar and reads no ext_pillar.
		"ext_pillar with no extension": {fmt.Sprintf(
			"listen: %s\npillar_roots:\n  base:\n    - %s\next_pillar:\n  - aws_secrets_manager:\n      region: us-east-1\n",
			freeAddr(t), pillarRoot), "no extension of that name is installed"},
		// A listen address with no port fails at the listener, as an
		// unassigned address does, and is the operator's to fix.
		"a listen address that does not parse": {"listen: not-an-address\n", "listening on"},
		"a setting that does not parse":        {fmt.Sprintf("listen: %s\nkey_algorithm: rot13\n", freeAddr(t)), "rot13"},
	} {
		t.Run(name, func(t *testing.T) {
			got := serveOnce(t, c.extra)
			if got.code != cli.ExitConfig {
				t.Errorf("serve exited %d, want cli.ExitConfig (%d)\nstderr: %s",
					got.code, cli.ExitConfig, got.stderr)
			}
			// So that a pass is this fault and not another one earlier.
			if !strings.Contains(got.stderr, c.says) {
				t.Errorf("serve did not fail on %s; stderr: %s", c.says, got.stderr)
			}
		})
	}
}

// The listener failures a restart fixes exit 1, which the unit restarts.
func TestServeExitsOneForAListenerThatMayComeRight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cli.ListenMayRecover reads Unix errnos; on Windows these are configuration faults")
	}
	t.Run("the port is still held", func(t *testing.T) {
		held, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		got := serveOnce(t, "listen: "+held.Addr().String()+"\n")
		if got.code != 1 {
			t.Errorf("serve on a held port exited %d, want 1\nstderr: %s", got.code, got.stderr)
		}
		if !strings.Contains(got.stderr, "listening on") {
			t.Errorf("serve failed somewhere other than the listener: %s", got.stderr)
		}
	})
	t.Run("the address is not on this host yet", func(t *testing.T) {
		// 192.0.2.1 is TEST-NET-1 (RFC 5737), assigned to no host.
		got := serveOnce(t, "listen: 192.0.2.1:4510\n")
		if got.code != 1 {
			t.Errorf("serve on an unassigned address exited %d, want 1\nstderr: %s", got.code, got.stderr)
		}
		if !strings.Contains(got.stderr, "listening on") {
			t.Errorf("serve failed somewhere other than the listener: %s", got.stderr)
		}
	})
}

// The unit and the program agree: the hub's unit stops on the status
// `serve` uses for a fault a restart cannot fix, and only that one.
//
// Two things that have to say the same and are in different files, so
// one can change without the other. It is a check of the file; nothing
// here starts the unit under systemd.
func TestHubUnitStopsOnExitConfigAndRestartsOnOne(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "contrib", "systemd", "halite-hub.service"))
	if err != nil {
		t.Fatal(err)
	}
	directives := map[string][]string{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			directives[k] = append(directives[k], strings.Fields(v)...)
		}
	}
	if got := directives["Restart"]; len(got) != 1 || got[0] != "on-failure" {
		t.Errorf("Restart=%v, want on-failure: exit 1 is a failure a restart can fix", got)
	}
	prevent := map[string]bool{}
	for _, v := range directives["RestartPreventExitStatus"] {
		prevent[v] = true
	}
	if !prevent[strconv.Itoa(cli.ExitConfig)] {
		t.Errorf("RestartPreventExitStatus=%v does not name cli.ExitConfig (%d), so a hub "+
			"that cannot start is restarted into the same error every RestartSec",
			directives["RestartPreventExitStatus"], cli.ExitConfig)
	}
	if prevent["1"] {
		t.Errorf("RestartPreventExitStatus= names 1, which `serve` uses for a listen address " +
			"that is not there yet; the unit would stop where a restart was the fix")
	}
}
