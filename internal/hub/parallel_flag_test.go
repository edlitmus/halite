package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// receiveJob connects nodeID and hands back the first job message it is
// sent, which is where a node reads `--parallel` from.
func receiveJob(t *testing.T, client *transport.Client, nodeID string) (<-chan transport.Message, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	jobs := make(chan transport.Message, 1)
	ready := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Subscribe(ctx, transport.SubscribeRequest{NodeID: nodeID, Grains: json.RawMessage(`{}`)},
			func(msg transport.Message) error {
				switch msg.T {
				case transport.MsgPing:
					select {
					case <-ready:
					default:
						close(ready)
					}
				case transport.MsgJob:
					select {
					case jobs <- msg:
					default:
					}
				}
				return nil
			})
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("%s never connected", nodeID)
	}
	return jobs, func() { cancel(); <-stopped }
}

func wantParallel(t *testing.T, jobs <-chan transport.Message, want bool, when string) {
	t.Helper()
	select {
	case msg := <-jobs:
		if msg.Parallel != want {
			t.Errorf("%s: the job reached the node with parallel=%v, want %v", when, msg.Parallel, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no job reached the node", when)
	}
}

// `--parallel` reaches the node, live and from the record a queued job is
// delivered from (SPEC 9.6). A flag that stopped at the hub would make the
// operator's request silently a serial one.
func TestTheParallelFlagReachesTheNode(t *testing.T) {
	l := newLab(t).withJobs(t)
	client := l.enrolled(t, "web1.example")
	op := l.operator(t, "ed")

	jobs, stop := receiveJob(t, client, "web1.example")
	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example", Fun: "test.ping", Parallel: true,
	}); err != nil {
		t.Fatal(err)
	}
	wantParallel(t, jobs, true, "dispatched live")
	stop()

	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example", Fun: "test.ping", Parallel: true, Offline: "queue",
	}); err != nil {
		t.Fatal(err)
	}
	jobs, stop = receiveJob(t, client, "web1.example")
	defer stop()
	wantParallel(t, jobs, true, "queued while the node was away")
}

// Without the flag, nothing says parallel: the default stays serial.
func TestAJobWithoutTheFlagIsNotParallel(t *testing.T) {
	l := newLab(t).withJobs(t)
	client := l.enrolled(t, "web1.example")
	op := l.operator(t, "ed")
	jobs, stop := receiveJob(t, client, "web1.example")
	defer stop()

	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example", Fun: "test.ping",
	}); err != nil {
		t.Fatal(err)
	}
	wantParallel(t, jobs, false, "dispatched without --parallel")
}
