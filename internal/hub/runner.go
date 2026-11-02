package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// RunnerContext is what a runner is given: the hub it runs on, who
// asked, and the arguments already bound against the signature.
//
// A runner runs *on* the hub, with the hub's own reach over the key
// store, the job cache, and the file server. That is why SPEC 23.5
// grants runners through their own `runners:` list rather than through
// `functions:` -- a grant to run `test.ping` across the fleet and a
// grant to call `key.accept` on the hub are not the same grant, and
// Salt's `external_auth` conflating them is how a `@runner` grant
// becomes more than its holder expected.
type RunnerContext struct {
	Ctx       context.Context
	Server    *Server
	Principal string
	JID       job.ID
	Args      *value.Map
	// Scope is the nodes the caller may reach through this runner.
	Scope policy.Scope
}

// Visible reports whether a node is inside the caller's scope, which a
// ReachNodes runner asks of everything it would list.
func (c *RunnerContext) Visible(node string) bool { return c.Scope.Covers(node) }

// arg reads a bound argument as a string.
func (c *RunnerContext) arg(name string) string {
	v, ok := c.Args.Get(name)
	if !ok || v == nil {
		return ""
	}
	return value.KeyString(v)
}

// argInt reads a bound argument as an integer.
func (c *RunnerContext) argInt(name string) int {
	v, ok := c.Args.Get(name)
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	var n int
	fmt.Sscanf(value.KeyString(v), "%d", &n)
	return n
}

// argBool reads a bound argument as a boolean.
func (c *RunnerContext) argBool(name string) bool {
	v, ok := c.Args.Get(name)
	if !ok {
		return false
	}
	return value.Truthy(v)
}

// RunnerFunc is one runner function.
type RunnerFunc func(*RunnerContext) (any, error)

// RunnerModule is a runner together with its signature, mirroring
// exec.Module so that the two registries read the same way.
type RunnerModule struct {
	Sig signature.Signature
	Fn  RunnerFunc
	// Pending says when a declared-but-unbuilt runner arrives, and is
	// the whole message an operator gets. SPEC 19.2 is an inventory,
	// and a name missing from the registry is indistinguishable from a
	// typo; a name that answers "manage.bootstrap arrives in phase 5"
	// is not.
	Pending string
	// Reach is what the runner touches of the nodes, which decides how
	// the caller's scope applies to it (see Reach). Every built runner
	// declares one; Add refuses a runner that does not.
	Reach Reach
	// NodeArg names the argument that holds one node, for a ReachNodes
	// runner that takes one: a node outside the caller's scope is
	// refused before the runner runs. Empty, or the argument not given,
	// and the runner filters what it returns by RunnerContext.Visible.
	NodeArg string
	// TargetArg and TargetKindArg name the arguments that hold a target
	// expression and its kind: every node it matches must be in scope.
	TargetArg, TargetKindArg string
}

// Reach says what a runner touches of the nodes (issue 321, M-56).
//
// A runner runs on the hub with the hub's whole reach. Salt grants one
// with no target at all -- `spec_check` in salt/utils/minions.py consults
// none for `@runner` -- so a grant of `pillar.show_pillar` there reads
// any node's pillar whatever the caller's other rules name. halite
// confines a runner to the nodes the targets of the rules granting it
// cover (policy.Scope), and what that means depends on the runner, so
// each says.
type Reach int

const (
	// reachUnset is no declaration, which Add refuses.
	reachUnset Reach = iota
	// ReachHub runners touch nothing of any node: the file server,
	// sending an event, an error for a test.
	ReachHub
	// ReachDispatch runners reach nodes only by sending jobs, and every
	// job is authorized against the caller's function rules as it is
	// sent (DispatchAs), so the scope adds nothing.
	ReachDispatch
	// ReachNodes runners read or change what the hub holds about
	// particular nodes. A node or target named in NodeArg or TargetArg
	// must be in scope; what they list is filtered to it.
	ReachNodes
	// ReachEstate runners act on every node at once, with no node to
	// name: clearing every cache, pruning every job, minting a token any
	// new node can enroll with. They need a scope of `*`, written.
	ReachEstate
)

