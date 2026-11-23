package orchplan

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/jobsign"
)

func samplePlan() *Plan {
	created := time.Date(2026, 11, 22, 18, 10, 0, 0, time.UTC)
	expires := FormatTime(created.Add(time.Hour))
	return &Plan{
		SLS: []string{"deploy"}, Env: "base", PillarDigest: "none",
		Created: FormatTime(created), Expires: expires,
		Steps: []Step{{
			ID: "apply_web", Step: "salt.state", Signature: "c2ln",
			Job: Job{JID: "20261122T181000000000", Target: "web*", TargetKind: "glob", Fun: "state.apply",
				Arg: []string{"web"}, Kwarg: map[string]any{"pillar": map[string]any{"n": 1}}, Env: "base", Expires: expires},
		}},
		HubSteps: []HubStep{{ID: "announce", Step: "salt.runner", Name: "event.send"}},
	}
}

func encoded(t *testing.T, p *Plan) []byte {
	t.Helper()
	raw, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A plan the tool wrote reads back to the same signed payload, numbers
// included: the payload a node rebuilds from the wire is the one the
// signer signed.
func TestAPlanReadsBackToThePayloadItWasSignedOver(t *testing.T) {
	j := &job.Job{JID: "20261122T181000000000", Target: "web*", TargetKind: "glob", Fun: "state.apply",
		Arg: []string{"web"}, Env: "base", Test: true, Expires: time.Date(2026, 11, 22, 19, 10, 0, 0, time.UTC)}
	wire, err := jobsign.WireValues(map[string]any{"pillar": map[string]any{"big": int64(9007199254740993), "f": 1.5}})
	if err != nil {
		t.Fatal(err)
	}
	j.Kwarg = wire
	p := samplePlan()
	p.Steps[0].Job = FromJob(j)
	p.Expires = p.Steps[0].Job.Expires

	parsed, err := Parse(encoded(t, p))

	if err != nil {
		t.Fatal(err)
	}
	got, err := parsed.Steps[0].Job.Payload()
	if err != nil {
		t.Fatal(err)
	}
	if string(jobsign.Canonical(got)) != string(jobsign.Canonical(job.SigningPayload(j))) {
		t.Errorf("the payload changed on the way through the file:\n%s\n%s",
			jobsign.Canonical(got), jobsign.Canonical(job.SigningPayload(j)))
	}
}

// Parse is strict: whatever it does not understand, it refuses.
func TestAPlanThatBreaksARuleIsRefused(t *testing.T) {
	cases := map[string]struct {
		change func(p *Plan)
		raw    func(raw []byte) []byte
		want   string
	}{
		"another schema":       {change: func(p *Plan) {}, raw: func(r []byte) []byte { return []byte(strings.Replace(string(r), Schema, "halite.orch-plan/2", 1)) }, want: "schema"},
		"an unknown field":     {raw: func(r []byte) []byte { return []byte(strings.Replace(string(r), `"env"`, `"extra": 1, "env"`, 1)) }, want: "unknown field"},
		"trailing content":     {raw: func(r []byte) []byte { return append(r, []byte("{}")...) }, want: "after its document"},
		"no sls":               {change: func(p *Plan) { p.SLS = nil }, want: "no orchestration SLS"},
		"no pillar digest":     {change: func(p *Plan) { p.PillarDigest = "" }, want: "pillar_digest"},
		"half a resume":        {change: func(p *Plan) { p.ResumeOf = "20261122T181000000000" }, want: "both resume_of and resume_from"},
		"expires before made":  {change: func(p *Plan) { p.Expires = p.Created; p.Steps[0].Job.Expires = p.Created }, want: "expires before"},
		"a bad job id":         {change: func(p *Plan) { p.Steps[0].Job.JID = "x" }, want: "not a job identifier"},
		"a job id used twice":  {change: func(p *Plan) { s := p.Steps[0]; s.ID = "other"; p.Steps = append(p.Steps, s) }, want: "used twice"},
		"a step id used twice": {change: func(p *Plan) { s := p.Steps[0]; s.Job.JID = "20261122T181000000001"; p.Steps = append(p.Steps, s) }, want: "names step"},
		"a step with its own expiry": {change: func(p *Plan) {
			p.Steps[0].Job.Expires = FormatTime(time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC))
		}, want: "one window"},
		"an unsigned step":      {change: func(p *Plan) { p.Steps[0].Signature = "" }, want: "no signature"},
		"a step with no target": {change: func(p *Plan) { p.Steps[0].Job.Target = "" }, want: "no target"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := samplePlan()
			if tc.change != nil {
				tc.change(p)
			}
			raw := encoded(t, p)
			if tc.raw != nil {
				raw = tc.raw(raw)
			}

			_, err := Parse(raw)

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want a refusal saying %q, got %v", tc.want, err)
			}
		})
	}
}

// A plan carries no parallel flag, batch or subset: its job is exactly
// job.SigningPayload's field set (issue 765 question 4).
func TestAPlanJobIsTheSignedFieldSetAndNothingElse(t *testing.T) {
	signed := reflect.TypeOf(jobsign.Payload{}).NumField()
	planned := reflect.TypeOf(Job{})
	if planned.NumField() != signed {
		t.Errorf("a plan's job has %d fields and the signed payload %d", planned.NumField(), signed)
	}
	for i := 0; i < planned.NumField(); i++ {
		name := strings.Split(planned.Field(i).Tag.Get("json"), ",")[0]
		switch name {
		case "jid", "target", "target_kind", "fun", "arg", "kwarg", "env", "expires":
		default:
			t.Errorf("a plan's job carries %q, which no signature covers", name)
		}
	}
}
