package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// `-h` is the first thing an operator types at a command they do not know,
// and the parser reads it as a flag called `h`. The program looked only for
// `help`, so `halite-node enroll -h` fetched the hub CA and went on to enrol
// (issue 882). Asking what a command is must do nothing at all: no request
// to the hub and no file written.
func TestAskingForHelpWithAShortFlagTouchesNothing(t *testing.T) {
	var hubRequests atomic.Int64
	// Count connections, not requests: a client that reaches the hub and
	// fails the handshake never gets as far as a handler.
	hub := httptest.NewUnstartedServer(http.NotFoundHandler())
	hub.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			hubRequests.Add(1)
		}
	}
	hub.StartTLS()
	defer hub.Close()

	for _, command := range []string{"enroll", "renew", "connect"} {
		for _, flag := range []string{"-h", "--help"} {
			t.Run(command+" "+flag, func(t *testing.T) {
				pkiDir := t.TempDir()
				before := hubRequests.Load()
				got := run(t, command, flag, "--hub", hub.URL, "--pki-dir", pkiDir,
					"--hub-fingerprint", "00:00")

				if got.code != 0 {
					t.Errorf("exited %d, want 0: %s%s", got.code, got.stdout, got.stderr)
				}
				if !strings.Contains(got.stdout, "Usage:") {
					t.Errorf("did not print the usage text: %q", got.stdout)
				}
				if n := hubRequests.Load() - before; n != 0 {
					t.Errorf("opened %d connection(s) to the hub", n)
				}
				entries, err := os.ReadDir(pkiDir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Errorf("wrote %d file(s) under the pki directory", len(entries))
				}
			})
		}
	}
}