// Runners is the runner registry a hub serves.
type Runners struct {
	mods    map[string]RunnerModule
	fns     map[string]RunnerFunc
	pending map[string]string
	sigs    *signature.Registry
}

// NewRunners builds the registry this build ships. SPEC section 19.2.
func NewRunners() *Runners {
	r := &Runners{mods: map[string]RunnerModule{}, fns: map[string]RunnerFunc{}, pending: map[string]string{}, sigs: signature.NewRegistry()}
	registerJobsRunner(r)
	registerStateRunner(r)
	registerReactorRunner(r)
	registerMineRunner(r)
	registerManageRunner(r)
	registerKeyRunner(r)
	registerNodegroupsRunner(r)
	registerPillarRunner(r)
	registerCacheRunner(r)
	registerFileserverRunner(r)
	registerEventRunner(r)
	registerSaltutilRunner(r)
	registerSurveyRunner(r)
	registerErrorRunner(r)
	registerPendingRunners(r)
	return r
}

// Add registers runners. Registering the same name twice panics, for
// the reason exec.Registry.Add does: it can only happen while a build
// wires itself up, and the alternative is a function serving under a
// name whose signature belongs to something else.
func (r *Runners) Add(mods ...RunnerModule) {
	for _, m := range mods {
		name := m.Sig.Name()
		if _, dup := r.fns[name]; dup {
			panic("hub: runner " + name + " is registered twice")
		}
		fn := m.Fn
		if m.Pending != "" {
			phase := m.Pending
			r.pending[name] = phase
			fn = func(*RunnerContext) (any, error) {
				return nil, fmt.Errorf(
					"the %s runner is declared and not built yet: %s", name, phase)
			}
		}
		if fn == nil {
			panic("hub: runner " + name + " has no implementation")
		}
		if m.Reach == reachUnset && m.Pending == "" {
			panic("hub: runner " + name + " declares no Reach; say what it touches of the nodes")
		}
		r.fns[name] = fn
		r.mods[name] = m
		r.sigs.Add(m.Sig)
	}
}

// Signatures exposes the signature registry, which `runner list` and
// the API schema read.
func (r *Runners) Signatures() *signature.Registry { return r.sigs }

// Has reports whether a runner is registered, built or pending.
func (r *Runners) Has(name string) bool { _, ok := r.fns[name]; return ok }

// Pending reports why a declared-but-unbuilt runner is not built.
func (r *Runners) Pending(name string) (string, bool) {
	phase, ok := r.pending[name]
	return phase, ok
}

// Names lists every runner, sorted.
func (r *Runners) Names() []string { return r.sigs.Names() }

// nearMisses names the runners a caller may have meant.
func (r *Runners) nearMisses(name string) []string {
	module, _, _ := strings.Cut(name, ".")
	var out []string
	for _, n := range r.Names() {
		if strings.HasPrefix(n, module+".") {
			out = append(out, n)
		}
	}
	if len(out) > 0 {
		return out
	}
	return r.sigs.Modules()
}

// UnknownRunnerError names what the hub does serve.
type UnknownRunnerError struct {
	Name  string
	Known []string
}

func (e *UnknownRunnerError) Error() string {
	if len(e.Known) == 0 {
		return fmt.Sprintf("%s is not a runner this hub serves", e.Name)
	}
	return fmt.Sprintf("%s is not a runner this hub serves; it has %s",
		e.Name, strings.Join(e.Known, ", "))
}

// runners is the registry, built on first use so that a Server put
// together by hand in a test still serves them.
func (s *Server) runners() *Runners {
	s.runnersOnce.Do(func() {
		if s.Runners == nil {
			s.Runners = NewRunners()
		}
	})
	return s.Runners
}

// RunnerCall is one invocation.
type RunnerCall struct {
	Principal string
	Fun       string
	Arg       []string
	Kwarg     map[string]any
	// Narrow is transport.RunnerRequest.Narrow.
	Narrow *[]string
}

