package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/orchplan"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/treesign"
	"github.com/edlitmus/halite/internal/value"
)

// Issue 765: an orchestration on nodes that require signed jobs.
//
// The operator compiles the orchestration off the hub, from their own
// checkout, with the compiler below, and signs every fleet step as an
// ordinary SPEC 25.6 job (`halite-hub orch plan`). The hub compiles it
// again, compares every step with the plan before it dispatches anything,
// and at each step sends the plan's signed job in place of an unsigned
// one. The node verifies each one as it verifies any signed job; it does
// not know plans exist.
//
// The pre-flight is a convenience for an honest hub, so that a stale plan
// fails at once, by name, before anything runs. It is not the control: a
// compromised hub skips it, and the nodes refuse whatever it sends that
// was not signed. DIVERGENCE 5.169 is why this is not a key on the hub.

// plannedConfigRefusal is what a planned orchestration's template is told
// when it reads `opts`. Both compiles bind it, so the plan and the hub's
// pre-flight have the same inputs: the plan tool has no hub configuration
// to give a template, and a value the hub supplied would be one nothing
// signed.
const plannedConfigRefusal = "a planned orchestration may not read the hub's configuration: " +
	"the plan is compiled off the hub, which has none to give it (issue 765); " +
	"pass the value with --pillar instead"

// plannedJobID seeds a planned compile's template random source in place
// of the run's job id, which the plan tool cannot know. Fixed, so that
// the two compiles draw the same values.
const plannedJobID = "orch-plan"

// CompilePlanned compiles an orchestration the way a plan is compiled:
// from loader, with the override, no grains, no `salt` dispatcher, strict
// undefined names, no nodegroups and no configuration.
//
// The plan tool calls it on the operator's checkout and the hub calls it
// on its own tree when it is given a plan, so the only input that can
// differ between them is the tree.
func CompilePlanned(loader state.Loader, sls []string, env string, pillar *value.Map, test bool) *state.Compiled {
	return compileOrch(orchCompileInputs{
		loader:        loader,
		configRefused: plannedConfigRefusal,
		jobID:         plannedJobID,
	}, sls, env, pillar, test, OrchSignatures())
}

// orchCompileInputs is what differs between a compile on the hub and a
// planned one.
type orchCompileInputs struct {
	loader        state.Loader
	config        *value.Map
	configRefused string
	nodegroups    target.Nodegroups
	jobID         string
	onRendered    func(file, sls string, pipeline []string, text string)
}

// compileOrch is the one compile both kinds go through.
func compileOrch(in orchCompileInputs, sls []string, env string, pillar *value.Map, test bool, sigs *signature.Registry) *state.Compiled {
	if pillar == nil {
		pillar = value.NewMap(0)
	}
	config := in.config
	if config == nil {
		config = value.NewMap(0)
	}
	c := &state.Compiler{
		Loader:   in.loader,
		Registry: sigs,
		Config: state.Config{
			Env:    env,
			JobID:  in.jobID,
			NodeID: OrchNodeID,
			// The hub has no grains of its own to expose here, and
			// inventing a set would make an orchestration that reads
			// `grains` look portable when it is not.
			Grains:        value.NewMap(0),
			Pillar:        pillar,
			ConfigValues:  config,
			ConfigRefused: in.configRefused,
			// SPEC 25.5: the hub's dispatcher is restricted, and this
			// build gives an orchestration template none at all rather
			// than one that has not been audited against that list.
			Salt:       nil,
			Undefined:  template.Strict,
			Nodegroups: in.nodegroups,
			Test:       test,
			GPG:        render.GPGOptions{},
			OnRendered: in.onRendered,
		},
	}
	compiled := c.CompileSLS(sls)
	refuseParallelStateRuns(compiled, sigs)
	return compiled
}

// PlannedStep is one fleet step and the job it sends, unsigned: the plan
// tool gives it an identifier, an expiry and a signature.
type PlannedStep struct {
	ID   string
	Step string
	Job  *job.Job
}

