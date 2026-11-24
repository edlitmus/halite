package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/runner"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/tracing"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

// executeJob runs one job from the hub and produces its return.
//
// Nothing here calls Fatalf. A job that cannot be compiled, cannot be
// run, or names a function this build does not ship comes back as a
// failed return, because an agent that exits on a bad job is an agent
// an operator can stop with one typo.
//
// The result is named, and set as soon as there is one, because the deferred
// function below recovers a panic and fills that return in: with an unnamed
// result a recovered panic returns the zero value, so a module that panicked
// gave `executor.Run` a nil return, which it read on the next line, in a
// goroutine nothing recovers, and the node stopped. The comment on the
// recover was right about what it meant and wrong about what it did.
// DIVERGENCE 5.542.
func (n *node) executeJob(j *job.Job) (result *job.Return) {
	// A job runs against a copy of the node, never against the agent's
	// own.
	//
	// The first cut mutated the agent: a job carrying `test: true` set
	// the flag, and every job after it on that node was a dry run for
	// ever after. An operator would have watched `run '*' state.apply`
	// report what it would do and believe it had done it. The same
	// applies to the environment a job names.
	n = n.forJob()
	// One signed revision per environment for the length of this job, so
	// a tree that changes on the hub mid-run fails the run rather than
	// mixing two revisions (issue 764). A no-op without the requirement.
	n.files = pinTreeForRun(n.files)

	// SPEC 26.3's span per job, under whatever the hub sent. Nil when
	// tracing is off, and every method on it tolerates that, so nothing
	// below this line branches on whether tracing is configured.
	span := n.jobSpan(j)
	defer span.End()
	// The states and the file transfers this job causes hang off it,
	// and they find it through the context rather than through a
	// parameter on every seam between here and a module.
	n.jobCtx = tracing.ContextWithSpan(context.Background(), span)
	// The tree is wrapped for tracing below, after adoptJobEnvironment
	// and not here: the wrapper is what a traced job's n.files is, and
	// adoptJobEnvironment tells the hub's tree from the node's own by
	// its type.

	started := time.Now()
	ret := &job.Return{
		JID:         j.JID,
		NodeID:      n.nodeID,
		Fun:         j.Fun,
		FunArgs:     j.Arg,
		StartTime:   started.UTC().Format(time.RFC3339Nano),
		NodeVersion: version.String(),
		Schema:      job.ReturnSchema,
	}
	result = ret
	defer func() {
		took := time.Since(started)
		ret.DurationMS = took.Milliseconds()
		// A module that panics must produce a failed return rather than
		// take the agent down: one bad job would otherwise stop a node
		// answering anything at all.
		if r := recover(); r != nil {
			fail(ret, fmt.Errorf("the job panicked: %v", r))
			n.log.Error("a job panicked", "jid", string(j.JID), "fun", j.Fun, "panic", fmt.Sprint(r))
		}
		// Counted last, so that a job which panicked is counted as the
		// failure it became rather than as whatever it was before the
		// recover ran.
		n.metrics.countJob(ret, took, n.metricFunction(j.Fun))
	}()

	// The job's environment, not this invocation's. A job for `staging`
	// compiled against `base` would apply the wrong tree and report
	// success.
	if err := n.adoptJobEnvironment(j); err != nil {
		return fail(ret, err)
	}
	// After the environment, and never before it. It was before it, and
	// a job the tracer sampled reached adoptJobEnvironment with n.files
	// a tracedFiles, which is not a *fileserver.Remote: so a sampled job
	// naming another environment was sent to this node's own roots for
	// it, applied whatever they held (or the packaged defaults, where
	// none are configured), and reported success -- for only the jobs
	// the sample rate picked, which is what would make it look like
	// chance. The same job unsampled fetched from the hub. Wrapping the
	// tree the environment settled on also traces a job's fetches from
	// the node's own roots for another environment, which the old order
	// replaced with an unwrapped tree. Issue 803, DIVERGENCE 5.721.
	n.files = n.traceFiles(span)
	// `test` on the job overrides the node's default in one direction
	// only: a hub asking for a dry run gets one, and a node configured
	// with `test: true` is not talked out of it.
	test, err := jobTestFlag(j)
	if err != nil {
		return fail(ret, err)
	}
	if test {
		n.test = true
	}

	out, err := n.runFunction(j)
	if err != nil {
		span.Fail(err)
		return fail(ret, err)
	}
	span.SetAttr("halite.success", out.success)
	span.SetAttr("halite.retcode", out.retcode)
	if !out.success {
		// A job that ran and reported a failure is a failed span. The
		// distinction from the branch above is the message, not the
		// status: one is "this did not run" and the other is "this ran
		// and the machine is not as asked", and an operator reading a
		// trace needs to tell them apart.
		span.SetStatus(tracing.StatusError, "the job reported a failure")
	} else {
		span.SetStatus(tracing.StatusOk, "")
	}
	ret.Success = out.success
	ret.RetCode = out.retcode
	ret.Out = out.format
	// Encoded here, with the ordered model's codec, because a state
	// result is a *value.Map and encoding/json cannot see one.
	encoded, err := value.EncodeJSON(out.value, 0)
	if err != nil {
		return fail(ret, fmt.Errorf("encoding the result: %w", err))
	}
	ret.Return = encoded
	return ret
}