// RunnerOutcome is what came back.
//
// A runner that ran and failed is an outcome, not a transport error:
// `jobs.lookup_jid` on a job that does not exist has answered the
// question. Only a refusal, an unknown name, or a malformed call is an
// error here.
type RunnerOutcome struct {
	JID      job.ID
	Fun      string
	Success  bool
	Return   any
	Err      string
	Duration time.Duration
}

// CallRunner authorizes, records, and runs one runner.
func (s *Server) CallRunner(ctx context.Context, call RunnerCall) (*RunnerOutcome, error) {
	reg := s.runners()
	if !reg.Has(call.Fun) {
		return nil, &UnknownRunnerError{Name: call.Fun, Known: reg.nearMisses(call.Fun)}
	}

	// SPEC 23.5: the decision is logged either way, with the rule that
	// matched. `Runner: true` is what makes the policy read `runners:`
	// rather than `functions:`.
	decision := s.Policy.Authorize(
		policyRequestFor(call.Principal, call.Fun, "", "", call.Arg, call.Kwarg, true))
	s.countDecision(decision)
	if !decision.Allowed {
		s.warn("runner refused by policy",
			"principal", call.Principal, "fun", call.Fun, "reason", decision.Reason)
		return nil, &policyRefusal{reason: decision.Reason}
	}
	s.info("runner authorized",
		"principal", call.Principal, "fun", call.Fun,
		"role", decision.Role, "rule", decision.RuleIndex)

	sig, _ := reg.sigs.Lookup(call.Fun)
	args := make([]any, len(call.Arg))
	for i, a := range call.Arg {
		args[i] = a
	}
	kwargs := value.NewMap(len(call.Kwarg))
	for _, k := range sortedKeys(call.Kwarg) {
		kwargs.Set(k, value.FromJSON(call.Kwarg[k]))
	}
	bound, errs := sig.Bind(args, kwargs)
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("%s: %s", call.Fun, strings.Join(msgs, "; "))
	}

	scope := s.Policy.RunnerScope(call.Principal, call.Fun, false)
	if call.Narrow != nil {
		scope = scope.Within(policy.Scope{Targets: *call.Narrow})
	}
	if err := s.checkRunnerReach(reg.mods[call.Fun], scope, bound); err != nil {
		s.warn("runner refused: outside the caller's scope",
			"principal", call.Principal, "fun", call.Fun, "reason", err.Error())
		return nil, &policyRefusal{reason: err.Error()}
	}

	jid := s.clock().Next()
	started := s.now()
	s.emit(tagRunNew(string(jid)), "", map[string]any{
		"jid":       string(jid),
		"fun":       call.Fun,
		"arg":       call.Arg,
		"principal": call.Principal,
	})
	s.recordRunnerJob(jid, call, started)

	out := &RunnerOutcome{JID: jid, Fun: call.Fun, Success: true}
	ret, err := s.runGuarded(call.Fun, reg.fns[call.Fun], &RunnerContext{
		Ctx:       ctx,
		Server:    s,
		Principal: call.Principal,
		JID:       jid,
		Args:      bound,
		Scope:     scope,
	})
	out.Duration = s.now().Sub(started)
	// The value and the error both, when a runner produced both. An
	// orchestration that failed is the case: the run happened, its
	// timeline is the answer, and the failure is what the caller's exit
	// status has to reflect. Dropping the value on error would leave an
	// operator told to look at a timeline they were not given.
	out.Return = ret
	if err != nil {
		out.Success = false
		out.Err = err.Error()
	}

	s.emit(tagRunRet(string(jid)), "", map[string]any{
		"jid":     string(jid),
		"fun":     call.Fun,
		"success": out.Success,
		"error":   out.Err,
	})
	s.recordRunnerReturn(out, started)
	return out, nil
}

