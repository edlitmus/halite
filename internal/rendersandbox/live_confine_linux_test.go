//go:build linux

package rendersandbox

import (
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveRenderSandboxConfinesTheChildAsRequired is issue 762's
// privileged run: a node running as root, `render_sandbox_user: nobody`
// and `render_sandbox_require: [identity, network]`, checked from inside
// the child rather than from what the parent meant to apply.
//
// It needs root, because the identity and the namespace both do, and an
// account called nobody. The fleet's linux leg runs it.
func TestLiveRenderSandboxConfinesTheChildAsRequired(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1, as root, to drive the render sandbox's confinement")
	}
	if os.Geteuid() != 0 {
		t.Skip("the render child is dropped to another account and given its own network namespace only by root")
	}
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Skipf("this machine has no account called nobody: %v", err)
	}
	nobodyUID, _ := strconv.Atoi(nobody.Uid)

	// The child re-executes this test binary, which `go test` built in a
	// directory only root can enter; nobody could not start it there.
	exe := copyExecutable(t)

	// A file only root may read, in a directory anybody may enter, so
	// that a refusal is about the file and not the path to it.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "root-only")
	if err := os.WriteFile(secret, []byte("not for the render child\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A listener on the host's loopback, which the parent can reach.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	t.Run("confined", func(t *testing.T) {
		stderr := &lockedBuffer{}
		s := New(Config{
			Exe:     exe,
			Args:    []string{"probe-read=" + secret, "probe-dial=" + ln.Addr().String()},
			User:    "nobody",
			Require: []string{RequireIdentity, RequireNetwork},
			Timeout: 30 * time.Second,
			Stderr:  stderr,
		})
		defer s.Close()

		o, err := s.Observe()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("the child reports uid %d gid %d, interfaces up %v", o.UID, o.GID, o.UpInterfaces)
		if o.UID != nobodyUID {
			t.Errorf("the child runs as uid %d, not nobody's %d", o.UID, nobodyUID)
		}
		if len(o.UpInterfaces) != 0 || o.NetworkError != "" {
			t.Errorf("the child sees interfaces up %v (error %q); in a namespace of its own it should see none",
				o.UpInterfaces, o.NetworkError)
		}
		if reasons, err := s.Unmet(); err != nil || len(reasons) != 0 {
			t.Errorf("the requirement is reported unmet: %v %v", reasons, err)
		}
		if err := renderOne(s); err != nil {
			t.Errorf("a confined child that meets the requirement did not render: %v", err)
		}

		read, ok := stderr.probeResult("probe-read")
		if !ok {
			t.Fatalf("the child never reported reading the file; its stderr:\n%s", stderr)
		}
		if !strings.Contains(read, "permission denied") {
			t.Errorf("the child, as nobody, read a file only root may read: %q", read)
		}
		dial, ok := stderr.probeResult("probe-dial")
		if !ok {
			t.Fatalf("the child never reported dialling; its stderr:\n%s", stderr)
		}
		if dial == "ok" {
			t.Errorf("the child reached a listener on the host from its own network namespace")
		}
		t.Logf("from the child: read %q, dial %q", read, dial)
	})

	// The same probes from a child that keeps root's identity. Without
	// this the read above could be refused for some reason that has
	// nothing to do with the account -- a probe that cannot succeed
	// proves nothing about the one that did not.
	t.Run("the read probe works when the child is root", func(t *testing.T) {
		stderr := &lockedBuffer{}
		s := New(Config{Exe: exe, Args: []string{"probe-read=" + secret}, Timeout: 30 * time.Second, Stderr: stderr})
		defer s.Close()
		if _, err := s.Observe(); err != nil {
			t.Fatal(err)
		}
		if read, ok := stderr.probeResult("probe-read"); !ok || read != "ok" {
			t.Errorf("a child running as root could not read the file either (%q), so the probe is not measuring the account", read)
		}
		if c, err := net.Dial("tcp", ln.Addr().String()); err != nil {
			t.Errorf("the parent cannot reach its own listener, so the dial probe is not measuring the namespace: %v", err)
		} else {
			c.Close()
		}
	})
}

// copyExecutable puts this test binary where any account can run it.
func copyExecutable(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "halite-render-sandbox-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	path := filepath.Join(dir, "child")
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