// metricFunction is the `fun` label for a job: the function's name when this
// node has such a function, and `other` when it has not.
//
// The name comes from the hub, and a node counted whatever it was given:
// 600 different names made the series bound spill, after which a real
// function's counts landed in `__overflow__` and every name a node does not
// have was a series of its own. This node knows what it can run, so the
// label is that and not what it was asked for (the hub, which cannot know,
// bounds the name by its shape: 5.532). DIVERGENCE 5.549.
func (n *node) metricFunction(fun string) string {
	if _, isState := stateFunction(fun); isState || n.registry.Exec.Has(fun) {
		return fun
	}
	return "other"
}

// jobTestFlag reads a job's `test` keyword argument, which is how a dry
// run reaches a node, and refuses one that is not a boolean.
//
// It used to be `j.Kwarg["test"].(bool)` with the failure ignored, so
// `"true"`, `"yes"` and `1` all meant "not a dry run" and the job ran
// for real. Under a v1 signature that was a way past the signature as
// well, because v1 wrote `true` and `"true"` as the same bytes
// (DIVERGENCE 5.330). The signature no longer allows it; this is the
// other half, so that a value nobody can have meant as "run it for real"
// is not taken to mean exactly that. Refused rather than coerced: a
// caller who wrote `"false"` is as likely as one who wrote `"true"`, and
// a guess in either direction is a guess.
func jobTestFlag(j *job.Job) (bool, error) {
	raw, present := j.Kwarg["test"]
	if !present || raw == nil {
		return false, nil
	}
	test, ok := raw.(bool)
	if !ok {
		return false, &job.Refusal{
			Reason: job.ReasonMalforme,
			Detail: fmt.Sprintf("the job's `test` is %T %v, and a dry run is asked for with the boolean true "+
				"or false; refused rather than guessed, since guessing wrong runs it for real", raw, raw),
		}
	}
	return test, nil
}

// forJob is a shallow copy: the pointers -- configuration, registries,
// logger, redactor -- are shared deliberately, and the fields a job may
// change are not.
func (n *node) forJob() *node {
	if n.grainsMu != nil {
		// The copy reads every field, `grains` among them, while the
		// refresh loop may be replacing it.
		n.grainsMu.RLock()
		defer n.grainsMu.RUnlock()
	}
	copied := *n
	return &copied
}

type outcome struct {
	value   any
	success bool
	retcode int
	format  string
}

func fail(ret *job.Return, err error) *job.Return {
	ret.Success = false
	ret.RetCode = 1
	// Encoded rather than assigned: the field is raw JSON, and a bare
	// string is not.
	if encoded, encErr := value.EncodeJSON(err.Error(), 0); encErr == nil {
		ret.Return = encoded
	}
	return ret
}

// adoptJobEnvironment points this node's roots at the environment the
// job names, subject to env_allowlist and env_denylist: SPEC 28.3's
// controls apply to a job from a hub exactly as they do to a command
// line, or they are not controls.
func (n *node) adoptJobEnvironment(j *job.Job) error {
	if j.Env == "" || j.Env == n.env {
		return nil
	}
	if err := checkEnvPermitted(n.cfg, j.Env); err != nil {
		return err
	}
	n.env = j.Env
	n.pillarEnv = j.Env
	if remote, ok := n.files.(*fileserver.Remote); ok {
		// The hub serves every environment from one connection, so
		// only the environment the fetcher asks for changes.
		n.files = remote.ForEnv(j.Env)
	} else {
		n.files = pinTreeForRun(n.localTree(n.env))
	}
	n.pillars = fileserver.NewRoots(n.pillarRootsFor(n.pillarEnv))
	return nil
}

