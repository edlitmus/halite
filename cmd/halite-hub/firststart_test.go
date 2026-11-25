package main

import (
	"crypto"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// A hub killed during its first start, at either of the two moments that
// used to strand it, starts again without anybody moving a file.
//
// The layouts are the two a stress loop left behind when it killed a first
// `serve` at random moments (150 runs, before the fix): ca.key with no
// ca.crt in 6 of them, refused with "already holds an enrollment CA", and
// an empty hub.key beside a whole CA in one, refused with "already holds a
// private key". CI met the second on macos-15. DIVERGENCE 5.739.
//
// Built here rather than raced for, because a race that lands one run in
// a hundred and fifty is a test that passes. The loop is what found them;
// this is what holds them.
func TestAHubKilledDuringItsFirstStartStartsAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a hub")
	}
	t.Run("a CA key written and its certificate not", func(t *testing.T) {
		lab, files := newFirstStartLab(t)
		key, err := pki.GenerateKey(pki.ECDSAP384)
		if err != nil {
			t.Fatal(err)
		}
		if err := files.WriteKey(pki.CAKeyFile, key); err != nil {
			t.Fatal(err)
		}
		lab.serveUntilListening()
		cert, err := files.ReadCert(pki.CACertFile)
		if err != nil {
			t.Fatal(err)
		}
		if !samePublicKey(cert.PublicKey, key.Public()) {
			t.Fatal("ca.crt is not over the ca.key that was already there: the CA key was replaced")
		}
	})
	t.Run("a hub key created and nothing written into it", func(t *testing.T) {
		lab, files := newFirstStartLab(t)
		if _, err := files.CreateCA(pki.ECDSAP384, "halite enrollment CA", time.Hour); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files.Path(pki.HubKeyFile), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		lab.serveUntilListening()
		if _, err := files.KeyPair(pki.HubCertFile, pki.HubKeyFile); err != nil {
			t.Fatal(err)
		}
	})
}

type firstStartLab struct {
	*orchWaitLab
	addr string
}

func newFirstStartLab(t *testing.T) (*firstStartLab, pki.Files) {
	t.Helper()
	dir := t.TempDir()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	lab := &firstStartLab{
		orchWaitLab: &orchWaitLab{t: t, dir: dir,
			hubArgs: []string{"--config", dir + "/hub.yaml", "--root", dir, "--pki-dir", dir + "/pki"}},
		addr: addr,
	}
	lab.write("states/.keep", "")
	lab.write("pillar/top.sls", "base: {}\n")
	lab.write("hub.yaml", fmt.Sprintf("listen: %s\nstate_dir: %[2]s/state\ncache_dir: %[2]s/cache\n"+
		"file_roots: {base: [%[2]s/states]}\npillar_roots: {base: [%[2]s/pillar]}\n", addr, dir))
	return lab, pki.Files{Dir: filepath.Join(dir, "pki")}
}

// serveUntilListening starts serve and waits for it to listen, failing at
// once, with what it said, if it exits first: a refusal is an exit, and
// waiting out a timeout for one says less than the refusal does.
func (l *firstStartLab) serveUntilListening() {
	l.t.Helper()
	cmd := l.command("serve", "--names", "127.0.0.1,localhost")
	var log syncBuffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		l.t.Fatal(err)
	}
	// One Wait, here, and the cleanup waits for it rather than calling
	// Wait a second time from another goroutine.
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	l.t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
		if l.t.Failed() {
			l.t.Logf("serve:\n%s", log.String())
		}
	})
	deadline := time.After(15 * time.Second)
	for {
		if conn, err := net.DialTimeout("tcp", l.addr, time.Second); err == nil {
			conn.Close()
			return
		}
		select {
		case <-done:
			l.t.Fatalf("serve exited before it listened (%v); its output is logged below", waitErr)
		case <-deadline:
			l.t.Fatalf("serve was not listening on %s after 15s", l.addr)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func samePublicKey(a, b crypto.PublicKey) bool {
	eq, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && eq.Equal(b)
}
