package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/runner"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

// executeJob runs one job from the hub and produces its return.
//
// Nothing here calls Fatalf. A job that cannot be compiled, cannot be
// run, or names a function this build does not ship comes back as a
// failed return, because an agent that exits on a bad job is an agent
// an operator can stop with one typo.
func (n *node) executeJob(j *job.Job) *job.Return {
	// A job runs against a copy of the node, never against the agent's
	// own.
	//
	// The first cut mutated the agent: a job carrying `test: true` set
	// the flag, and every job after it on that node was a dry run for
	// ever after. An operator would have watched `run '*' state.apply`
	// report what it would do and believe it had done it. The same
	// applies to the environment a job names.
	n = n.forJob()
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
	defer func() {
		ret.DurationMS = time.Since(started).Milliseconds()
		// A module that panics must produce a failed return rather than
		// take the agent down: one bad job would otherwise stop a node
		// answering anything at all.
		if r := recover(); r != nil {
			fail(ret, fmt.Errorf("the job panicked: %v", r))
			n.log.Error("a job panicked", "jid", string(j.JID), "fun", j.Fun, "panic", fmt.Sprint(r))
		}
	}()

	// The job's environment, not this invocation's. A job for `staging`
	// compiled against `base` would apply the wrong tree and report
	// success.
	if err := n.adoptJobEnvironment(j); err != nil {
		return fail(ret, err)
	}
	// `test` on the job overrides the node's default in one direction
	// only: a hub asking for a dry run gets one, and a node configured
	// with `test: true` is not talked out of it.
	if test, ok := j.Kwarg["test"].(bool); ok && test {
		n.test = true
	}

	out, err := n.runFunction(j)
	if err != nil {
		return fail(ret, err)
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

// forJob is a shallow copy: the pointers -- configuration, registries,
// logger, redactor -- are shared deliberately, and the fields a job may
// change are not.
func (n *node) forJob() *node {
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
	n.files = fileserver.NewRoots(n.fileRootsFor(n.env))
	n.pillars = fileserver.NewRoots(n.pillarRootsFor(n.pillarEnv))
	return nil
}

// runFunction dispatches on the job's function name, the same two ways
// the command line does: a state function through the compiler, and
// anything else through the execution registry.
func (n *node) runFunction(j *job.Job) (outcome, error) {
	if fn, ok := stateFunction(j.Fun); ok {
		return n.runStateJob(j, fn)
	}
	p, err := n.compilePillarOrErr()
	if err != nil {
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
		kwargs.Set(k, v)
	}
	out, err := n.registry.Exec.CallPositional(n.contextFor(p, string(j.JID)), j.Fun, positional, kwargs)
	if err != nil {
		return outcome{}, err
	}
	return outcome{value: out, success: true, retcode: 0}, nil
}

// stateFunction reports whether a name is one the state compiler
// handles, and what to call it.
func stateFunction(fun string) (string, bool) {
	const prefix = "state."
	if len(fun) <= len(prefix) || fun[:len(prefix)] != prefix {
		return "", false
	}
	name := fun[len(prefix):]
	switch name {
	case "apply", "highstate", "sls", "show_top", "show_highstate", "show_sls", "show_lowstate", "show_states":
		return name, true
	}
	return "", false
}

// runStateJob compiles and, for the applying forms, runs.
func (n *node) runStateJob(j *job.Job, fn string) (outcome, error) {
	p, err := n.compilePillarOrErr()
	if err != nil {
		return outcome{}, err
	}
	compiler := n.stateCompiler(p, string(j.JID))

	compiled := compiler.CompileHighstate()
	if len(j.Arg) > 0 && fn != "highstate" {
		compiled = compiler.CompileSLS(j.Arg)
	}
	for _, w := range compiled.RenderWarnings {
		n.log.Warn(w.String(), "component", "render", "jid", string(j.JID))
	}
	if err := compiled.Err(); err != nil {
		return outcome{}, err
	}

	switch fn {
	case "show_lowstate":
		return outcome{value: renderLow(compiled), success: true}, nil
	case "show_highstate", "show_sls":
		return outcome{value: renderHigh(compiled), success: true}, nil
	case "show_states", "show_top":
		names := make([]any, len(compiled.SLS))
		for i, s := range compiled.SLS {
			names[i] = s
		}
		return outcome{value: names, success: true}, nil
	}

	result := (&runner.Runner{
		States:   n.registry.States,
		Exec:     n.registry.Exec,
		Ctx:      n.contextFor(p, string(j.JID)),
		FailHard: n.cfg.Bool("failhard", false),
	}).Run(compiled.Low)
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
type executor struct {
	node    *node
	guard   *job.Guard
	queue   chan *job.Job
	returns func(*job.Return)
}

func newExecutor(n *node, depth int, returns func(*job.Return)) *executor {
	if depth <= 0 {
		depth = 16
	}
	return &executor{
		node:    n,
		guard:   job.NewGuard(job.DefaultGuardSize),
		queue:   make(chan *job.Job, depth),
		returns: returns,
	}
}

// ErrQueueFull is what a node says instead of growing.
var ErrQueueFull = errors.New("this node's job queue is full")

// Offer puts a job on the queue after the replay checks of SPEC 6.3.
func (e *executor) Offer(j *job.Job) error {
	if err := e.guard.Admit(j); err != nil {
		return err
	}
	select {
	case e.queue <- j:
		return nil
	default:
		return ErrQueueFull
	}
}

// Run works the queue until the channel closes.
func (e *executor) Run(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case j := <-e.queue:
			e.node.log.Info("running a job", "jid", string(j.JID), "fun", j.Fun)
			ret := e.node.executeJob(j)
			e.node.log.Info("job finished",
				"jid", string(j.JID), "fun", j.Fun,
				"success", ret.Success, "retcode", ret.RetCode, "duration_ms", ret.DurationMS)
			e.returns(ret)
		}
	}
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
