package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// jobRecord is what one driver saw of one job: for each of its simulated
// nodes, when the job arrived and when the hub accepted its return.
//
// Records are kept by jid rather than for "the job being measured",
// because the harness used to time whatever job arrived while it was
// measuring (DIVERGENCE 5.279, limit L2 of docs/simfleet-aws-plan.md): a
// second job in flight was counted as the first. Keyed by jid, two jobs
// at once are two records, and a node that answers before Submit has told
// the harness the jid is still filed under the right one.
type jobRecord struct {
	fun        string
	received   []atomic.Int64 // unix nanos, indexed like fleet.sims; 0 = never
	returned   []atomic.Int64
	returnErrs atomic.Int64
}

// jobs is the set of records one driver holds, created on first sight.
type jobs struct {
	mu   sync.Mutex
	byID map[string]*jobRecord
	n    int
}

func newJobs(n int) *jobs { return &jobs{byID: map[string]*jobRecord{}, n: n} }

func (j *jobs) get(jid, fun string) *jobRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	r := j.byID[jid]
	if r == nil {
		r = &jobRecord{fun: fun, received: make([]atomic.Int64, j.n), returned: make([]atomic.Int64, j.n)}
		j.byID[jid] = r
	}
	return r
}

func (j *jobs) lookup(jid string) *jobRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.byID[jid]
}

// simTimes is one simulated node's part of a job, as a driver writes it
// to disk for `simfleet merge`. Times are unix nanoseconds on the
// driver's clock: merge compares them with the submitting host's clock,
// so the run kit records each host's clock offset before and after.
type simTimes struct {
	ID       string `json:"id"`
	Received int64  `json:"received_unix_nano,omitempty"`
	Returned int64  `json:"returned_unix_nano,omitempty"`
}

// driverJob is one job as one driver saw it.
type driverJob struct {
	JID          string     `json:"jid"`
	Fun          string     `json:"fun"`
	ReturnErrors int64      `json:"return_errors"`
	Sims         []simTimes `json:"sims"`
}

// driverFile is what `simfleet run -no-submit -out FILE` writes.
type driverFile struct {
	Driver         string      `json:"driver"`
	First          int         `json:"first"`
	N              int         `json:"n"`
	Connected      int64       `json:"connected"`
	ConnectSeconds float64     `json:"connect_seconds"`
	Jobs           []driverJob `json:"jobs"`
}

func (j *jobs) snapshot(sims []*sim) []driverJob {
	j.mu.Lock()
	ids := make([]string, 0, len(j.byID))
	for id := range j.byID {
		ids = append(ids, id)
	}
	j.mu.Unlock()
	sort.Strings(ids)
	out := make([]driverJob, 0, len(ids))
	for _, id := range ids {
		r := j.lookup(id)
		dj := driverJob{JID: id, Fun: r.fun, ReturnErrors: r.returnErrs.Load(), Sims: make([]simTimes, len(sims))}
		for i, s := range sims {
			dj.Sims[i] = simTimes{ID: s.id, Received: r.received[i].Load(), Returned: r.returned[i].Load()}
		}
		out = append(out, dj)
	}
	return out
}

// submitFile is what `simfleet submit -out FILE` writes: when the job was
// sent, when the hub accepted it, and what the hub said it matched.
type submitFile struct {
	JID         string    `json:"jid"`
	Fun         string    `json:"fun"`
	Target      string    `json:"target"`
	Matched     int       `json:"matched"`
	Absent      int       `json:"absent"`
	SubmitStart time.Time `json:"submit_start"`
	SubmitEnd   time.Time `json:"submit_end"`
}

// latency is one distribution, in seconds from the start of the submit.
type latency struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_seconds"`
	P95   float64 `json:"p95_seconds"`
	Last  float64 `json:"last_seconds"`
}

// result is one measurement: what `run` prints and, with -json, writes;
// and what `merge` computes from several drivers' files. Both go through
// summarize and print, so a single-driver run and a merged multi-driver
// run cannot report the same data two ways.
type result struct {
	JID           string  `json:"jid"`
	Fun           string  `json:"fun"`
	Expected      int     `json:"expected"`
	Matched       int     `json:"matched"`
	Absent        int     `json:"absent"`
	Drivers       int     `json:"drivers"`
	SubmitSeconds float64 `json:"submit_seconds"`
	Received      latency `json:"received"`
	Returned      latency `json:"returned"`
	ReturnErrors  int64   `json:"return_errors"`
	// ReceivedAfterAccept is the last arrival measured from the moment
	// Submit returned rather than from when it was sent. The dispatch
	// verdict is taken from the send, which is what an operator waits
	// through; this is shown beside it because a submit that blocks for
	// 25 s otherwise hides inside a MET (limit L11).
	ReceivedAfterAccept float64 `json:"last_received_after_accept_seconds"`
	Verdict             *spec30 `json:"verdict,omitempty"`
}

