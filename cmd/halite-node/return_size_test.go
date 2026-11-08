package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// hubCheck is a returnPoster that applies the hub's own size check to
// the bytes the node's client would send: transport.EncodeBody into
// transport.ReadJSON at transport.MaxReturnBody, which is what
// hub.returned does. A refusal comes back as the RefusedError the real
// client turns a 400 into, so sendReturn sees what it would see.
type hubCheck struct {
	got []*job.Return
}

func (h *hubCheck) Return(_ context.Context, ret any) error {
	raw, err := transport.EncodeBody(ret)
	if err != nil {
		return err
	}
	req := httptest.NewRequest(http.MethodPost, transport.PathReturn, bytes.NewReader(raw))
	var decoded job.Return
	if err := transport.ReadJSON(httptest.NewRecorder(), req, transport.MaxReturnBody, &decoded); err != nil {
		return &transport.RefusedError{Path: transport.PathReturn, Status: http.StatusBadRequest,
			Code: transport.CodeMalformed, Msg: err.Error()}
	}
	h.got = append(h.got, &decoded)
	return nil
}

// A return too large for the hub reaches it as a failure that says so,
// rather than not at all.
//
// Before, the hub refused it, the node logged one line and dropped it,
// and the job -- which had run -- never answered. Seen in FreeBSD jails
// on 2026-11-08 with a 60 MB state return.
func TestAnOversizedReturnArrivesAsAFailureNamingItsSize(t *testing.T) {
	n := nodeForEvidence(t, "")
	hub := &hubCheck{}
	big, _ := json.Marshal(strings.Repeat("r", transport.MaxReturnBody))
	n.sendReturn(context.Background(), hub, &job.Return{
		JID: "20261108T120000000001", NodeID: "web1.example", Fun: "state.apply",
		FunArgs: []string{"upgrade"}, Success: true, RetCode: 0,
		Return: big, Out: "highstate", Schema: job.ReturnSchema,
	})

	if len(hub.got) != 1 {
		t.Fatalf("the hub took %d returns, want the stand-in: the job would never answer", len(hub.got))
	}
	got := hub.got[0]
	if got.JID != "20261108T120000000001" || got.NodeID != "web1.example" || got.Fun != "state.apply" {
		t.Errorf("the stand-in is not for the same job: %+v", got)
	}
	if got.Success || got.RetCode == 0 {
		t.Errorf("the stand-in reads as a success: success=%v retcode=%d", got.Success, got.RetCode)
	}
	var body struct {
		Oversized  string `json:"oversized"`
		Bytes      int    `json:"bytes"`
		Limit      int    `json:"limit"`
		JobSuccess bool   `json:"job_success"`
	}
	if err := json.Unmarshal(got.Return, &body); err != nil {
		t.Fatalf("the stand-in's return is not the expected object: %v: %s", err, got.Return)
	}
	if body.Bytes <= transport.MaxReturnBody || body.Limit != transport.MaxReturnBody {
		t.Errorf("the stand-in does not name the size and the limit: %s", got.Return)
	}
	if !body.JobSuccess {
		t.Errorf("the stand-in lost that the job itself succeeded: %s", got.Return)
	}
	if body.Oversized == "" {
		t.Errorf("the stand-in says nothing a person can read: %s", got.Return)
	}
}

// The node's measure and the hub's check agree to the byte.
//
// Two paths that must say the same thing: a return the node measures as
// fitting and the hub refuses is lost exactly as before, and one the node
// replaces although the hub would have taken it is a result thrown away.
// Both sides of the boundary, with an HTML-escapable character in the
// payload because json.Marshal and the client's encoder disagree about
// those.
func TestTheNodeAndTheHubAgreeWhereTheReturnLimitIs(t *testing.T) {
	n := nodeForEvidence(t, "")
	sized := func(extra int) *job.Return {
		ret := &job.Return{JID: "20261108T120000000002", NodeID: "web1.example",
			Fun: "test.fits", Success: true, Schema: job.ReturnSchema}
		ret.Return = json.RawMessage(`"<"`)
		raw, err := transport.EncodeBody(ret)
		if err != nil {
			t.Fatal(err)
		}
		pad := transport.MaxReturnBody - len(raw) + extra
		ret.Return = json.RawMessage(`"<` + strings.Repeat("r", pad) + `"`)
		return ret
	}

	at := &hubCheck{}
	n.sendReturn(context.Background(), at, sized(0))
	if len(at.got) != 1 || !at.got[0].Success {
		t.Errorf("a return of exactly the limit was not delivered as itself (%d delivered; replaced or refused)", len(at.got))
	}

	over := &hubCheck{}
	n.sendReturn(context.Background(), over, sized(1))
	if len(over.got) != 1 {
		t.Fatalf("a return one byte over the limit was not replaced, and the hub refused it")
	}
	if over.got[0].Success {
		t.Errorf("a return one byte over the limit arrived as itself; the hub must have refused it")
	}
}
