package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/transport"
)

// **A job a node cannot read is refused when it is submitted** (issue
// 321, M-11). A node reads its stream with a scanner bounded at
// MaxSubscribeLine; a longer job ended that node's stream and the job,
// already recorded as dispatched to it, never arrived. On main the
// submission below was accepted. One just inside the bound arrives
// whole, so the two checks agree on where the line is.
func TestAJobTooLongForANodeIsRefusedAndOneThatFitsArrives(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")

	var mu sync.Mutex
	got := map[string]int{} // fun -> length of its blob
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	ended := make(chan error, 1)
	go func() {
		ended <- web1.Subscribe(ctx, transport.SubscribeRequest{NodeID: "web1.example"}, func(msg transport.Message) error {
			switch msg.T {
			case transport.MsgPing:
				select {
				case <-ready:
				default:
					close(ready)
				}
			case transport.MsgJob:
				blob, _ := msg.Kwarg["blob"].(string)
				mu.Lock()
				got[msg.Fun] = len(blob)
				mu.Unlock()
			}
			return nil
		})
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("the node never connected")
	}

	op := l.operator(t, "ed")
	_, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example", Fun: "test.too_long",
		Kwarg: map[string]any{"blob": strings.Repeat("x", transport.MaxSubscribeLine)},
	})
	if err == nil || !strings.Contains(err.Error(), "on the wire") {
		t.Fatalf("a job longer than a node reads was accepted: %v", err)
	}

	fits := transport.MaxSubscribeLine - 4096
	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example", Fun: "test.fits",
		Kwarg: map[string]any{"blob": strings.Repeat("x", fits)},
	}); err != nil {
		t.Fatalf("a job inside the bound was refused: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n, ok := got["test.fits"]
		mu.Unlock()
		if ok {
			if n != fits {
				t.Errorf("the node got a blob of %d bytes, sent %d", n, fits)
			}
			break
		}
		select {
		case err := <-ended:
			t.Fatalf("the node's stream ended instead: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the job inside the bound never reached the node")
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := got["test.too_long"]; ok {
		t.Error("the refused job reached the node")
	}
}

// **An event too long for the stream does not end it.** A node may post
// 1 MiB of event data, which with the hub's fields is a line longer than
// any reader of `/v1/events` takes. On main the reader below got
// ErrTooLong at that event, and resuming from the last offset met it
// again. Now it gets a stand-in naming the event, and the next event.
func TestAnEventTooLongForTheStreamIsSentAsAStandIn(t *testing.T) {
	l := newLab(t).withEvents(t)
	node := l.enrolled(t, "web1.example")
	data, _ := json.Marshal(map[string]any{"blob": strings.Repeat("y", transport.MaxGrainsPayload-64)})
	if _, err := node.SendEvent(context.Background(), transport.EventRequest{Tag: "big", Data: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := node.SendEvent(context.Background(), transport.EventRequest{Tag: "after", Data: json.RawMessage(`{"n":1}`)}); err != nil {
		t.Fatal(err)
	}

	events := readEvents(t, l.operator(t, "ed"), []string{"halite/node/web1.example/*"})
	var big, after *eventbus.Event
	for i := range events {
		switch events[i].Tag {
		case "halite/node/web1.example/big":
			big = &events[i]
		case "halite/node/web1.example/after":
			after = &events[i]
		}
	}
	if big == nil || after == nil {
		t.Fatalf("the stream held %d events, missing the big one or the one after it", len(events))
	}
	if big.Data["_oversized"] != true || big.Offset == "" || big.Node != "web1.example" {
		t.Errorf("the stand-in is %+v", big)
	}
	if n, _ := strconv.Atoi(fmt.Sprint(big.Data["_bytes"])); n <= transport.MaxSubscribeLine {
		t.Errorf("the stand-in says the event was %v bytes", big.Data["_bytes"])
	}

	// The bus itself keeps the event whole: the stand-in is the stream's.
	raw, _, err := l.server.Events.Read(eventbus.Earliest, []string{"halite/node/web1.example/big"}, 10)
	if err != nil || len(raw) != 1 {
		t.Fatalf("reading the bus: %v %d", err, len(raw))
	}
	if blob, _ := raw[0].Data["blob"].(string); len(blob) != transport.MaxGrainsPayload-64 {
		t.Errorf("the bus holds a blob of %d bytes", len(blob))
	}
}
