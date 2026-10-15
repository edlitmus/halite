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
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/pki"
)

// `serve` tells a fault a restart cannot fix from one it can, by exit
// status, and the API's unit stops on the first and restarts on the
// second.
//
// The mirror of the hub's defect (DIVERGENCE 5.287), the other way round.
// `serve` exited 1 for both kinds, and the unit carried
// RestartPreventExitStatus=1, so it stopped on both: an API whose
// `listen` named an address that was not up yet at boot, or whose port a
// previous process still held, exited once and stayed down until
// somebody started it, and so did one that failed while serving.
// DIVERGENCE 5.289.
//
// Each case is a real `serve`, re-executed from this test binary, on a
// configuration that fails in exactly one way.

// apiFixture is everything `serve` needs to get as far as its listener:
// a hub CA, an operator certificate for the hub, a serving certificate,
// and a state directory for the token store. The hub it names is never
// asked anything, because `serve` does not ask the hub anything until a
// request needs it; that is part of what is being tested.
func apiFixture(t *testing.T) (root, base string) {
	t.Helper()
	root = t.TempDir()
	ca, err := pki.NewCA(pki.ECDSAP256, "hub CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	files := pki.Files{Dir: filepath.Join(root, "pki")}
	if err := os.MkdirAll(files.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.Path(pki.CACertFile), pki.EncodeCert(ca.Cert.Raw), 0o644); err != nil {
		t.Fatal(err)
	}
	issueOperatorFiles(t, ca, files, "api")

	// The serving certificate. Issued by the same CA for convenience; all
	// `serve` asks of it is that it loads.
	key, _ := pki.GenerateKey(pki.ECDSAP256)
	der, err := ca.IssueHub(key, []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := pki.EncodeKey(key)
	tlsCert, tlsKey := filepath.Join(root, "api.crt"), filepath.Join(root, "api.key")
	if err := os.WriteFile(tlsCert, pki.EncodeCert(der), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tlsKey, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	// 192.0.2.2 is TEST-NET-1: nothing answers there, and nothing tries.
	base = fmt.Sprintf("state_dir: %s\npki_dir: %s\nhub: 192.0.2.2\ntls_cert: %s\ntls_key: %s\n",
		filepath.Join(root, "state"), files.Dir, tlsCert, tlsKey)
	return root, base
}

// serveOnce runs `serve` on an api.yaml of the fixture's settings plus
// extra, and returns how it exited. None of the cases gets as far as
// serving, so it is waited for rather than stopped.
func serveOnce(t *testing.T, root, body string) (stderr string, code int) {
	t.Helper()
	config := filepath.Join(root, "api.yaml")
	if err := os.WriteFile(config, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code = run(t, "serve", "--root", root, "--config", config)
	return stderr, code
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

// The configuration faults, one from each part of start-up, and a listen
// address that is wrong rather than early.
func TestAPIServeExitsConfigForAFaultARestartCannotFix(t *testing.T) {
	for name, c := range map[string]struct {
		// strip names a line of the fixture to leave out.
		strip, extra, says string
	}{
		"no serving certificate": {strip: "tls_cert:", says: "needs a serving certificate"},
		"no hub":                 {strip: "hub:", says: "this service has no hub"},
		"a hooks block that does not parse": {
			extra: "hooks:\n  deploy:\n    auth: rot13\n", says: "rot13"},
		// An mtls hook could never authenticate a delivery, and is a
		// configuration fault now rather than a dead hook. DIVERGENCE 5.290.
		"an mtls hook": {
			extra: "hooks:\n  deploy:\n    auth: mtls\n", says: "auth: mtls"},
		"an issuer with no role map": {
			extra: "oidc_issuer: https://idp.example\noidc_client_id: halite\n", says: "oidc_role_map"},
		// A listen address with no port fails at the listener, as an
		// unassigned address does, and is the operator's to fix.
		"a listen address that does not parse": {extra: "listen: not-an-address\n", says: "listening on"},
	} {
		t.Run(name, func(t *testing.T) {
			root, base := apiFixture(t)
			var body strings.Builder
			for _, line := range strings.SplitAfter(base, "\n") {
				if c.strip == "" || !strings.HasPrefix(line, c.strip) {
					body.WriteString(line)
				}
			}
			if !strings.Contains(c.extra, "listen:") {
				body.WriteString("listen: " + freeAddr(t) + "\n")
			}
			body.WriteString(c.extra)
			stderr, code := serveOnce(t, root, body.String())
			if code != cli.ExitConfig {
				t.Errorf("serve exited %d, want cli.ExitConfig (%d)\nstderr: %s",
					code, cli.ExitConfig, stderr)
			}
			// So that a pass is this fault and not another one earlier.
			if !strings.Contains(stderr, c.says) {
				t.Errorf("serve did not fail on %s; stderr: %s", c.says, stderr)
			}
		})
	}
}

// The listener failures a restart fixes exit 1, which the unit restarts.
func TestAPIServeExitsOneForAListenerThatMayComeRight(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cli.ListenMayRecover reads Unix errnos; on Windows these are configuration faults")
	}
	t.Run("the port is still held", func(t *testing.T) {
		held, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		root, base := apiFixture(t)
		stderr, code := serveOnce(t, root, base+"listen: "+held.Addr().String()+"\n")
		if code != 1 {
			t.Errorf("serve on a held port exited %d, want 1\nstderr: %s", code, stderr)
		}
		if !strings.Contains(stderr, "listening on") {
			t.Errorf("serve failed somewhere other than the listener: %s", stderr)
		}
	})
	t.Run("the address is not on this host yet", func(t *testing.T) {
		// 192.0.2.1 is TEST-NET-1 (RFC 5737), assigned to no host.
		root, base := apiFixture(t)
		stderr, code := serveOnce(t, root, base+"listen: 192.0.2.1:4511\n")
		if code != 1 {
			t.Errorf("serve on an unassigned address exited %d, want 1\nstderr: %s", code, stderr)
		}
		if !strings.Contains(stderr, "listening on") {
			t.Errorf("serve failed somewhere other than the listener: %s", stderr)
		}
	})
}

// The unit and the program agree: the API's unit stops on the status
// `serve` uses for a fault a restart cannot fix, and only that one.
//
// The API's unit is the one that had this wrong: it named 1, which
// `serve` also uses for a listener a restart fixes. It is a check of the
// file; nothing here starts the unit under systemd.
func TestAPIUnitStopsOnExitConfigAndRestartsOnOne(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "contrib", "systemd", "halite-api.service"))
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
		t.Errorf("RestartPreventExitStatus=%v does not name cli.ExitConfig (%d), so an API "+
			"that cannot start is restarted into the same error every RestartSec",
			directives["RestartPreventExitStatus"], cli.ExitConfig)
	}
	if prevent["1"] {
		t.Errorf("RestartPreventExitStatus= names 1, which `serve` uses for a listen address " +
			"that is not there yet and for a failure while serving; the unit would stop " +
			"where a restart was the fix")
	}
}