// runFunction dispatches on the job's function name, the same two ways
// the command line does: a state function through the compiler, and
// anything else through the execution registry.
func (n *node) runFunction(j *job.Job) (outcome, error) {
	if fn, ok := stateFunction(j.Fun); ok {
		n.refreshTreeListing()
		return n.runStateJob(j, fn)
	}
	// A pillar that does not compile no longer fails every function.
	//
	// It used to: a node whose pillar tree had one bad GPG block could
	// not answer `test.ping`, report its grains, or say whether a
	// service was running — it went silent exactly when somebody was
	// trying to find out what was wrong with it. The error travels on
	// the context instead and surfaces at the functions that read
	// pillar, which is where it means something.
	p, pillarErr := n.compilePillarOrErr()
	if pillarErr != nil {
		n.log.Warn("this node's pillar did not compile; functions that read it will fail",
			"jid", string(j.JID), "fun", j.Fun, "error", pillarErr.Error())
		p = value.NewMap(0)
	}
	if err := n.quiesceAfterPillar(j); err != nil {
		return outcome{}, err
	}
	positional := make([]any, len(j.Arg))
	for i, a := range j.Arg {
		positional[i] = a
	}
	kwargs := value.NewMap(len(j.Kwarg))
	for k, v := range j.Kwarg {
		if k == "test" {
			continue
		}
		// Lifted out of what `encoding/json` produced: a mapping
		// argument arrives as a Go map with no order, and a number as
		// a json.Number, and a module must see neither.
		kwargs.Set(k, value.FromJSON(v))
	}
	ctx := n.contextFor(p, string(j.JID))
	ctx.PillarErr = pillarErr
	out, err := n.registry.Exec.CallPositional(ctx, j.Fun, positional, kwargs)
	if err != nil {
		return outcome{}, err
	}
	return outcome{value: out, success: true, retcode: 0}, nil
}

// quiesceAfterPillar refuses a job whose own pillar has just quiesced the
// node.
//
// A job is admitted against the last pillar the agent read, and the
// pillar it then compiles is newer. Without this the first job after an
// operator quiesced a node in pillar -- most likely the highstate the
// quiesce was meant to stop -- would run, because it was admitted before
// anybody had read the pillar that said so. The job has its acceptance
// record by now, so it fails with the refusal rather than being refused;
// the chain is append-only and cannot be told otherwise.
//
// Only in the agent: agentless mode does not check quiesce at all
// (DIVERGENCE 5.694), and pillarQuiesce is nil there.
func (n *node) quiesceAfterPillar(j *job.Job) error {
	if n.pillarQuiesce == nil {
		return nil
	}
	return n.quiesceHeld(j.Fun)
}

// stateFunction reports whether a name is one the state compiler
// handles, and what to call it. The list is internal/state's, because
// the hub's policy catalog reads it too (DIVERGENCE 5.564).
func stateFunction(fun string) (string, bool) {
	return state.JobFunction(fun)
}

