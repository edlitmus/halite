package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// The hub renews its own certificate: at startup once it is past half
// its life, and while it runs, serving the new one without a restart.
//
// It did neither. It loaded hub.crt once and issued another only when
// it found the one on disk had already expired, so a hub left running
// served an expired certificate from day 90 and every node failed its
// handshake, and a restart before then changed nothing. DIVERGENCE 5.259.
//
// A real `serve`, with a 30-second certificate_lifetime: renewal falls
// due 15 seconds after issue and is looked for every 10.
func TestTheHubRenewsItsOwnCertificate(t *testing.T) {
	if testing.Short() {
		t.Skip("waits about forty seconds for two renewals")
	}
	root := t.TempDir()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	config := filepath.Join(root, "hub.yaml")
	body := fmt.Sprintf("listen: %s\npki_dir: %s\nstate_dir: %s\ncache_dir: %s\ncertificate_lifetime: 30s\n",
		addr, filepath.Join(root, "pki"), filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err := os.WriteFile(config, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs []string
	files := map[*exec.Cmd]*os.File{}
	t.Cleanup(func() {
		if t.Failed() {
			for _, l := range logs {
				b, _ := os.ReadFile(l)
				t.Logf("%s:\n%s", filepath.Base(l), b)
			}
		}
	})
	start := func() *exec.Cmd {
		// --names skips the hostname lookup serverNames does by default,
		// which is a DNS query and can take longer than the test waits.
		cmd := exec.Command(os.Args[0], "serve", "--root", root, "--config", config, "--names", "127.0.0.1")
		cmd.Env = append(os.Environ(), reexec+"=1")
		logFile, err := os.Create(filepath.Join(root, fmt.Sprintf("hub-%d.log", time.Now().UnixNano())))
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdout, cmd.Stderr = logFile, logFile
		logs = append(logs, logFile.Name())
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// Closed once the hub has exited: Windows will not delete a file
		// that is still open, and the temporary directory's cleanup
		// failed on this log when it was left open.
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = logFile.Close() })
		files[cmd] = logFile
		return cmd
	}
	// An interrupt where there is one, so the hub shuts down as it would
	// under its service manager. Windows has none to send to another
	// process -- Signal(os.Interrupt) returns an error there -- and
	// waiting after a signal that was never delivered waited for the
	// test's own timeout, ten minutes later.
	stop := func(cmd *exec.Cmd) {
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		_ = files[cmd].Close()
	}
	// serial is the certificate the hub presents now, waiting for it to
	// be listening.
	serial := func() string {
		deadline := time.Now().Add(10 * time.Second)
		for {
			conn, err := tls.Dial("tcp", addr, &tls.Config{
				InsecureSkipVerify: true, MinVersion: tls.VersionTLS13,
				NextProtos: []string{transport.ALPN, transport.Negotiated},
			})
			if err == nil {
				defer conn.Close()
				return conn.ConnectionState().PeerCertificates[0].SerialNumber.String()
			}
			if time.Now().After(deadline) {
				t.Fatalf("the hub never answered on %s: %v", addr, err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	hub := start()
	first := serial()
	stop(hub)

	// Past half its life and not expired: a restart now must renew.
	time.Sleep(16 * time.Second)
	hub = start()
	second := serial()
	if second == first {
		t.Fatalf("a hub started 16 seconds into a 30-second certificate still serves it (serial %s)", first)
	}
	// The renewal loop checks as soon as the hub is listening, so a new
	// serial alone does not show that startup renewed: the loop would
	// have. The startup path says so in its own words.
	startLog, err := os.ReadFile(logs[len(logs)-1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(startLog), "the hub's certificate is past half its life; renewing it") {
		t.Errorf("the restarted hub did not renew at startup; only its running loop did")
	}

	// And the running hub renews again without being restarted.
	deadline := time.Now().Add(30 * time.Second)
	for serial() == second {
		if time.Now().After(deadline) {
			t.Fatalf("the running hub still served serial %s after 30 seconds of a 30-second certificate", second)
		}
		time.Sleep(time.Second)
	}
	stop(hub)
}
