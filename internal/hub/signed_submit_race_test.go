package hub

import (
	"sync"
	"testing"
	"time"
)

// Two submissions of one signed job do not both succeed.
//
// `identify` checks that no job has the identifier, and the job is recorded
// some lines later, so two concurrent submissions of the same signed job could
// both pass the check, both be recorded -- the second replacing the first --
// and both be answered with success. The node's replay guard (SPEC 6.3) kept
// the second from running, so nothing ran twice; two callers were told their
// submission was accepted, and one record stood for both. DIVERGENCE 5.531.
//
// The two are held at the gap between check and record until both are there,
// or half a second has passed, which is what a loaded hub's scheduler
// produces by chance and a test cannot leave to chance. With the check and the
// record under one lock the second never arrives while the first waits, the
// wait times out, and the second then finds the first's record.
func TestTwoSubmissionsOfOneSignedJobDoNotBothSucceed(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	jid := l.server.clock().Next()

	var mu sync.Mutex
	arrived := 0
	both := make(chan struct{})
	l.server.signedAfterIdentify = func() {
		mu.Lock()
		arrived++
		if arrived == 2 {
			close(both)
		}
		mu.Unlock()
		select {
		case <-both:
		case <-time.After(500 * time.Millisecond):
		}
	}

	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := l.server.Dispatch(Submission{
				Fun: "test.ping", Target: "web1.example", TargetKind: "L",
				JID: jid, Expires: time.Now().Add(time.Hour), Signature: "signature",
			})
			results <- err
		}()
	}
	ok := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("%d of two submissions of the same signed job were accepted, want exactly one", ok)
	}
}
