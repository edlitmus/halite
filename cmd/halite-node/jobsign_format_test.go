package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/transport"
)

// signedMessageExpiring is signedMessage with the expiry chosen by the
// test, signed straight over the digest so that an expiry Sign itself
// would refuse -- none at all -- can still reach the node, as it could
// from a signer that is not this program.
func signedMessageExpiring(t *testing.T, key *ecdsa.PrivateKey, jid string, kwargs map[string]any, expires time.Time) transport.Message {
	t.Helper()
	nonce, err := job.Nonce()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jobsign.WireValues(kwargs)
	if err != nil {
		t.Fatal(err)
	}
	j := &job.Job{JID: job.ID(jid), Fun: "test.ping", Kwarg: wire, Nonce: nonce,
		Expires: expires, Target: "web1.example", TargetKind: "glob"}
	digest := jobsign.Digest(job.SigningPayload(j))
	der, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	msg := transport.Message{
		T: transport.MsgJob, JID: jid, Fun: "test.ping", Kwarg: wire, Nonce: nonce,
		Target: "web1.example", TargetKind: "glob",
		Signature: base64.StdEncoding.EncodeToString(der),
	}
	if !expires.IsZero() {
		msg.Expires = expires.Format(time.RFC3339Nano)
	}
	return msg
}

// A hub that turns a signed dry run into a string is refused.
//
// The node honours a dry run only as a boolean, so before v2 this ran the
// job for real under a signature that verified. DIVERGENCE 5.290.
func TestASignedDryRunTamperedToAStringIsRefused(t *testing.T) {
	n, key := signedNode(t, "true")
	msg := signedMessage(t, key, "20261015T080000000001", "test.ping", "web1.example", "glob",
		map[string]any{"test": true})
	msg.Kwarg = map[string]any{"test": "true"}
	n.acceptJob(msg)
	if n.executor.Depth() != 0 {
		t.Fatal("a signed `test: true` changed to the string \"true\" was accepted")
	}
	if refusal := lastRefusal(t, n); !strings.Contains(refusal, "does not verify") {
		t.Errorf("the refusal is not the signature's: %s", refusal)
	}
}

// A node refuses a signed job with no expiry, and one whose expiry is
// further off than it allows; one inside the bound is accepted.
func TestASignedJobsExpiryMustBePresentAndBounded(t *testing.T) {
	cases := []struct {
		name    string
		expires time.Time
		accept  bool
		says    string
	}{
		{"no expiry", time.Time{}, false, "must carry an expiry"},
		{"a year away", time.Now().Add(365 * 24 * time.Hour), false, "job_signature_max_ttl"},
		{"just past the bound", time.Now().Add(DefaultSignatureMaxTTL + signatureExpirySkew + time.Minute), false, "job_signature_max_ttl"},
		{"the default TTL", time.Now().Add(job.DefaultTTL), true, ""},
		{"at the bound", time.Now().Add(DefaultSignatureMaxTTL), true, ""},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, key := signedNode(t, "true")
			jid := "20261015T08000000001" + string(rune('0'+i))
			n.acceptJob(signedMessageExpiring(t, key, jid, nil, c.expires))
			if c.accept {
				if n.executor.Depth() != 1 {
					t.Fatalf("refused: %s", lastRefusal(t, n))
				}
				return
			}
			if n.executor.Depth() != 0 {
				t.Fatal("accepted")
			}
			if refusal := lastRefusal(t, n); !strings.Contains(refusal, c.says) {
				t.Errorf("the refusal does not say %q: %s", c.says, refusal)
			}
		})
	}
}

// The bound is the node's to set.
func TestTheSignedExpiryBoundIsConfigurable(t *testing.T) {
	n, key := signedNode(t, "true")
	// signedNode's configuration plus the setting: rebuilt the same way
	// so that the key is read from configuration, not poked into a
	// field.
	line, err := jobsign.FormatSignerKey("ops", &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	cfgNode := nodeForEvidence(t, "require_job_signature: true\njob_signature_max_ttl: 5m\njob_signer_keys:\n  - '"+line+"'\n")
	n.cfg = cfgNode.cfg
	n.acceptJob(signedMessageExpiring(t, key, "20261015T080000000101", nil, time.Now().Add(job.DefaultTTL)))
	if n.executor.Depth() != 0 {
		t.Fatal("a job signed for fifteen minutes was accepted by a node that allows five")
	}
	if refusal := lastRefusal(t, n); !strings.Contains(refusal, "5m0s") {
		t.Errorf("the refusal does not name the configured bound: %s", refusal)
	}
}

// A `test` that is not a boolean is refused, not read as "run it".
func TestANonBooleanTestIsRefused(t *testing.T) {
	for _, v := range []any{"true", "false", "yes", 1, []any{true}} {
		n := nodeForEvidence(t, "")
		n.refusals = make(chan *job.Return, 4)
		n.executor = newExecutor(n, 4, func(*job.Return) {})
		msg := jobMessage(t, "20261015T080000000201")
		msg.Kwarg = map[string]any{"test": v}
		n.acceptJob(msg)
		if n.executor.Depth() != 0 {
			t.Errorf("a job with `test: %#v` was accepted", v)
			continue
		}
		if refusal := lastRefusal(t, n); !strings.Contains(refusal, "dry run is asked for with the boolean") {
			t.Errorf("the refusal for `test: %#v` does not say why: %s", v, refusal)
		}
	}
}

// The paths that do not come through Offer -- a schedule, agentless mode
// -- reach executeJob, and it refuses too rather than running.
func TestExecuteJobRefusesANonBooleanTest(t *testing.T) {
	n := nodeForEvidence(t, "")
	ret := n.executeJob(&job.Job{JID: job.ID("20261015T3"), Fun: "test.ping",
		Kwarg: map[string]any{"test": "true"}})
	if ret.Success {
		t.Fatal("a job with `test: \"true\"` ran")
	}
	if !strings.Contains(string(ret.Return), "dry run is asked for with the boolean") {
		t.Errorf("the failure does not say why: %s", ret.Return)
	}
	// And the boolean still works, both ways.
	for _, v := range []bool{true, false} {
		ret := n.executeJob(&job.Job{JID: job.ID("20261015T4"), Fun: "test.ping",
			Kwarg: map[string]any{"test": v}})
		if !ret.Success {
			t.Errorf("`test: %v` failed: %s", v, ret.Return)
		}
	}
}