// PlanSteps works out every job a compiled orchestration sends, as a plan
// signs them, and the hub-side steps it runs.
//
// The jobs are what stepJob builds -- the function the step modules call
// at run time -- from the arguments the runner would bind, with the
// keyword arguments put through jobsign.WireValues, so that what is
// signed is what a node decodes. Under test, a salt.function step is left
// out: a test run does not dispatch one.
//
// Refused here, rather than signed and refused by every node later:
//
//   - a target a node cannot evaluate about itself, such as a nodegroup;
//     a node refuses every signed job so targeted (matchesSignedTarget);
//   - `retry` on a fleet step: a second attempt would send the same
//     signed job again, which the hub refuses as a job it already has and
//     every node that ran it refuses as a replay;
//   - two fleet steps with one state ID, which a plan matches steps by.
func PlanSteps(compiled *state.Compiled, env string, test bool) ([]PlannedStep, []orchplan.HubStep, error) {
	sigs := OrchSignatures()
	var steps []PlannedStep
	var hubSteps []orchplan.HubStep
	seen := map[string]bool{}
	for _, ch := range compiled.Low {
		fun := ch.Func()
		if !isFleetStep(fun) {
			hubSteps = append(hubSteps, orchplan.HubStep{ID: ch.ID, Step: fun, Name: ch.Name})
			continue
		}
		if seen[ch.ID] {
			return nil, nil, fmt.Errorf("step %s: two fleet steps share this ID, and a plan matches steps by ID; "+
				"give each its own", ch.ID)
		}
		seen[ch.ID] = true
		if ch.Opts.Retry != nil {
			return nil, nil, fmt.Errorf("step %s: `retry` cannot be planned: a second attempt would send the "+
				"same signed job again, which the hub and every node that ran it refuse; remove it", ch.ID)
		}
		sig, ok := sigs.Lookup(fun)
		if !ok {
			return nil, nil, fmt.Errorf("step %s: %s is not a step this build ships", ch.ID, fun)
		}
		bound, errs := sig.Bind(nil, ch.Args)
		if len(errs) > 0 {
			return nil, nil, fmt.Errorf("step %s: %v", ch.ID, errs[0])
		}
		sub, honoursTest, refusal := stepJob(fun, bound, env, test)
		if refusal != "" {
			return nil, nil, fmt.Errorf("step %s: %s", ch.ID, refusal)
		}
		if test && !honoursTest {
			continue
		}
		j, err := signableJob(sub)
		if err != nil {
			return nil, nil, fmt.Errorf("step %s: %w", ch.ID, err)
		}
		steps = append(steps, PlannedStep{ID: ch.ID, Step: fun, Job: j})
	}
	return steps, hubSteps, nil
}

// signableJob is the job record whose SigningPayload a node will rebuild,
// with no identifier or expiry yet.
func signableJob(sub Submission) (*job.Job, error) {
	kind, ok := target.KindFromFlag(sub.TargetKind)
	if !ok {
		return nil, fmt.Errorf("%q is not a target kind", sub.TargetKind)
	}
	// No nodegroups, as on a node: an expression naming one fails here
	// with the reason a node would give.
	if _, err := target.Compile(kind, sub.Target, nil); err != nil {
		return nil, fmt.Errorf("a node cannot check that a job targeted at %q (%s) was meant for it: %w; "+
			"target by something a node can evaluate about itself", sub.Target, kind, err)
	}
	wire, err := jobsign.WireValues(sub.Kwarg)
	if err != nil {
		return nil, err
	}
	return &job.Job{
		Target:     sub.Target,
		TargetKind: kind.String(),
		Fun:        sub.Fun,
		Arg:        sub.Arg,
		Kwarg:      wire,
		Env:        sub.Env,
		Test:       sub.Test,
	}, nil
}

// PillarDigest is the digest a run records for its override, which a plan
// carries so that it cannot be run with another.
func PillarDigest(p *value.Map) string { return pillarDigest(p) }

// planRefusal is a pre-flight refusal: the step and the field are logged.
type planRefusal struct {
	step, field, msg string
}

func (r *planRefusal) Error() string { return r.msg }

// refusePlan builds a refusal, ending every message with what to do.
func refusePlan(step, field, format string, args ...any) error {
	return &planRefusal{step: step, field: field, msg: fmt.Sprintf(format, args...) +
		"; plan the orchestration again and sign the new plan"}
}

