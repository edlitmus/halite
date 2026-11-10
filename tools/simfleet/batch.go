package main

import (
	"errors"
	"flag"
	"fmt"
	"sort"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// A batched job is SPEC 9.3's `--batch`: the hub delivers it a slice at a
// time and waits for a slice's returns before sending the next
// (runBatches in internal/hub/batch.go). simfleet used to send no batch at
// all (limit L5 of docs/simfleet-aws-plan.md), so the plan's batch step
// had nothing to run.
//
// The flags are `halite-hub run`'s, spelt the same and checked the same
// way, so that a batch measured here is one an operator can write.
// `--batch-safe-limit` is left out on purpose: a simulated node always
// succeeds, so a safe limit could only ever be tripped by a node that did
// not answer, and that is a timeout, which -batch-timeout already sets.
type batchFlags struct {
	spec    *string
	wait    *time.Duration
	timeout *time.Duration
}

func addBatchFlags(fs *flag.FlagSet) batchFlags {
	return batchFlags{
		spec:    fs.String("batch", "", "deliver the job a slice at a time, as `halite-hub run --batch` does: a count, or a percentage such as 10%"),
		wait:    fs.Duration("batch-wait", 0, "with -batch, how long the hub waits between one slice's returns and the next slice"),
		timeout: fs.Duration("batch-timeout", 0, "with -batch, how long the hub waits for one slice's returns (0: the hub's default, 5m)"),
	}
}

func (b batchFlags) on() bool { return *b.spec != "" }

// check refuses what `halite-hub run` refuses, before anything connects:
// a wait or timeout that is not a whole number of seconds would be
// truncated on the wire, and the run would measure a batch nobody asked
// for.
func (b batchFlags) check() error {
	if !b.on() && (*b.wait != 0 || *b.timeout != 0) {
		return errors.New("-batch-wait and -batch-timeout only mean something with -batch")
	}
	for _, d := range []struct {
		name string
		v    time.Duration
	}{{"-batch-wait", *b.wait}, {"-batch-timeout", *b.timeout}} {
		if d.v < 0 {
			return fmt.Errorf("%s %s is negative", d.name, d.v)
		}
		if d.v%time.Second != 0 {
			return fmt.Errorf("%s %s is not a whole number of seconds, which is what the hub is sent", d.name, d.v)
		}
	}
	return nil
}

// request is the job simfleet submits, batched or not.
func (b batchFlags) request(target, fun string) transport.SubmitRequest {
	return transport.SubmitRequest{
		Target:           target,
		Fun:              fun,
		Batch:            *b.spec,
		BatchWaitSeconds: int(*b.wait / time.Second),
		BatchTimeoutSecs: int(*b.timeout / time.Second),
	}
}

// patience is how long `run` keeps waiting for returns after the last one
// arrived, for a batched job. An unbatched job is waited for against a
// fixed deadline from its SPEC 30 row; a batched one has no row, and its
// length is the number of slices times however long each takes, which
// is the thing being measured. So the wait is from the last progress
// instead: a slice that timed out on the hub, plus the wait before the
// next, plus a margin, with no return arriving, is a batch that has
// stopped.
func (b batchFlags) patience() time.Duration {
	t := *b.timeout
	if t == 0 {
		t = 5 * time.Minute // hub.DefaultBatchTimeout
	}
	return t + *b.wait + 30*time.Second
}

// batchResult is what a batched job looked like from the nodes.
//
// The hub reports the size it resolved; the nodes report when each one
// received the job and when its return was accepted. From those two the
// result answers the plan's two questions for the batch step: was more
// than one slice ever in flight, and how long does the hub take from one
// slice's last return to the next slice's first delivery.
type batchResult struct {
	Size int `json:"size"`
	// Slices is how many the hub should have needed for the nodes that
	// were expected: ceil(expected / size).
	Slices int `json:"slices"`
	// PeakInFlight is the most nodes that had received the job and not
	// yet had their return accepted, at any one moment. One slice at a
	// time means at most Size. Measured, not taken from the hub: the
	// point is to check the hub, so the hub's own account of what it did
	// is not evidence.
	PeakInFlight int `json:"peak_in_flight"`
	// Gaps is, for each slice after the first, the time from the last
	// return of the slice before it to the first arrival of this one:
	// -batch-wait, plus however long the hub took to notice the slice
	// had finished and deliver the next. It is computed by putting the
	// nodes in the order they received the job and cutting that into
	// slices of Size, which is only right while PeakInFlight <= Size; it
	// is left out otherwise, because a gap between slices that overlap
	// does not mean anything.
	Gaps *latency `json:"gaps,omitempty"`
}

// batchOf works a batchResult out of arrival and return times (unix
// nanos, 0 = never), indexed alike.
//
// Across several drivers the times are on different clocks, so a peak
// or a gap that is within the hosts' recorded clock offsets of the limit
// says nothing either way. A simulated node answers in well under a
// millisecond, which is what makes slices separable at all: a real
// fleet's slices would be just as separate, but each would take as long
// as its slowest node.
func batchOf(size, expected int, received, returned []int64) *batchResult {
	if size < 1 {
		return nil
	}
	b := &batchResult{Size: size, Slices: (expected + size - 1) / size}

	type event struct {
		at    int64
		delta int
	}
	var events []event
	type node struct{ recv, ret int64 }
	var nodes []node
	// Only nodes whose return was accepted. One that received the job
	// and never answered has no end to its time in flight, and counting
	// it as in flight for ever would make every later slice look like an
	// overlap; it is already counted, in the returns that are missing.
	for i, r := range received {
		if r == 0 || returned[i] == 0 {
			continue
		}
		events = append(events, event{r, +1}, event{returned[i], -1})
		nodes = append(nodes, node{r, returned[i]})
	}
	// A return and an arrival at the same instant: the return first, so
	// that a slice handed over within one clock tick is not counted as
	// two slices at once. A peak above Size has to be a real overlap.
	sort.Slice(events, func(i, j int) bool {
		if events[i].at != events[j].at {
			return events[i].at < events[j].at
		}
		return events[i].delta < events[j].delta
	})
	cur := 0
	for _, e := range events {
		cur += e.delta
		b.PeakInFlight = max(b.PeakInFlight, cur)
	}
	if b.PeakInFlight > size {
		return b
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].recv < nodes[j].recv })
	var gaps []time.Duration
	for start := size; start < len(nodes); start += size {
		var lastRet int64
		for _, n := range nodes[start-size : start] {
			lastRet = max(lastRet, n.ret)
		}
		gaps = append(gaps, time.Duration(nodes[start].recv-lastRet))
	}
	if len(gaps) > 0 {
		sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
		d := dist(gaps)
		b.Gaps = &d
	}
	return b
}