// jobPillarOverride reads a state job's `pillar` keyword argument: the
// override Salt's state.apply, state.sls and state.highstate take, which a
// `salt.state` orchestration step's `pillar:` and `halite-hub run ...
// pillar=...` both send. Nil when the job has none.
//
// Nothing read it before issue 820: the job carried it, the node ran the
// state with its ordinary pillar, and reported success. DIVERGENCE 5.712.
//
// Anything but a mapping is refused. Salt logs "Pillar override was not
// passed as a dictionary" and applies the state without it (Salt 3008.x,
// salt/state.py, State._gather_pillar), which is the silent half of the
// same defect: a run that was asked for a different pillar and got the
// ordinary one, and said it succeeded.
//
// `pillar_enc` is refused too. In Salt it names the renderer, `gpg`, that
// decrypts the override before it is merged; this node does not, and
// merging the ciphertext as though it were the value would write it
// wherever the state put the value.
func jobPillarOverride(j *job.Job) (*value.Map, error) {
	if enc, present := j.Kwarg["pillar_enc"]; present && enc != nil {
		return nil, &job.Refusal{
			Reason: job.ReasonMalforme,
			Detail: fmt.Sprintf("the job asks for its pillar override to be decrypted with pillar_enc=%v, "+
				"which this node does not do; refused rather than merging the ciphertext as the value", enc),
		}
	}
	raw, present := j.Kwarg["pillar"]
	if !present || raw == nil {
		return nil, nil
	}
	override, ok := value.FromJSON(raw).(*value.Map)
	if !ok {
		return nil, &job.Refusal{
			Reason: job.ReasonMalforme,
			Detail: fmt.Sprintf("the job's pillar override is %s %v, and a pillar override must be a mapping "+
				"of keys to merge over this node's pillar; refused rather than running with the ordinary pillar",
				value.TypeName(value.FromJSON(raw)), raw),
		}
	}
	if override.Len() == 0 {
		return nil, nil
	}
	return override, nil
}

// mergePillarOverride merges a job's override over the compiled pillar,
// the way Salt's State.__init__ "reapplies overrides on top of compiled
// pillar": salt.utils.dictupdate.merge with the node's own
// pillar_source_merging_strategy and pillar_merge_lists, so under the
// default a nested mapping is merged key by key and a list is replaced.
// Neither argument is changed; the node's pillar is not this run's.
func (n *node) mergePillarOverride(p, override *value.Map) *value.Map {
	if p == nil {
		p = value.NewMap(0)
	}
	strategy, _ := value.ParseStrategy(n.cfg.String("pillar_source_merging_strategy", "smart"))
	merged, ok := value.Merge(p, override, value.MergeOpts{
		Strategy:   strategy,
		MergeLists: n.cfg.Bool("pillar_merge_lists", false),
	}).(*value.Map)
	if !ok {
		return p
	}
	return merged
}

// jobPillarEnv reads a state job's `pillarenv` keyword argument: the
// pillar environment Salt's state functions compile that run's pillar
// from, which a `salt.state` orchestration step's `pillarenv:` and
// `halite-hub run ... pillarenv=...` both send. Empty when the job names
// none, and the run then has the node's own pillar.
//
// Nothing read it before issue 826, so a run asked for another pillar
// environment rendered with the node's own and reported success.
// DIVERGENCE 5.716.
//
// The environment is held to env_allowlist and env_denylist, as the
// node's configured pillarenv is at start and a job's saltenv is in
// adoptJobEnvironment. The hub would compile any environment a node
// asks for -- it checks the identity in the request and not the
// environment -- so the node's own controls are the only thing standing
// between a job and an environment's pillar, and a job must not get past
// them by naming it here rather than in the node's configuration.
//
// Refused rather than ignored in agentless mode, where the pillar is the
// one the hub compiled before the run and sent with it.
func (n *node) jobPillarEnv(j *job.Job) (string, error) {
	raw, present := j.Kwarg["pillarenv"]
	if !present || raw == nil {
		return "", nil
	}
	env, ok := raw.(string)
	if !ok {
		return "", &job.Refusal{
			Reason: job.ReasonMalforme,
			Detail: fmt.Sprintf("the job's pillarenv is %s %v, and a pillar environment is named by a string; "+
				"refused rather than running with this node's own pillar", value.TypeName(value.FromJSON(raw)), raw),
		}
	}
	if env == "" {
		return "", nil
	}
	// Not job.Refusal's "malformed": the job is well formed, and this
	// node's configuration is what will not have it.
	if err := checkEnvPermitted(n.cfg, env); err != nil {
		return "", fmt.Errorf("the job's pillarenv: %w", err)
	}
	if n.pillarPushed {
		return "", fmt.Errorf("the job asks for pillarenv=%s, and an agentless run has only the pillar the hub "+
			"compiled and sent with it; refused rather than running with that one", env)
	}
	return env, nil
}

// runPillarIn is a run's pillar from the named pillar environment: that
// environment alone, as Salt compiles a pillar with pillarenv set, and
// asked of the hub when there is one, as every pillar is.
//
// Not through compilePillarOrErr, which is the one place a pillar's
// quiesce is recorded: this is the job's pillar and not the node's, and
// the environment is the job's choice, so its pillar can neither quiesce
// the node nor lift a quiesce the node's own pillar set (5.703).
func (n *node) runPillarIn(env string) (*value.Map, error) {
	if n.hubPillar != nil {
		return n.hubPillar(env, false)
	}
	return n.compileLocalPillarIn(env, false)
}