// runGuarded runs a runner and turns a panic in it into the error it is.
//
// `net/http` recovers a handler's panic for the request and nothing else, so a
// runner that panicked left what `CallRunner` had already written: the
// `halite/run/<jid>/new` event, and a job record in the state `dispatched`
// that nothing would ever close, with no `ret` event and no return. The caller
// got a dropped connection. It is now a failed run like any other -- the `ret`
// event, the return recorded as the failure it was, the caller told -- and the
// stack goes to the hub's log, where the bug that caused it is found
// (DIVERGENCE 5.527).
//
// `http.ErrAbortHandler` is passed on: it is the sentinel a handler panics
// with on purpose to abandon a response, and swallowing it would turn that into
// a success.
func (s *Server) runGuarded(fun string, fn func(*RunnerContext) (any, error), c *RunnerContext) (ret any, err error) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if r == http.ErrAbortHandler {
			panic(r)
		}
		s.warn("a runner panicked",
			"fun", fun, "principal", c.Principal, "panic", fmt.Sprint(r),
			"stack", string(debug.Stack()))
		ret, err = nil, fmt.Errorf("%s panicked: %v; the hub's log has the stack", fun, r)
	}()
	return fn(c)
}

// policyRefusal carries a denial out of CallRunner so the endpoint can
// answer 403 without inspecting the message.
type policyRefusal struct{ reason string }

func (e *policyRefusal) Error() string { return e.reason }

// recordRunnerJob writes the runner call into the job cache before it
// runs.
//
// Before, not after: SPEC 9.1 records the expected respondents ahead of
// delivery so that a job which never finishes is still visible, and a
// runner that wedges the hub is exactly the case where "what was it
// doing" has to be answerable from disk.
func (s *Server) recordRunnerJob(jid job.ID, call RunnerCall, started time.Time) {
	if s.Jobs == nil {
		return
	}
	err := s.Jobs.Put(&job.Job{
		JID:       jid,
		Fun:       call.Fun,
		Arg:       call.Arg,
		Kwarg:     call.Kwarg,
		Created:   started,
		Submitter: call.Principal,
		// A runner runs on the hub, so the hub is the only respondent.
		Target: RunnerTarget,
		Nodes:  []string{RunnerTarget},
		State:  job.Dispatched,
	})
	if err != nil {
		s.warn("could not record a runner job", "jid", string(jid), "error", err.Error())
	}
}

// RunnerTarget is the respondent recorded for a runner call. A runner
// has no node set, and leaving the field empty would make `jobs list`
// report a job nobody was expected to answer.
const RunnerTarget = "hub"

func (s *Server) recordRunnerReturn(out *RunnerOutcome, started time.Time) {
	if s.Jobs == nil {
		return
	}
	encoded, err := value.EncodeJSON(out.Return, 0)
	if err != nil {
		encoded = []byte(`null`)
	}
	if !out.Success && out.Return == nil {
		encoded, _ = json.Marshal(out.Err)
	}
	retcode := 0
	if !out.Success {
		retcode = 1
	}
	added, err := s.Jobs.AddReturn(&job.Return{
		JID:        out.JID,
		NodeID:     RunnerTarget,
		Fun:        out.Fun,
		FunArgs:    nil,
		Success:    out.Success,
		RetCode:    retcode,
		Return:     encoded,
		StartTime:  started.UTC().Format(time.RFC3339Nano),
		DurationMS: out.Duration.Milliseconds(),
		Schema:     job.ReturnSchema,
	})
	if err != nil {
		s.warn("could not record a runner return", "jid", string(out.JID), "error", err.Error())
		return
	}
	if added {
		s.completeIfDone(out.JID)
	}
}

