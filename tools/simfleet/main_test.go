package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func at(base time.Time, d time.Duration) int64 { return base.Add(d).UnixNano() }

// merge expects every node a driver ran, including a driver whose file
// never mentions the job: those nodes never received it, and a merge
// that dropped them would report 10 of 10 for a 20-node fleet.
func TestMergeCountsADriverThatNeverSawTheJob(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	sub := submitFile{JID: "j1", Fun: "test.ping", Matched: 4, SubmitStart: t0, SubmitEnd: t0.Add(100 * time.Millisecond)}
	a := driverFile{Driver: "a", First: 1, N: 2, Jobs: []driverJob{{JID: "j1", Fun: "test.ping", Sims: []simTimes{
		{ID: "sim-00001.simfleet", Received: at(t0, 200*time.Millisecond), Returned: at(t0, time.Second)},
		{ID: "sim-00002.simfleet", Received: at(t0, 300*time.Millisecond), Returned: at(t0, 2*time.Second)},
	}}}}
	b := driverFile{Driver: "b", First: 3, N: 2}
	res, err := merge(sub, []driverFile{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if res.Expected != 4 || res.Returned.Count != 2 || res.Verdict == nil || res.Verdict.Met {
		t.Fatalf("got expected %d, returned %d, verdict %+v; want 4, 2 and NOT MET", res.Expected, res.Returned.Count, res.Verdict)
	}
	if got := res.ReceivedAfterAccept; got < 0.199 || got > 0.201 {
		t.Fatalf("last received after accept = %v s, want 0.2 (300 ms after sending, 100 ms of it in the submit)", got)
	}
}

func TestMergeRefusesTheSameNodeInTwoDrivers(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	sub := submitFile{JID: "j1", Fun: "test.ping", SubmitStart: t0, SubmitEnd: t0}
	one := func(name string) driverFile {
		return driverFile{Driver: name, First: 1, N: 1, Jobs: []driverJob{{JID: "j1", Sims: []simTimes{{ID: "sim-00001.simfleet"}}}}}
	}
	if _, err := merge(sub, []driverFile{one("a"), one("b")}); err == nil || !strings.Contains(err.Error(), "-first") {
		t.Fatalf("err = %v, want a refusal naming -first", err)
	}
}

// The dispatch row is taken from when the job was sent, and the time
// after the hub accepted it is reported beside it, not instead of it: on
// step 0's first run, a submit blocked for 25.5 s and the dispatch still
// read MET at 26 s (limit L11), which is the operator's view and true,
// but said nothing of the 25 s unless the submit time was read too.
func TestDispatchVerdictCountsTheSubmit(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	sub := submitFile{JID: "j", Fun: "state.apply", SubmitStart: t0, SubmitEnd: t0.Add(25 * time.Second)}
	res := summarize(sub, 1, []int64{at(t0, 31*time.Second)}, []int64{at(t0, 32*time.Second)}, 0, 1)
	if res.Verdict.Met {
		t.Fatal("31 s after sending is not within 30 s, however quickly it followed the accept")
	}
	var out bytes.Buffer
	res.print(&out)
	if !strings.Contains(out.String(), "the submit took 25s") || !strings.Contains(out.String(), "last 6s after the hub accepted") {
		t.Fatalf("output does not show both clocks:\n%s", out.String())
	}
}

// Real binaries over a real socket: `simfleet setup`, `halite-hub serve`,
// two `simfleet run -no-submit` drivers on separate ranges, one
// `simfleet submit`, and `simfleet merge`; then the single-process path
// with -json. The merged result must account for every node of both
// drivers, and the single-process one must agree with what it printed.
func TestTwoDriversOneJob(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a hub and the harness")
	}
	if runtime.GOOS == "windows" {
		t.Skip("stops the drivers with an interrupt, which Windows cannot send to another process")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	for _, pkg := range []string{"cmd/halite-hub", "tools/simfleet"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(bin, filepath.Base(pkg)),
			"github.com/edlitmus/halite/"+pkg).CombinedOutput()
		if err != nil {
			t.Fatalf("building %s: %v\n%s", pkg, err, out)
		}
	}
	sf, hubBin := filepath.Join(bin, "simfleet"), filepath.Join(bin, "halite-hub")
	root := filepath.Join(dir, "fleet")
	if out, err := exec.Command(sf, "setup", "-root", root, "-n", "20").CombinedOutput(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	var hubLog bytes.Buffer
	hub := exec.Command(hubBin, "serve", "--root", root, "--config", filepath.Join(root, "hub.yaml"),
		"--listen", addr, "--names", "127.0.0.1,localhost")
	hub.Stdout, hub.Stderr = &hubLog, &hubLog
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Process.Kill(); _ = hub.Wait() })
	waitFor(t, "the hub to listen", func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
		}
		return err == nil
	})

	common := []string{"-root", root, "-hub", addr, "-server-name", "127.0.0.1"}
	type driver struct {
		cmd *exec.Cmd
		out string
		log *bytes.Buffer
	}
	var drivers []driver
	for i, first := range []string{"1", "11"} {
		out := filepath.Join(dir, "driver"+first+".json")
		var log bytes.Buffer
		cmd := exec.Command(sf, append([]string{"run", "-first", first, "-n", "10", "-no-submit", "-out", out,
			"-driver", string(rune('a' + i))}, common...)...)
		cmd.Stdout, cmd.Stderr = &log, &log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		drivers = append(drivers, driver{cmd, out, &log})
	}
	readDriver := func(p string) driverFile {
		var d driverFile
		_ = readJSON(p, &d)
		return d
	}
	for _, d := range drivers {
		waitFor(t, "a driver to connect its 10 nodes", func() bool { return readDriver(d.out).Connected == 10 })
	}

	subOut := filepath.Join(dir, "ping.json")
	if out, err := exec.Command(sf, append([]string{"submit", "-fun", "test.ping", "-out", subOut}, common...)...).CombinedOutput(); err != nil {
		t.Fatalf("submit: %v\n%s\nhub:\n%s", err, out, hubLog.String())
	}
	var sub submitFile
	if err := readJSON(subOut, &sub); err != nil {
		t.Fatal(err)
	}
	for _, d := range drivers {
		waitFor(t, "a driver to record 10 returns", func() bool {
			for _, j := range readDriver(d.out).Jobs {
				if j.JID == sub.JID {
					n := 0
					for _, s := range j.Sims {
						if s.Returned != 0 {
							n++
						}
					}
					return n == 10
				}
			}
			return false
		})
		if err := d.cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		if err := d.cmd.Wait(); err != nil {
			t.Fatalf("driver: %v\n%s", err, d.log.String())
		}
	}

	merged := filepath.Join(dir, "merged.jsonl")
	out, err := exec.Command(sf, "merge", "-submit", subOut, "-json", merged, drivers[0].out, drivers[1].out).CombinedOutput()
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	res := oneResult(t, merged)
	if res.Expected != 20 || res.Returned.Count != 20 || res.Received.Count != 20 || res.Drivers != 2 ||
		res.Verdict == nil || !res.Verdict.Met || res.Verdict.Want != 20 {
		t.Fatalf("merged result %+v, verdict %+v; want 20 of 20 from 2 drivers, MET\n%s", res, res.Verdict, out)
	}
	if !strings.Contains(string(out), "returned by 20 of 20") {
		t.Fatalf("merge printed:\n%s", out)
	}

	// The single-process path, through the same summarize.
	single := filepath.Join(dir, "single.jsonl")
	out, err = exec.Command(sf, append([]string{"run", "-n", "20", "-ping", "-apply", "-json", single}, common...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	lines := readLines(t, single)
	if len(lines) != 2 {
		t.Fatalf("%d JSON lines, want one for test.ping and one for state.apply", len(lines))
	}
	for _, r := range lines {
		if r.Returned.Count != 20 || r.Verdict == nil || !r.Verdict.Met {
			t.Fatalf("%s: %+v, verdict %+v; want 20 returns, MET\n%s", r.Fun, r, r.Verdict, out)
		}
		if !strings.Contains(string(out), r.Fun+": jid "+r.JID) {
			t.Fatalf("the JSON names jid %s for %s, and the printed output does not:\n%s", r.JID, r.Fun, out)
		}
	}
}

func oneResult(t *testing.T, path string) result {
	t.Helper()
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("%s: %d lines, want 1", path, len(lines))
	}
	return lines[0]
}

func readLines(t *testing.T, path string) []result {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []result
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