// runStateJob compiles and, for the applying forms, runs.
func (n *node) runStateJob(j *job.Job, fn string) (outcome, error) {
	// A beacon with `disable_during_state_run` is suppressed for as
	// long as this lasts, which is what stops a state run from firing
	// the beacon that triggers the state run. SPEC 16.3.
	n.enterStateRun()
	defer n.leaveStateRun()

	override, err := jobPillarOverride(j)
	if err != nil {
		return outcome{}, err
	}
	pillarEnv, err := n.jobPillarEnv(j)
	if err != nil {
		return outcome{}, err
	}
	// The node's own pillar first, even for a run that will use another
	// environment's: it is what says whether the node is quiesced, and a
	// job admitted before the node read a newly quiescing pillar must
	// still be stopped by it.
	p, err := n.compilePillarOrErr()
	if err != nil {
		return outcome{}, err
	}
	if err := n.quiesceAfterPillar(j); err != nil {
		return outcome{}, err
	}
	if pillarEnv != "" && (pillarEnv != n.pillarEnv || n.pillarAll) {
		n.log.Info("this run's pillar is compiled from the pillar environment the job names",
			"jid", string(j.JID), "fun", j.Fun, "pillarenv", pillarEnv)
		if p, err = n.runPillarIn(pillarEnv); err != nil {
			return outcome{}, err
		}
	}
	// After both of the above, and not inside compilePillarOrErr, which
	// is the one place a pillar's quiesce is recorded: an override is
	// this run's data, not the node's pillar, so it can neither quiesce
	// the node (and leave a record that outlives the run) nor lift a
	// quiesce the compiled pillar set.
	if override != nil {
		// The keys and not the values: an override is as likely as a
		// pillar file to carry a password.
		n.log.Info("this run's pillar is the node's with the job's override merged over it",
			"jid", string(j.JID), "fun", j.Fun, "keys", strings.Join(override.StringKeys(), ","))
		p = n.mergePillarOverride(p, override)
	}
	compiler := n.stateCompiler(p, string(j.JID))

	compileStarted := time.Now()
	compiled := compiler.CompileHighstate()
	if len(j.Arg) > 0 && fn != "highstate" {
		compiled = compiler.CompileSLS(j.Arg)
	}
	// Timed whether or not it succeeded: a compilation that is getting
	// slower until it fails is the one worth seeing coming.
	n.metrics.observeStateCompile(time.Since(compileStarted))
	for _, w := range compiled.RenderWarnings {
		n.log.Warn(w.String(), "component", "render", "jid", string(j.JID))
	}
	if err := compiled.Err(); err != nil {
		return outcome{}, err
	}

	switch fn {
	case "show_lowstate":
		// Masked, as the command line's `state show_*` is (DIVERGENCE
		// 5.556): the compiled states carry what the tree carries, a
		// password in `contents:` or a token in a command, and this value
		// goes back to the hub and into its job cache. The applying forms
		// below set `result.Secrets` for the same reason. DIVERGENCE 5.557.
		return outcome{value: maskValue(renderLow(compiled), n.secrets), success: true}, nil
	case "show_highstate", "show_sls":
		return outcome{value: maskValue(renderHigh(compiled), n.secrets), success: true}, nil
	case "show_states", "show_top":
		names := make([]any, len(compiled.SLS))
		for i, s := range compiled.SLS {
			names[i] = s
		}
		return outcome{value: names, success: true}, nil
	}

	runStarted := time.Now()
	result := (&runner.Runner{
		States:   n.registry.States,
		Exec:     n.registry.Exec,
		Ctx:      n.contextFor(p, string(j.JID)),
		FailHard: n.cfg.Bool("failhard", false),
		Tracer:   n.tracer,
	}).Run(compiled.Low)
	n.metrics.observeStateRun(time.Since(runStarted))
	result.Secrets = n.secrets

	code := result.RetCode()
	return outcome{
		value: result.Returns(),
		// SPEC 11.8: 0 is success and 2 is "nothing to do", which is
		// also success. Only a real failure is a failure.
		success: code == 0 || code == 2,
		retcode: code,
		format:  "highstate",
	}, nil
}

