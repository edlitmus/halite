package main

import (
	"bytes"
	"errors"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// Manual mode, from one address: the hub's limiter admits
// enroll_source_burst (32) new names, each made pending, and refuses the
// rest with 429 until its budget drains at one every 10 s. Nothing is
// issued, and nothing fails.
func TestAnEnrollStormInManualMode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and the harness")
	}
	sf, root, addr := stormHub(t, "manual")
	jsonl := filepath.Join(t.TempDir(), "storm.jsonl")
	out, err := exec.Command(sf, "enroll-storm", "-root", root, "-hub", addr, "-server-name", "127.0.0.1",
		"-n", "40", "-concurrency", "8", "-json", jsonl).CombinedOutput()
	if err != nil {
		t.Fatalf("enroll-storm: %v\n%s", err, out)
	}
	t.Logf("enroll-storm printed:\n%s", out)
	r := oneStorm(t, jsonl)
	pending, limited := r.Outcomes["pending"], r.Outcomes["rate_limited"]
	// 32 at once, and the budget drains by one every 10 s, so a storm
	// that takes a second or two can see a 33rd admitted.
	if pending < 32 || pending > 33 || pending+limited != 40 || r.Mode != "manual" {
		t.Fatalf("outcomes %v in %s mode; want 32 or 33 pending and the rest rate_limited, in manual mode\n%s", r.Outcomes, r.Mode, out)
	}
}

// Token mode: a token for 10 names admits 10 and refuses the 11th and
// 12th, which is the token spent, not the limiter: an issue is refunded.
func TestAnEnrollStormInTokenMode(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and the harness")
	}
	sf, root, addr := stormHub(t, "token", "-token-uses", "10")
	jsonl := filepath.Join(t.TempDir(), "storm.jsonl")
	out, err := exec.Command(sf, "enroll-storm", "-root", root, "-hub", addr, "-server-name", "127.0.0.1",
		"-n", "12", "-concurrency", "4", "-token-file", filepath.Join(root, "bootstrap.token"), "-json", jsonl).CombinedOutput()
	if err != nil {
		t.Fatalf("enroll-storm: %v\n%s", err, out)
	}
	t.Logf("enroll-storm printed:\n%s", out)
	r := oneStorm(t, jsonl)
	if r.Outcomes["issued"] != 10 || r.Outcomes["refused"] != 2 || r.Mode != "token" {
		t.Fatalf("outcomes %v in %s mode; want 10 issued and 2 refused, in token mode\n%s", r.Outcomes, r.Mode, out)
	}
	if !strings.Contains(string(out), "certificates issued a second") {
		t.Fatalf("printed:\n%s", out)
	}
}

func TestEnrollOutcomeNamesWhatTheHubCounts(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "issued"},
		{transport.ErrPending, "pending"},
		{&transport.StatusError{Status: 429, Code: transport.CodeRateLimited}, "rate_limited"},
		{&transport.RefusedError{Status: 403, Code: transport.CodeRefused}, "refused"},
		{&transport.RefusedError{Status: 400, Code: transport.CodeMalformed}, "failed"},
		{&transport.StatusError{Status: 500, Code: transport.CodeInternal}, "failed"},
		{errors.New("dial tcp: connection refused"), "error"},
	} {
		if got := enrollOutcome(tc.err); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.err, got, tc.want)
		}
	}
}

// stormHub builds the hub and simfleet, runs setup with one identity in
// the given enrollment mode, and serves it.
func stormHub(t *testing.T, mode string, setupArgs ...string) (sf, root, addr string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	for _, pkg := range []string{"cmd/halite-hub", "tools/simfleet"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(bin, filepath.Base(pkg)+exe),
			"github.com/edlitmus/halite/"+pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("building %s: %v\n%s", pkg, err, out)
		}
	}
	sf = filepath.Join(bin, "simfleet"+exe)
	root = filepath.Join(dir, "fleet")
	args := append([]string{"setup", "-root", root, "-n", "1", "-enrollment", mode}, setupArgs...)
	if out, err := exec.Command(sf, args...).CombinedOutput(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = l.Addr().String()
	l.Close()
	var hubLog bytes.Buffer
	hub := exec.Command(filepath.Join(bin, "halite-hub"+exe), "serve", "--root", root, "--config", filepath.Join(root, "hub.yaml"),
		"--listen", addr, "--names", "127.0.0.1,localhost")
	hub.Stdout, hub.Stderr = &hubLog, &hubLog
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = hub.Process.Kill()
		_ = hub.Wait()
		if t.Failed() {
			t.Logf("hub:\n%s", hubLog.String())
		}
	})
	waitFor(t, "the hub to listen", func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	return sf, root, addr
}

// exe is what Windows needs on the end of a program's name for exec to
// find it by path: `go build -o bin/simfleet` writes that name exactly,
// and on Windows a file without .exe is not something exec will run.
var exe = func() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}()

func oneStorm(t *testing.T, path string) stormResult {
	t.Helper()
	var r stormResult
	if err := readJSON(path, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