// treeRevision is the issue 764 manifest id of the tree this hub serves
// for env, or empty when it serves none.
func (s *Server) treeRevision(env string) (string, error) {
	if s.Files == nil {
		return "", nil
	}
	raw, _, err := s.Files.Read(env, treesign.ManifestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return treesign.ID(raw), nil
}

// checkPlan is the pre-flight: everything about the plan is compared with
// this run before any step is dispatched, and any difference refuses the
// whole run.
//
// The whole run rather than the one step, because a step refused mid-run
// sets off the `onfail` steps after it, and a rollback set off by a stale
// plan is the wrong reaction to one.
//
// seed is the resume's carried-forward steps: they dispatch nothing, so
// they need no signature, and a plan entry for one is refused as a step
// the run will not send.
func (s *Server) checkPlan(req OrchRequest, env string, compiled *state.Compiled, seed map[string]bool) error {
	plan := req.Plan
	if strings.Join(plan.SLS, ",") != strings.Join(req.SLS, ",") {
		return refusePlan("", "sls", "the plan is for %s and this run is %s",
			strings.Join(plan.SLS, ","), strings.Join(req.SLS, ","))
	}
	if plan.Env != env {
		return refusePlan("", "env", "the plan is for environment %q and this run compiles %q", plan.Env, env)
	}
	if plan.Test != req.Test {
		if plan.Test {
			return refusePlan("", "test", "the plan was made for a test run and this run is real; "+
				"a test plan signs every state step with test set, so a node would only predict")
		}
		return refusePlan("", "test", "the plan was made for a real run and this run is a test; "+
			"pass --test to orch plan for a test plan")
	}
	if got := pillarDigest(req.Pillar); got != plan.PillarDigest {
		return refusePlan("", "pillar", "the plan was compiled with a different pillar override from this run's; "+
			"pass the same --pillar to both")
	}
	if plan.ResumeOf != string(req.ResumeOf) || plan.ResumeFrom != req.ResumeFrom {
		switch {
		case req.ResumeOf == "":
			return refusePlan("", "resume", "the plan resumes %s from %s and this run is not a resume",
				plan.ResumeOf, plan.ResumeFrom)
		case plan.ResumeOf == "":
			return refusePlan("", "resume", "this resume of %s needs a resume plan: a resume always needs a new "+
				"plan, `orch plan --resume-of %s --from %s`", req.ResumeOf, req.ResumeOf, req.ResumeFrom)
		default:
			return refusePlan("", "resume", "the plan resumes %s from %s and this run resumes %s from %s",
				plan.ResumeOf, plan.ResumeFrom, req.ResumeOf, req.ResumeFrom)
		}
	}
	now := s.now()
	if !plan.ExpiresAt().After(now) {
		return refusePlan("", "expires", "the plan expired at %s and it is now %s",
			orchplan.FormatTime(plan.ExpiresAt()), orchplan.FormatTime(now))
	}
	if plan.TreeRevision != "" {
		served, err := s.treeRevision(env)
		if err != nil {
			return fmt.Errorf("reading this hub's %s for %s: %w", treesign.ManifestPath, env, err)
		}
		if served != plan.TreeRevision {
			if served == "" {
				return refusePlan("", "tree_revision", "the plan was compiled from signed tree revision %s and "+
					"this hub serves %s with no manifest", plan.TreeRevision, env)
			}
			return refusePlan("", "tree_revision", "the plan was compiled from signed tree revision %s and "+
				"this hub serves %s", plan.TreeRevision, served)
		}
	}

	planned, hubSteps, err := PlanSteps(compiled, env, req.Test)
	if err != nil {
		return refusePlan("", "compile", "%v", err)
	}
	if err := sameHubSteps(plan.HubSteps, hubSteps); err != nil {
		return err
	}

	wanted := map[string]bool{}
	for _, step := range planned {
		if seed[step.ID] {
			continue
		}
		wanted[step.ID] = true
		entry, ok := plan.Step(step.ID)
		if !ok {
			if req.ResumeOf != "" {
				return refusePlan(step.ID, "missing", "step %s runs in this resume and is not in the plan: "+
					"a step before %s that %s did not record as finished runs again, and needs signing too "+
					"(a run stopped by a hub restart may have recorded no steps; issue 802)",
					step.ID, req.ResumeFrom, req.ResumeOf)
			}
			return refusePlan(step.ID, "missing", "step %s sends a job and is not in the plan", step.ID)
		}
		if err := s.comparePlannedStep(step, entry); err != nil {
			return err
		}
	}
	for _, entry := range plan.Steps {
		if !wanted[entry.ID] {
			return refusePlan(entry.ID, "extra", "the plan signs step %s and this run does not send it", entry.ID)
		}
	}
	return nil
}

// sameHubSteps compares the hub-side steps by ID, function and name, in
// order. An honest hub refuses an orchestration whose runner steps changed
// since it was planned; nothing makes a compromised one.
func sameHubSteps(planned, compiled []orchplan.HubStep) error {
	describe := func(list []orchplan.HubStep) string {
		parts := make([]string, len(list))
		for i, h := range list {
			parts[i] = h.ID + " (" + h.Step + " " + h.Name + ")"
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	if len(planned) != len(compiled) {
		return refusePlan("", "hub_steps", "the plan lists hub steps %s and this compile has %s",
			describe(planned), describe(compiled))
	}
	for i := range planned {
		if planned[i] != compiled[i] {
			return refusePlan(compiled[i].ID, "hub_steps", "the plan lists hub steps %s and this compile has %s",
				describe(planned), describe(compiled))
		}
	}
	return nil
}

// comparePlannedStep holds one step's compiled job to the plan's: the
// same canonical bytes a node checks the signature over, with the plan's
// identifier and expiry, and then the identifier is one this hub has not
// used.
func (s *Server) comparePlannedStep(step PlannedStep, entry orchplan.Step) error {
	if entry.Step != step.Step {
		return refusePlan(step.ID, "step", "step %s is planned as %s and compiles to %s", step.ID, entry.Step, step.Step)
	}
	if field, planned, compiled, differ := differentField(step.Job, entry.Job); differ {
		return refusePlan(step.ID, field, "step %s is signed for %s %s and compiles to %s", step.ID, field, planned, compiled)
	}
	if s.Jobs != nil {
		if _, err := s.Jobs.Get(job.ID(entry.Job.JID)); err == nil {
			return refusePlan(step.ID, "jid", "step %s's signed job %s has already been sent by this hub, and a node "+
				"that ran it refuses it as a replay", step.ID, entry.Job.JID)
		} else if !errors.Is(err, job.ErrNoJob) {
			return fmt.Errorf("checking whether %s has been sent before: %w", entry.Job.JID, err)
		}
	}
	return nil
}

// differentField compares a compiled job with a plan's by the signed
// payload, field by field so the refusal can name one. The payloads as a
// whole are compared last, through jobsign.Canonical, so that a
// difference this list does not name still refuses.
func differentField(compiled *job.Job, planned orchplan.Job) (field, plannedText, compiledText string, differ bool) {
	payload, err := planned.Payload()
	if err != nil {
		return "expires", planned.Expires, "a time", true
	}
	withPlanID := *compiled
	withPlanID.JID = job.ID(planned.JID)
	withPlanID.Expires = payload.Expires
	ours := job.SigningPayload(&withPlanID)

	quote := func(v any) string {
		raw, _ := json.Marshal(v)
		return string(raw)
	}
	kwargBytes := func(kw map[string]any) []byte {
		return jobsign.Canonical(jobsign.Payload{Kwarg: kw})
	}
	switch {
	case ours.Target != payload.Target:
		return "target", quote(payload.Target), quote(ours.Target), true
	case ours.TargetKind != payload.TargetKind:
		return "target_kind", quote(payload.TargetKind), quote(ours.TargetKind), true
	case ours.Fun != payload.Fun:
		return "fun", quote(payload.Fun), quote(ours.Fun), true
	case quote(ours.Arg) != quote(payload.Arg) && !(len(ours.Arg) == 0 && len(payload.Arg) == 0):
		return "arg", quote(payload.Arg), quote(ours.Arg), true
	case !bytes.Equal(kwargBytes(ours.Kwarg), kwargBytes(payload.Kwarg)):
		return "kwarg", quote(payload.Kwarg), quote(ours.Kwarg), true
	case ours.Env != payload.Env:
		return "env", quote(payload.Env), quote(ours.Env), true
	case !bytes.Equal(jobsign.Canonical(ours), jobsign.Canonical(payload)):
		return "payload", "one job", "another", true
	}
	return "", "", "", false
}

// signedStep fills a step's submission from the plan, once the step is
// about to be dispatched, and checks it again: the pre-flight compared the
// compile, and this compares what is actually being sent.
func (o *orchRunner) signedStep(id string, sub Submission) (Submission, error) {
	entry, ok := o.plan.Step(id)
	if !ok {
		return Submission{}, fmt.Errorf("step %s is not in this run's plan, so it has no signature to send", id)
	}
	j, err := signableJob(sub)
	if err != nil {
		return Submission{}, err
	}
	if field, planned, compiled, differ := differentField(j, entry.Job); differ {
		return Submission{}, fmt.Errorf("step %s is signed for %s %s and is about to send %s; not sent",
			id, field, planned, compiled)
	}
	payload, err := entry.Job.Payload()
	if err != nil {
		return Submission{}, err
	}
	sub.Kwarg = j.Kwarg
	sub.JID = job.ID(entry.Job.JID)
	sub.Expires = payload.Expires
	sub.Signature = entry.Signature
	return sub, nil
}

// planExpiresIn is how long a plan has left, for the log.
func planExpiresIn(p *orchplan.Plan, now time.Time) time.Duration {
	return p.ExpiresAt().Sub(now).Round(time.Second)
}

// PlanOptions is what `halite-hub orch plan` compiles a plan for.
type PlanOptions struct {
	SLS    []string
	Env    string
	Pillar *value.Map
	Test   bool
	// ResumeOf and ResumeFrom make a resume plan, which signs the step
	// named and every fleet step after it.
	ResumeOf   string
	ResumeFrom string
	// TreeRevision is the checkout's issue 764 manifest id, if it has one.
	TreeRevision string
	Created      time.Time
	// TTL is the window for the whole run: every step expires at
	// Created plus TTL.
	TTL time.Duration
}

// NewPlan compiles an orchestration from loader and lays out its plan,
// unsigned: every step's job with its identifier and expiry. The caller
// signs each payload, in order, and puts the signature in Steps[i].
//
// Job identifiers are minted from Created, one microsecond apart, in the
// 21-character form the hub accepts for a signed job. They carry the time
// the plan was made, not the time the step runs.
func NewPlan(loader state.Loader, opts PlanOptions) (*orchplan.Plan, []jobsign.Payload, error) {
	if len(opts.SLS) == 0 {
		return nil, nil, errors.New("a plan needs an orchestration SLS")
	}
	if opts.Env == "" {
		opts.Env = "base"
	}
	if opts.TTL <= 0 {
		return nil, nil, errors.New("a plan needs a positive time to live")
	}
	if (opts.ResumeOf == "") != (opts.ResumeFrom == "") {
		return nil, nil, errors.New("a resume plan needs both --resume-of and --from")
	}
	compiled := CompilePlanned(loader, opts.SLS, opts.Env, opts.Pillar, opts.Test)
	if err := compiled.Err(); err != nil {
		return nil, nil, err
	}
	steps, hubSteps, err := PlanSteps(compiled, opts.Env, opts.Test)
	if err != nil {
		return nil, nil, err
	}

	// A resume signs the named step and everything after it; the steps
	// before it are carried forward by the hub from its record and send
	// nothing.
	include := map[string]bool{}
	from := opts.ResumeFrom == ""
	for _, ch := range compiled.Low {
		if ch.ID == opts.ResumeFrom {
			from = true
		}
		if from {
			include[ch.ID] = true
		}
	}
	if !from {
		return nil, nil, fmt.Errorf("the orchestration has no step %q to resume from", opts.ResumeFrom)
	}

	created := opts.Created.UTC().Truncate(time.Microsecond)
	expires := created.Add(opts.TTL)
	plan := &orchplan.Plan{
		Schema:       orchplan.Schema,
		SLS:          opts.SLS,
		Env:          opts.Env,
		Test:         opts.Test,
		PillarDigest: pillarDigest(opts.Pillar),
		TreeRevision: opts.TreeRevision,
		ResumeOf:     opts.ResumeOf,
		ResumeFrom:   opts.ResumeFrom,
		Created:      orchplan.FormatTime(created),
		Expires:      orchplan.FormatTime(expires),
		Steps:        []orchplan.Step{},
		HubSteps:     hubSteps,
	}
	if plan.HubSteps == nil {
		plan.HubSteps = []orchplan.HubStep{}
	}
	var payloads []jobsign.Payload
	for _, step := range steps {
		if !include[step.ID] {
			continue
		}
		step.Job.JID = job.NewID(created.Add(time.Duration(len(payloads)) * time.Microsecond))
		step.Job.Expires = expires
		plan.Steps = append(plan.Steps, orchplan.Step{ID: step.ID, Step: step.Step, Job: orchplan.FromJob(step.Job)})
		payloads = append(payloads, job.SigningPayload(step.Job))
	}
	return plan, payloads, nil
}