// executor is the node's job queue: SPEC 9.6, one job at a time, with a
// bounded queue between the stream reader and the worker.
//
// Bounded because the alternative is a node under a reactor storm
// growing until it is killed, which is what Salt's does. A full queue
// is refused and said out loud.
//
// Every job this node runs as an agent comes through it: the hub's, by
// Offer, and the node's own schedule, by runLocal. The schedule used to
// call executeJob from its own goroutine, so a scheduled highstate ran
// alongside a dispatched one on a node that said it ran one job at a time
// (issue 781). The queue is a slice under a mutex rather than a channel,
// because Cancel has to take one job out of the middle of it.
type executor struct {
	node  *node
	guard *job.Guard
	// signed is the on-disk record of the signed jobs this node has
	// accepted, which outlives the process and is never evicted by
	// count. See job.SignedReplay.
	signed  *job.SignedReplay
	returns func(*job.Return)

	depth   int
	mu      sync.Mutex
	waiting []*pending
	running int
	// exclusive is true while a job that holds the whole node runs.
	exclusive bool
	// families are the module families a parallel job holds, SPEC 9.6's
	// named locks: one `pkg` job at a time, beside one `service` job.
	families map[string]bool
	// wake tells Run to look at the queue again: a job arrived, or one
	// finished. Buffered by one, so a signal is never lost and never
	// blocks the sender.
	wake chan struct{}
}

// pending is one job waiting for the executor, and where its return goes.
type pending struct {
	job *job.Job
	// finish receives the return. For a job from the hub it is the queue
	// to the hub; for a scheduled job it is the schedule, waiting for it.
	finish func(*job.Return)
	// local is a job this node raised itself. It has no acceptance
	// record in the evidence chain, so it gets no result record either:
	// a result paired with nothing would read as a job the chain lost.
	local bool
	// exclusive and family are how the job may share the node, settled
	// when it is queued. See schedulingOf.
	exclusive bool
	family    string
}

func newExecutor(n *node, depth int, returns func(*job.Return)) *executor {
	if depth <= 0 {
		depth = 16
	}
	return &executor{
		node:     n,
		guard:    job.NewGuard(job.DefaultGuardSize),
		signed:   job.NewSignedReplay(n.signedReplayPath()),
		returns:  returns,
		depth:    depth,
		wake:     make(chan struct{}, 1),
		families: map[string]bool{},
	}
}

// signedReplayPath is where job.SignedReplay keeps its record: under
// `state_dir`, beside the evidence chain, because it has to survive what
// the evidence chain survives.
func (n *node) signedReplayPath() string {
	if n.cfg == nil {
		return filepath.Join(config.DefaultStateDir, "signed-jobs")
	}
	return filepath.Join(n.cfg.String("state_dir", config.DefaultStateDir), "signed-jobs")
}

// ErrQueueFull is what a node says instead of growing.
var ErrQueueFull = errors.New("this node's job queue is full")