// runnerCall is POST /v1/runners: an operator asking the hub to run one
// of its own functions.
func (s *Server) runnerCall(w http.ResponseWriter, r *http.Request, principal string) {
	var req transport.RunnerRequest
	if err := transport.ReadJSON(w, r, transport.MaxRequestBody, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if req.Fun == "" {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed,
			fmt.Errorf("a runner call needs a function, as module.function"))
		return
	}

	out, err := s.CallRunner(r.Context(), RunnerCall{
		Principal: principal,
		Fun:       req.Fun,
		Arg:       req.Arg,
		Kwarg:     req.Kwarg,
		Narrow:    req.Narrow,
	})
	switch e := err.(type) {
	case nil:
	case *policyRefusal:
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused, e)
		return
	case *UnknownRunnerError:
		transport.WriteError(w, http.StatusNotFound, transport.CodeMalformed, e)
		return
	default:
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}

	encoded, err := value.EncodeJSON(out.Return, 0)
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal,
			fmt.Errorf("%s returned something that will not encode: %w", out.Fun, err))
		return
	}
	transport.WriteJSON(w, http.StatusOK, transport.RunnerResponse{
		JID:        string(out.JID),
		Fun:        out.Fun,
		Success:    out.Success,
		Return:     encoded,
		Error:      out.Err,
		DurationMS: out.Duration.Milliseconds(),
	})
}

// sortedKeys orders a kwarg map so that binding, and the record on
// disk, do not depend on Go's map iteration order.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runnerArg declares a required runner argument, and runnerOpt an
// optional one. The pair keeps a declaration on one line without
// hiding what it says.
func runnerArg(name string, t signature.Type, doc string) signature.Param {
	return signature.Param{Name: name, Type: t, Required: true, Doc: doc}
}

func runnerOpt(name string, t signature.Type, def any, doc string) signature.Param {
	return signature.Param{Name: name, Type: t, Default: def, Doc: doc}
}

// runnerSig is the shorthand every runner declaration uses.
func runnerSig(module, function, doc, section string, params ...signature.Param) signature.Signature {
	return signature.Signature{
		Module:   module,
		Function: function,
		Doc:      doc,
		Params:   params,
		TestMode: signature.TestNotApplicable,
		Section:  section,
	}
}

// checkRunnerReach applies the caller's scope to what a runner is about
// to touch, before it runs.
func (s *Server) checkRunnerReach(m RunnerModule, scope policy.Scope, args *value.Map) error {
	switch m.Reach {
	case ReachHub, ReachDispatch:
		return nil
	case ReachEstate:
		if !scope.Everything() {
			return fmt.Errorf("%s acts on every node at once, and the rules granting it do not name "+
				"target '*'; it needs a runner rule with target: '*'", m.Sig.Name())
		}
		return nil
	case ReachNodes:
		if m.NodeArg != "" {
			if node := argString(args, m.NodeArg); node != "" && !scope.Covers(node) {
				return fmt.Errorf("%s may be called here, but not for %s: no rule granting it has a target "+
					"that covers that node", m.Sig.Name(), node)
			}
		}
		if m.TargetArg != "" {
			if expr := argString(args, m.TargetArg); expr != "" {
				nodes, err := s.runnerTargetNodes(expr, argString(args, m.TargetKindArg))
				if err != nil {
					return err
				}
				for _, n := range nodes {
					if !scope.Covers(n) {
						return fmt.Errorf("%s may be called here, but %q reaches %s, which no rule "+
							"granting it covers", m.Sig.Name(), expr, n)
					}
				}
			}
		}
		return nil
	}
	if m.Pending != "" {
		return nil
	}
	return fmt.Errorf("%s declares no reach, and is refused rather than guessed at", m.Sig.Name())
}

func argString(args *value.Map, name string) string {
	if args == nil {
		return ""
	}
	v, ok := args.Get(name)
	if !ok || v == nil {
		return ""
	}
	return value.KeyString(v)
}

// runnerTargetNodes is the nodes a runner's target expression matches,
// by the same compile and resolve dispatch uses, so that "every node it
// reaches" means for a runner what it means for a job.
func (s *Server) runnerTargetNodes(expr, kindFlag string) ([]string, error) {
	kind := target.Glob
	if kindFlag != "" {
		k, ok := target.KindFromFlag(kindFlag)
		if !ok {
			return nil, fmt.Errorf("%q is not a target kind", kindFlag)
		}
		kind = k
	}
	m, err := target.Compile(kind, expr, s.nodegroups())
	if err != nil {
		return nil, err
	}
	return s.resolve(m, "")
}
