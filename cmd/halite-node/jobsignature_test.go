package main

import (
	"crypto/ecdsa"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/nodeevidence"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// signedNode builds a node that requires signatures and trusts one key,
// and hands back the key to sign with.
func signedNode(t *testing.T, require string) (*node, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := jobsign.GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	line, err := jobsign.FormatSignerKey("ops", &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	n := nodeForEvidence(t, "require_job_signature: "+require+"\njob_signer_keys:\n  - '"+line+"'\n")
	required, err := parseSignatureRequirement(n.cfg.Get("require_job_signature"))
	if err != nil {
		t.Fatalf("the requirement does not parse: %v", err)
	}
	n.signatureRequired = required
	n.signerKeyList = n.signerKeys()
	n.grains = value.NewMap(0)
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {})
	return n, key
}

// signedMessage builds the message a hub would deliver for a job the
// operator signed, through the same functions the hub and the operator
// use, so that a disagreement between them fails here.
func signedMessage(t *testing.T, key *ecdsa.PrivateKey, jid, fun, target, kind string, kwargs map[string]any) transport.Message {
	t.Helper()
	nonce, err := job.Nonce()
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(job.DefaultTTL)
	wire, err := jobsign.WireValues(kwargs)
	if err != nil {
		t.Fatal(err)
	}
	j := &job.Job{
		JID:        job.ID(jid),
		Fun:        fun,
		Kwarg:      wire,
		Nonce:      nonce,
		Expires:    expires,
		Target:     target,
		TargetKind: kind,
	}
	signature, err := jobsign.Sign(key, job.SigningPayload(j))
	if err != nil {
		t.Fatal(err)
	}
	return transport.Message{
		T:          transport.MsgJob,
		JID:        jid,
		Fun:        fun,
		Kwarg:      wire,
		Nonce:      nonce,
		Expires:    expires.Format(time.RFC3339Nano),
		Target:     target,
		TargetKind: kind,
		Signature:  signature,
		Submitter:  "cert:CN=ops.example",
	}
}

func lastRefusal(t *testing.T, n *node) string {
	t.Helper()
	select {
	case ret := <-n.refusals:
		return string(ret.Return)
	default:
		t.Fatal("no refusal was posted")
		return ""
	}
}

func TestASignedJobIsAcceptedAndItsSignerRecorded(t *testing.T) {
	n, key := signedNode(t, "true")
	n.acceptJob(signedMessage(t, key, "20260923T080000000001", "test.ping", "web1.example", "glob", nil))

	if n.executor.Depth() != 1 {
		t.Fatalf("a properly signed job was not queued: %s", lastRefusal(t, n))
	}
	recs := wantRecords(t, n, 1)
	if recs[0].Kind != nodeevidence.KindJobAccepted {
		t.Fatalf("the record is %q", recs[0].Kind)
	}
	// `verified_signer`, not `claimed_`: this node checked it.
	if recs[0].Detail["verified_signer"] != "ops" {
		t.Errorf("the record does not name the key that authorised the job: %v", recs[0].Detail)
	}
}

func TestAnUnsignedJobIsRefusedWhenOneIsRequired(t *testing.T) {
	n, _ := signedNode(t, "true")
	n.acceptJob(jobMessage(t, "20260923T080000000002"))

	if n.executor.Depth() != 0 {
		t.Fatal("an unsigned job was queued on a node that requires signatures")
	}
	if refusal := lastRefusal(t, n); !strings.Contains(refusal, "carries no signature") {
		t.Errorf("the refusal does not say what was missing: %s", refusal)
	}
	recs := wantRecords(t, n, 1)
	if recs[0].Kind != nodeevidence.KindJobRefused {
		t.Errorf("the refusal was not recorded: %v", kindsOf(recs))
	}
}

// The hub relays the signature and cannot change what it covers.
//
// Each of these is a hub altering one field of a signed job in flight,
// which is the attack the mechanism exists for.
func TestAHubCannotChangeASignedJob(t *testing.T) {
	changes := map[string]func(m *transport.Message){
		"the function":   func(m *transport.Message) { m.Fun = "cmd.run" },
		"an argument":    func(m *transport.Message) { m.Arg = []string{"rm -rf /"} },
		"the target":     func(m *transport.Message) { m.Target = "*" },
		"the expiry":     func(m *transport.Message) { m.Expires = time.Now().Add(72 * time.Hour).Format(time.RFC3339Nano) },
		"an environment": func(m *transport.Message) { m.Env = "production" },
		"the signature":  func(m *transport.Message) { m.Signature = "" },
	}
	for what, change := range changes {
		t.Run(what, func(t *testing.T) {
			n, key := signedNode(t, "true")
			msg := signedMessage(t, key, "20260923T080000000003", "test.ping", "web1.example", "glob", nil)
			change(&msg)
			n.acceptJob(msg)
			if n.executor.Depth() != 0 {
				t.Errorf("a hub changed %s of a signed job and the node ran it", what)
			}
		})
	}
}

// A signature for another machine does not authorise this one.
//
// Without this check the signature says what may be run and not where,
// so a hub that has been taken over could take a legitimate signed job
// for one host and deliver it to the whole estate.
func TestASignatureForAnotherNodeIsRefused(t *testing.T) {
	n, key := signedNode(t, "true")
	n.acceptJob(signedMessage(t, key, "20260923T080000000004", "test.ping", "db*", "glob", nil))

	if n.executor.Depth() != 0 {
		t.Fatal("this node ran a job signed for a target it does not match")
	}
	refusal := lastRefusal(t, n)
	if !strings.Contains(refusal, "does not match") {
		t.Errorf("the refusal does not say the target did not match: %s", refusal)
	}
	if !strings.Contains(refusal, n.nodeID) {
		t.Errorf("the refusal does not name this node: %s", refusal)
	}
}

// A target this node cannot evaluate about itself is refused rather than
// waved through, because "I could not check" must not mean "accepted" in
// the one control whose purpose is to distrust the hub.
func TestANodegroupTargetCannotBeCheckedAndIsRefused(t *testing.T) {
	n, key := signedNode(t, "true")
	n.acceptJob(signedMessage(t, key, "20260923T080000000005", "test.ping", "webservers", "nodegroup", nil))

	if n.executor.Depth() != 0 {
		t.Fatal("a nodegroup-targeted signed job was accepted, which this node cannot check")
	}
	if refusal := lastRefusal(t, n); !strings.Contains(refusal, "cannot check") {
		t.Errorf("the refusal does not explain what could not be checked: %s", refusal)
	}
}

// The grain matcher works here too, so an estate can sign for a class of
// machine rather than for a name.
func TestASignedGrainTargetIsCheckedAgainstThisNodesGrains(t *testing.T) {
	n, key := signedNode(t, "true")
	n.grains.Set("os", "FreeBSD")

	n.acceptJob(signedMessage(t, key, "20260923T080000000006", "test.ping", "os:FreeBSD", "grain", nil))
	if n.executor.Depth() != 1 {
		t.Fatalf("a job signed for this node's own grain was refused: %s", lastRefusal(t, n))
	}

	// Checked by the refusal rather than by the queue depth alone. The
	// depth after two jobs is 1 whether the first was taken and the
	// second refused or the other way round, so a depth assertion on its
	// own would pass with the two results swapped.
	n.acceptJob(signedMessage(t, key, "20260923T080000000007", "test.ping", "os:Windows", "grain", nil))
	if n.executor.Depth() != 1 {
		t.Fatalf("a job signed for a grain this node does not have was accepted: depth %d",
			n.executor.Depth())
	}
	refusal := lastRefusal(t, n)
	if !strings.Contains(refusal, "os:Windows") {
		t.Errorf("the refusal is not about the second job: %s", refusal)
	}
}

// `--test` becomes a keyword argument on the wire, and the signature has
// to cover the same arguments the node receives.
//
// The two sides build the kwargs through one function for exactly this
// case; if they ever diverge, every signed dry run is refused as unsigned
// and the message says nothing about `test`.
func TestASignedTestJobVerifies(t *testing.T) {
	n, key := signedNode(t, "true")
	msg := signedMessage(t, key, "20260923T080000000008", "state.apply", "web1.example", "glob",
		map[string]any{"test": true})

	n.acceptJob(msg)
	if n.executor.Depth() != 1 {
		t.Fatalf("a signed dry run was refused: %s", lastRefusal(t, n))
	}
}

// A structured argument survives the journey from what the operator
// typed to what the node decodes.
func TestASignedStructuredArgumentVerifies(t *testing.T) {
	n, key := signedNode(t, "true")
	msg := signedMessage(t, key, "20260923T080000000009", "state.apply", "web1.example", "glob",
		map[string]any{"pillar": map[string]any{"b": 2, "a": 1}, "queue": true})

	n.acceptJob(msg)
	if n.executor.Depth() != 1 {
		t.Fatalf("a signed job with a mapping argument was refused: %s", lastRefusal(t, n))
	}
}

// The per-class requirement of SPEC 25.6: read-only functions pass
// unsigned and the dangerous ones do not.
func TestTheRecommendedRequirementLetsReadOnlyFunctionsThrough(t *testing.T) {
	n, _ := signedNode(t, "[arbitrary_code, state]")

	n.acceptJob(jobMessage(t, "20260923T080000000010"))
	if n.executor.Depth() != 1 {
		t.Fatalf("an unsigned test.ping was refused under the recommended requirement: %s",
			lastRefusal(t, n))
	}

	unsignedShell := jobMessage(t, "20260923T080000000011")
	unsignedShell.Fun = "cmd.run"
	n.acceptJob(unsignedShell)
	if n.executor.Depth() != 1 {
		t.Error("an unsigned cmd.run was accepted under a requirement that names arbitrary_code")
	}

	unsignedApply := jobMessage(t, "20260923T080000000012")
	unsignedApply.Fun = "state.apply"
	n.acceptJob(unsignedApply)
	if n.executor.Depth() != 1 {
		t.Error("an unsigned state.apply was accepted under a requirement that names state")
	}

	// And rendering is not applying: SPEC 25.6's recommendation is about
	// state application, and a node that cannot be asked what it would
	// do is a node nobody can debug.
	unsignedShow := jobMessage(t, "20260923T080000000013")
	unsignedShow.Fun = "state.show_highstate"
	n.acceptJob(unsignedShow)
	if n.executor.Depth() != 2 {
		t.Errorf("an unsigned state.show_highstate was refused: %s", lastRefusal(t, n))
	}
}

func TestRequiringSignaturesWithNoKeysRefusesAndSaysSo(t *testing.T) {
	n := nodeForEvidence(t, "require_job_signature: true\n")
	required, err := parseSignatureRequirement(n.cfg.Get("require_job_signature"))
	if err != nil {
		t.Fatal(err)
	}
	n.signatureRequired = required
	n.signerKeyList = n.signerKeys()
	n.refusals = make(chan *job.Return, 4)
	n.executor = newExecutor(n, 4, func(*job.Return) {})

	n.acceptJob(jobMessage(t, "20260923T080000000014"))
	if n.executor.Depth() != 0 {
		t.Fatal("a node with no signer keys accepted a job it must not verify")
	}
	if refusal := lastRefusal(t, n); !strings.Contains(refusal, "job_signer_keys") {
		t.Errorf("the refusal does not name the setting that is empty: %s", refusal)
	}
}

func TestTheRequirementSettingIsParsedOrRefused(t *testing.T) {
	cases := []struct {
		raw     any
		all     bool
		classes []string
		bad     string
	}{
		{raw: true, all: true},
		{raw: false},
		{raw: "true", all: true},
		{raw: []any{"arbitrary_code", "state"}, classes: []string{"arbitrary_code", "state"}},
		{raw: []any{"ARBITRARY_CODE"}, classes: []string{"arbitrary_code"}},
		{raw: []any{"everything"}, bad: "function class"},
		{raw: 7, bad: "true, false, or a list"},
		{raw: []any{3}, bad: "not a function class"},
	}
	for _, c := range cases {
		got, err := parseSignatureRequirement(c.raw, true)
		if c.bad != "" {
			if err == nil {
				t.Errorf("%v was accepted", c.raw)
			} else if !strings.Contains(err.Error(), c.bad) {
				t.Errorf("%v: the message is %q, expected it to mention %q", c.raw, err, c.bad)
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", c.raw, err)
			continue
		}
		if got.all != c.all {
			t.Errorf("%v: all = %v", c.raw, got.all)
		}
		for _, class := range c.classes {
			if !got.classes[class] {
				t.Errorf("%v: %s is not required", c.raw, class)
			}
		}
		if len(got.classes) != len(c.classes) {
			t.Errorf("%v: requires %v", c.raw, got)
		}
	}
}

// A function this build does not ship needs a signature whenever
// anything does. It will fail with "no such function" a moment later, and
// an unknown name must not be the way past the control.
func TestAnUnknownFunctionIsNotTreatedAsReadOnly(t *testing.T) {
	n, _ := signedNode(t, "[arbitrary_code]")
	msg := jobMessage(t, "20260923T080000000015")
	msg.Fun = "nosuch.function"
	n.acceptJob(msg)

	if n.executor.Depth() != 0 {
		t.Fatal("a function this build does not ship was accepted unsigned")
	}
}
