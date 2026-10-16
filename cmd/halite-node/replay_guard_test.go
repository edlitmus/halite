package main

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// replayNode is signedNode with configuration of the test's own, and
// with evidence off: these tests send thousands of jobs, and the chain's
// synced writes would be most of their run time without being what they
// test.
func replayNode(t *testing.T, require string) (*node, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := jobsign.GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	line, err := jobsign.FormatSignerKey("ops", &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	n := nodeForEvidence(t, "evidence: false\nrequire_job_signature: "+require+
		"\njob_signer_keys:\n  - '"+line+"'\n")
	required, err := parseSignatureRequirement(n.cfg.Get("require_job_signature"))
	if err != nil {
		t.Fatal(err)
	}
	n.signatureRequired = required
	n.signerKeyList = n.signerKeys()
	n.grains = value.NewMap(0)
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {})
	return n, key
}

// replayOf is what a hub replaying a signed job sends: the same signed
// message under a nonce it has just made up, which the signature does not
// cover.
func replayOf(t *testing.T, msg transport.Message) transport.Message {
	t.Helper()
	nonce, err := job.Nonce()
	if err != nil {
		t.Fatal(err)
	}
	msg.Nonce = nonce
	return msg
}

func wantReplayRefusal(t *testing.T, n *node, what string) {
	t.Helper()
	if n.executor.Depth() != 0 {
		t.Fatalf("%s: the replay was queued", what)
	}
	refusal := lastRefusal(t, n)
	if !strings.Contains(refusal, "already accepted the signed job") && !strings.Contains(refusal, "already run") {
		t.Errorf("%s: the refusal is not a replay's: %s", what, refusal)
	}
}

func drainQueue(n *node) {
	for n.executor.Depth() > 0 {
		<-n.executor.queue
	}
}

// A replay of a signed job with a fresh nonce is refused.
func TestASignedJobReplayedWithAFreshNonceIsRefused(t *testing.T) {
	n, key := replayNode(t, "true")
	msg := signedMessage(t, key, "20261015T090000000001", "test.ping", "web1.example", "glob", nil)
	n.acceptJob(msg)
	if n.executor.Depth() != 1 {
		t.Fatalf("the signed job was refused: %s", lastRefusal(t, n))
	}
	drainQueue(n)
	n.acceptJob(replayOf(t, msg))
	wantReplayRefusal(t, n, "a fresh nonce")
}

// Junk cannot push a signed job out of what this node remembers.
//
// Two ways junk used to do it. On a node that requires a signature on
// every job, unsigned junk was refused -- after it had taken two places in
// the replay cache, so a few thousand of them evicted anything. On a node
// that requires one only for some functions, unsigned read-only junk is
// legitimately accepted, and the cache is bounded by count, so the same
// thing happened with jobs that ran. Both were reproduced.
func TestJunkCannotEvictASignedJob(t *testing.T) {
	flood := 2*job.DefaultGuardSize + 16
	for _, require := range []string{"true", "[state]"} {
		t.Run(require, func(t *testing.T) {
			n, key := replayNode(t, require)
			fun := "test.ping"
			if require != "true" {
				fun = "state.apply"
			}
			msg := signedMessage(t, key, "20261015T090000000002", fun, "web1.example", "glob", nil)
			n.acceptJob(msg)
			if n.executor.Depth() != 1 {
				t.Fatalf("the signed job was refused: %s", lastRefusal(t, n))
			}
			drainQueue(n)

			admitted := 0
			for i := 0; i < flood; i++ {
				// Straight to Offer: acceptJob would post a refusal
				// for each, and the refusals are not what is tested.
				junk := runnableJob(t, fmt.Sprintf("20261015T1%011d", i))
				if n.executor.Offer(junk) == nil {
					admitted++
				}
				drainQueue(n)
			}
			if require == "true" {
				if admitted != 0 {
					t.Fatalf("%d unsigned jobs were accepted on a node that requires signatures", admitted)
				}
				if n.executor.guard.Len() != 2 {
					t.Errorf("refused junk took places in the replay cache: it holds %d keys, want the signed job's 2",
						n.executor.guard.Len())
				}
			} else {
				// The junk really was accepted, and really did push the
				// signed job out of the in-memory cache; what refuses the
				// replay below is the signed record and nothing else.
				if admitted != flood {
					t.Fatalf("only %d of %d read-only jobs were accepted", admitted, flood)
				}
				probe := &job.Job{JID: job.ID(msg.JID), Nonce: replayOf(t, msg).Nonce, Fun: fun}
				if err := n.executor.guard.Check(probe); err != nil {
					t.Fatalf("the in-memory cache still holds the signed jid, so this test proves nothing: %v", err)
				}
			}

			n.acceptJob(replayOf(t, msg))
			wantReplayRefusal(t, n, fmt.Sprintf("after %d junk jobs", flood))
		})
	}
}

// A restart does not make a node forget a signed job.
func TestASignedJobReplayedAfterARestartIsRefused(t *testing.T) {
	n, key := replayNode(t, "true")
	msg := signedMessage(t, key, "20261015T090000000003", "test.ping", "web1.example", "glob", nil)
	n.acceptJob(msg)
	if n.executor.Depth() != 1 {
		t.Fatalf("the signed job was refused: %s", lastRefusal(t, n))
	}

	// What a restart leaves: the configuration and the state directory,
	// and a new executor with an empty in-memory cache.
	n.executor = newExecutor(n, 4, func(*job.Return) {})
	if n.executor.guard.Len() != 0 {
		t.Fatal("the new executor's cache is not empty, so this is not a restart")
	}
	n.acceptJob(replayOf(t, msg))
	wantReplayRefusal(t, n, "after a restart")

	// The very same message, nonce and all, likewise.
	n.executor = newExecutor(n, 4, func(*job.Return) {})
	n.acceptJob(msg)
	wantReplayRefusal(t, n, "the same message after a restart")
}

// A job refused for its signature is refused for that, not as a replay,
// and does not stop the properly signed job with its jid from running:
// the record is written only for a signature that verified.
func TestARefusedSignatureDoesNotBurnTheJid(t *testing.T) {
	n, key := replayNode(t, "true")
	msg := signedMessage(t, key, "20261015T090000000004", "test.ping", "web1.example", "glob", nil)
	forged := msg
	forged.Signature = ""
	n.acceptJob(forged)
	if refusal := lastRefusal(t, n); strings.Contains(refusal, "already") {
		t.Fatalf("the unsigned job was refused as a replay: %s", refusal)
	}
	n.acceptJob(msg)
	if n.executor.Depth() != 1 {
		t.Fatalf("the signed job was refused after a forged one with its jid: %s", lastRefusal(t, n))
	}
	var refusal *job.Refusal
	if err := n.executor.Offer(&job.Job{JID: "nonsense"}); !errors.As(err, &refusal) {
		t.Errorf("a malformed job gave %v", err)
	}
}
