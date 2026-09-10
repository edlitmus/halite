package varlink

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeService is a varlink server that answers one request per
// connection from a table of method -> reply.
func fakeService(t *testing.T, replies map[string]string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "svc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				frame, err := r.ReadString(0)
				if err != nil {
					return
				}
				var req struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal([]byte(strings.TrimRight(frame, "\x00")), &req)
				reply, ok := replies[req.Method]
				if !ok {
					reply = `{"error":"org.varlink.service.MethodNotFound","parameters":{}}`
				}
				_, _ = conn.Write(append([]byte(reply), 0))
			}()
		}
	}()
	return sock
}

func TestVarlinkCallReturnsReplyParameters(t *testing.T) {
	sock := fakeService(t, map[string]string{
		"io.systemd.Journal.Synchronize": `{"parameters":{}}`,
		"com.example.Echo":               `{"parameters":{"said":"hi"}}`,
	})

	p, err := Call(context.Background(), sock, "io.systemd.Journal.Synchronize", nil)
	if err != nil {
		t.Fatalf("Synchronize: %v", err)
	}
	if string(p) != "{}" {
		t.Errorf("parameters = %s, want {}", p)
	}

	p, err = Call(context.Background(), sock, "com.example.Echo", map[string]string{"say": "hi"})
	if err != nil {
		t.Fatalf("Echo: %v", err)
	}
	var got struct{ Said string }
	if err := json.Unmarshal(p, &got); err != nil || got.Said != "hi" {
		t.Errorf("Echo returned %s (%v)", p, err)
	}
}

func TestVarlinkErrorReplyIsATypedError(t *testing.T) {
	sock := fakeService(t, map[string]string{
		"io.systemd.Journal.Rotate": `{"error":"io.systemd.Journal.NotSupportedByNamespaces","parameters":{"why":"ns"}}`,
	})
	_, err := Call(context.Background(), sock, "io.systemd.Journal.Rotate", nil)
	var vErr *Error
	if !as(err, &vErr) {
		t.Fatalf("want *varlink.Error, got %T: %v", err, err)
	}
	if vErr.Name != "io.systemd.Journal.NotSupportedByNamespaces" {
		t.Errorf("error name = %q", vErr.Name)
	}
	if !strings.Contains(vErr.Error(), "ns") {
		t.Errorf("Error() dropped the parameters: %q", vErr.Error())
	}
}

func TestVarlinkUnknownMethodIsAnError(t *testing.T) {
	sock := fakeService(t, nil)
	_, err := Call(context.Background(), sock, "com.example.Nope", nil)
	var vErr *Error
	if !as(err, &vErr) || vErr.Name != "org.varlink.service.MethodNotFound" {
		t.Fatalf("want a MethodNotFound varlink error, got %v", err)
	}
}

func TestVarlinkDialFailureIsNotATypedError(t *testing.T) {
	_, err := Call(context.Background(), "/no/such/socket", "com.example.X", nil)
	if err == nil {
		t.Fatal("dialing a missing socket should fail")
	}
	var vErr *Error
	if as(err, &vErr) {
		t.Errorf("a dial failure came back as a *varlink.Error: %v", err)
	}
	if !strings.Contains(err.Error(), "dialing") {
		t.Errorf("the error does not say it was a dial failure: %v", err)
	}
}

func TestVarlinkRespectsAContextDeadline(t *testing.T) {
	// A listener that accepts and never replies.
	sock := filepath.Join(t.TempDir(), "hang.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn // hold it open, never write
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Call(ctx, sock, "com.example.Slow", nil); err == nil {
		t.Fatal("a call against a silent server should time out")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the deadline was not honoured: waited %s", elapsed)
	}
}

// as is errors.As without importing errors into every test line.
func as(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