type spec30 struct {
	Row    string  `json:"row"`
	Met    bool    `json:"met"`
	Got    float64 `json:"got_seconds"`
	Have   int     `json:"have"`
	Want   int     `json:"want"`
	Target float64 `json:"target_seconds"`
}

// spec30Row is the SPEC 30 row a function is measured against: test.ping
// by its returns ("95% of returns in 10 s"), state.apply by its dispatch
// ("full dispatch in 30 s"). Any other function is timed and given no
// verdict.
func spec30Row(fun string) (target time.Duration, what string, ok bool) {
	switch fun {
	case "test.ping":
		return 10 * time.Second, "returns", true
	case "state.apply":
		return 30 * time.Second, "dispatch", true
	}
	return 0, "", false
}

// summarize turns arrival times into a result. received and returned
// hold unix nanos for every expected node, 0 where nothing came.
func summarize(sub submitFile, expected int, received, returned []int64, returnErrs int64, drivers int) result {
	t0 := sub.SubmitStart.UnixNano()
	recv := since(t0, received)
	rets := since(t0, returned)
	r := result{
		JID: sub.JID, Fun: sub.Fun, Expected: expected, Matched: sub.Matched, Absent: sub.Absent,
		Drivers:       drivers,
		SubmitSeconds: sub.SubmitEnd.Sub(sub.SubmitStart).Seconds(),
		Received:      dist(recv), Returned: dist(rets), ReturnErrors: returnErrs,
	}
	if len(recv) > 0 {
		r.ReceivedAfterAccept = (time.Duration(t0) + pct(recv, 100) - time.Duration(sub.SubmitEnd.UnixNano())).Seconds()
	}
	if target, what, ok := spec30Row(sub.Fun); ok {
		v := &spec30{Want: expected, Target: target.Seconds()}
		switch what {
		case "returns":
			v.Row = fmt.Sprintf("95%% of returns within %s", target)
			v.Have, v.Got = len(rets), pct(rets, 95).Seconds()
			v.Met = len(rets) == expected && pct(rets, 95) <= target
		case "dispatch":
			v.Row = fmt.Sprintf("full dispatch within %s", target)
			v.Have, v.Got = len(recv), pct(recv, 100).Seconds()
			v.Met = len(recv) == expected && pct(recv, 100) <= target
		}
		r.Verdict = v
	}
	return r
}

func since(t0 int64, at []int64) []time.Duration {
	var out []time.Duration
	for _, v := range at {
		if v != 0 {
			out = append(out, time.Duration(v-t0))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func dist(sorted []time.Duration) latency {
	return latency{Count: len(sorted), P50: pct(sorted, 50).Seconds(), P95: pct(sorted, 95).Seconds(), Last: pct(sorted, 100).Seconds()}
}

func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := (len(sorted)*p + 99) / 100
	if i < 1 {
		i = 1
	}
	if i > len(sorted) {
		i = len(sorted)
	}
	return sorted[i-1].Round(time.Millisecond)
}

func secs(s float64) time.Duration {
	return time.Duration(s * float64(time.Second)).Round(time.Millisecond)
}

// print writes a result the way the harness always has, so that runs
// before and after -json read the same.
func (r result) print(w io.Writer) {
	fmt.Fprintf(w, "\n%s: jid %s, %d nodes matched, %d absent, the submit took %s\n",
		r.Fun, r.JID, r.Matched, r.Absent, secs(r.SubmitSeconds))
	fmt.Fprintf(w, "  received by %d of %d: p50 %s, p95 %s, last %s (last %s after the hub accepted the job)\n",
		r.Received.Count, r.Expected, secs(r.Received.P50), secs(r.Received.P95), secs(r.Received.Last),
		secs(r.ReceivedAfterAccept))
	fmt.Fprintf(w, "  returned by %d of %d: p50 %s, p95 %s, last %s; %d return error(s)\n",
		r.Returned.Count, r.Expected, secs(r.Returned.P50), secs(r.Returned.P95), secs(r.Returned.Last), r.ReturnErrors)
	if v := r.Verdict; v != nil {
		word := "MET"
		if !v.Met {
			word = "NOT MET"
		}
		fmt.Fprintf(w, "  SPEC 30 %s: %s (%s, %d of %d nodes)\n", v.Row, word, secs(v.Got), v.Have, v.Want)
	}
}

// writeJSONLine appends one result to a JSON-lines file.
func writeJSONLine(path string, v any) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := openAppend(path)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