// Offer puts a job on the queue after the replay checks of SPEC 6.3.
//
// SPEC 25.7's acceptance record is written here, and the position is the
// whole of it: after the replay checks, so that a job refused as a replay
// is not recorded as accepted, and before the job reaches the queue, so
// that the record exists before anything can run. A record written after
// execution is missing exactly the jobs that stopped the node, which are
// the ones an investigation is about.
//
// The refusals are recorded by node.refuse, which every path that will
// not run a job goes through -- including a queue that is full, which is
// a job recorded as accepted and then refused. Two records rather than
// one amended record, because a chain that went back and changed an entry
// would not be append-only.
//
// The order inside is the fix for DIVERGENCE 5.328. The replay cache used
// to record a job before its signature was checked, so a job refused for
// having no signature still took two places in it; a hub could send a few
// thousand of those and push a real signed job's entry out, then send that
// job again. So the cache is consulted first and written last: Check
// refuses a replay before anything expensive, the signature is checked,
// a signed job is written to the on-disk record, and only then does the
// job take a place in the in-memory cache.
func (e *executor) Offer(j *job.Job) error {
	if err := e.guard.Check(j); err != nil {
		e.node.metrics.countRefusal(err)
		return err
	}
	// SPEC 25.6, after the replay checks and before the record: a job
	// this node will not run unsigned is refused rather than accepted,
	// and the evidence record says which key authorised the ones it did
	// run.
	// A signed jid this node has already accepted is refused here, before
	// the signature check, which can mean compiling pillar: a hub
	// replaying a signed job in a loop should cost a map lookup each time.
	if j.Signature != "" && e.signed.Seen(j.JID) {
		err := &job.Refusal{Reason: job.ReasonReplayed,
			Detail: fmt.Sprintf("this node has already accepted the signed job %s", j.JID)}
		e.node.metrics.countRefusal(err)
		return err
	}
	signer, err := e.node.checkJobSignature(j)
	if err != nil {
		e.node.metrics.countRefusal(err)
		return err
	}
	if signer != "" {
		// Keyed by the jid, which the signature covers, rather than the
		// nonce, which it does not and the hub chooses. On disk and
		// synced before the job can be queued, so that neither a restart
		// nor a flood of other jobs makes this node forget it.
		if err := e.signed.Admit(j); err != nil {
			e.node.metrics.countRefusal(err)
			return err
		}
	}
	if err := e.guard.Admit(j); err != nil {
		// Only a job that raced its own replay past Check gets here.
		e.node.metrics.countRefusal(err)
		return err
	}
	// Before the acceptance record, so that a job carrying a `test` this
	// node will not interpret is recorded as refused rather than as
	// accepted and then failed. executeJob checks again, for the paths
	// that do not come through here (a schedule, agentless mode).
	//
	// After the replay records, not before them. A signed job refused
	// here has had its signature checked, so its jid is spent: the same
	// signed bytes would be refused the same way, and leaving the jid
	// open would let a hub offer it again, which is what the record is
	// for. The expiry bound on a signed job is enforced earlier, in
	// checkJobSignature, so the record still holds no entry for longer
	// than job_signature_max_ttl and a day.
	if _, err := jobTestFlag(j); err != nil {
		e.node.metrics.countRefusal(err)
		return err
	}
	// A pillar override this node cannot use, for the same reason and
	// with the same second check in runStateJob. Only a state function
	// reads one; to an execution function `pillar` is an ordinary keyword
	// argument and is the module's to judge.
	if _, isState := stateFunction(j.Fun); isState {
		if _, err := jobPillarOverride(j); err != nil {
			e.node.metrics.countRefusal(err)
			return err
		}
		// And a pillarenv it will not read from, the same way.
		if _, err := e.node.jobPillarEnv(j); err != nil {
			e.node.metrics.countRefusal(err)
			return err
		}
	}
	// Quiesce, in the same place and for the same reason as the test
	// flag: after the replay records, so a refused jid is spent and a hub
	// cannot hold a job until the node is lifted and then deliver it as
	// though it were new, and before the acceptance record, so a quiesced
	// node's chain says refused rather than accepted. Every job off the
	// stream comes through here -- dispatched live, queued for a node that
	// was away, or resent after a reconnect -- so this is the one check
	// for all three. A schedule does not, and checks for itself.
	if err := e.node.quiesceRefusal(j.Fun); err != nil {
		e.node.metrics.countRefusal(err)
		return err
	}
	e.node.metrics.countAccepted()
	e.node.recordJobAccepted(j, signer)
	if err := e.enqueue(&pending{job: j, finish: e.returns}); err != nil {
		// SPEC 26.2 asks for a counter behind every bounded queue's
		// overflow. This is the node's, and it was a refusal with
		// nothing counting it.
		e.node.metrics.countRefusal(err)
		return err
	}
	return nil
}

// refusal turns a structured refusal into the return SPEC 6.3 requires:
// a node that drops a job it will not run leaves an operator watching
// for something that is never coming.
func refusalReturn(n *node, j *job.Job, err error) *job.Return {
	encoded, _ := value.EncodeJSON(value.MapOf("refused", err.Error()), 0)
	return &job.Return{
		JID:         j.JID,
		NodeID:      n.nodeID,
		Fun:         j.Fun,
		FunArgs:     j.Arg,
		Success:     false,
		RetCode:     1,
		Return:      encoded,
		StartTime:   time.Now().UTC().Format(time.RFC3339Nano),
		NodeVersion: version.String(),
		Schema:      job.ReturnSchema,
	}
}
