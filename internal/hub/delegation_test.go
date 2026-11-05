package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// **A claim to act for somebody is refused from a caller not granted
// act_for** (issue 618). The roles in a claim only narrow, but the name
// decides whose runs a caller sees and is written into every record as
// who asked; a body that could name anyone could read as anyone. So the
// hub refuses the call outright -- not the claim alone, which would leave
// the caller doing what the API did before -- from a certificate whose
// role does not grant act_for that principal.
func TestAClaimToActForAnotherIsRefusedWithoutActFor(t *testing.T) {
	a := newAPILab(t, nil)
	// alice holds a certificate of her own: a direct operator, with
	// every runner the claim would ask for, and no act_for.
	alice := a.operator(t, "alice")
	api := a.operator(t, "api")
	ctx := context.Background()

	claim := &transport.Delegation{Principal: "local:alice", Roles: []string{"webops"}}
	_, err := alice.Runner(ctx, transport.RunnerRequest{Fun: "jobs.list_jobs", OnBehalfOf: claim})
	if err == nil || !strings.Contains(err.Error(), "act_for") {
		t.Errorf("a caller without act_for made a claim and was not refused for it: %v", err)
	}
	// Naming somebody the grant does not cover is the same refusal: the
	// API's act_for names `local:*`.
	_, err = api.Runner(ctx, transport.RunnerRequest{Fun: "jobs.list_jobs",
		OnBehalfOf: &transport.Delegation{Principal: "cert:CN=ed", Roles: []string{"dbops"}}})
	if err == nil || !strings.Contains(err.Error(), "act_for") {
		t.Errorf("the API acted for a principal its act_for does not name: %v", err)
	}
	// And a claim that names nobody.
	_, err = api.Runner(ctx, transport.RunnerRequest{Fun: "jobs.list_jobs",
		OnBehalfOf: &transport.Delegation{Roles: []string{"dbops"}}})
	if err == nil {
		t.Error("the API made a claim naming nobody and was not refused")
	}

	// Kill is held to the same rule. The job is one alice may kill
	// directly, so only the claim can be what refuses her.
	res, err := alice.Submit(ctx, transport.SubmitRequest{Target: "web1.example", Fun: "test.sleep"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.KillJobFor(ctx, res.JID, claim); err == nil || !strings.Contains(err.Error(), "act_for") {
		t.Errorf("a kill carrying a claim from a caller without act_for was not refused for it: %v", err)
	}

	// The API, which holds act_for, makes the same claim and is answered.
	if _, err := api.Runner(ctx, transport.RunnerRequest{Fun: "jobs.list_jobs", OnBehalfOf: claim}); err != nil {
		t.Errorf("the API, granted act_for, was refused a claim for local:alice: %v", err)
	}
}

// **A direct operator is unchanged** (issue 618). alice with her own
// certificate is held to web* by the rules that held her before, sees
// her own runner calls and not the API's, and kills with no claim.
func TestADirectOperatorIsUnchangedByDelegation(t *testing.T) {
	a := newAPILab(t, nil)
	alice := a.operator(t, "alice")
	ctx := context.Background()

	out, err := alice.Runner(ctx, transport.RunnerRequest{Fun: "saltutil.refresh_pillar",
		Kwarg: map[string]any{"target": "web1.example"}})
	if err != nil || !out.Success {
		t.Fatalf("alice could not refresh web1 directly: %v %+v", err, out)
	}
	out, err = alice.Runner(ctx, transport.RunnerRequest{Fun: "saltutil.refresh_pillar",
		Kwarg: map[string]any{"target": "db1.example"}})
	if err == nil && out.Success {
		t.Error("alice refreshed db1 directly")
	}

	// A call made through the API for local:alice is not hers to read
	// as cert:CN=alice: the pair is the owner, not either half.
	api := a.operator(t, "api")
	viaAPI, err := api.Runner(ctx, transport.RunnerRequest{Fun: "saltutil.refresh_pillar",
		Kwarg:      map[string]any{"target": "web1.example"},
		OnBehalfOf: &transport.Delegation{Principal: "local:alice", Roles: []string{"webops"}}})
	if err != nil || !viaAPI.Success {
		t.Fatalf("the API could not refresh web1 for local:alice: %v %+v", err, viaAPI)
	}
	if got, err := alice.Runner(ctx, transport.RunnerRequest{Fun: "jobs.lookup_jid", Arg: []string{viaAPI.JID}}); err == nil && got.Success {
		t.Errorf("cert:CN=alice read a runner call the API made for local:alice")
	}
	if got, err := alice.Runner(ctx, transport.RunnerRequest{Fun: "jobs.lookup_jid", Arg: []string{out.JID}}); err != nil || !got.Success {
		t.Errorf("alice could not read her own runner call: %v %+v", err, got)
	}

	res, err := alice.Submit(ctx, transport.SubmitRequest{Target: "web1.example", Fun: "test.sleep"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.KillJob(ctx, res.JID); err != nil && !strings.Contains(err.Error(), "already completed") {
		t.Errorf("alice could not kill her own job directly: %v", err)
	}
}
