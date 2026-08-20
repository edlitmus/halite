// Package runner executes a compiled low state.
//
// The compiler decided what runs and in what order. This package decides
// whether each chunk runs at all — requisite gating, the universal
// unless/onlyif/creates gates, the prereq two-phase run — and carries the
// result forward so the next chunk can see it.
package runner

import (
	"fmt"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// StateResult is one chunk's outcome.
type StateResult struct {
	Chunk  *state.Chunk
	Result states.Result
	// RunNum is the position in the run, which the return schema exposes
	// as __run_num__.
	RunNum    int
	StartTime time.Time
	Duration  time.Duration
	// Skipped marks a chunk a requisite or a gate held back.
	Skipped bool
}

// RunResult is the whole run.
type RunResult struct {
	Results []*StateResult
	// Aborted records that failhard stopped the run early.
	Aborted bool
	// AbortedBy names the chunk that triggered failhard.
	AbortedBy string
	Started   time.Time
	Duration  time.Duration
}

// Failed reports whether any state failed.
func (r *RunResult) Failed() bool {
	for _, res := range r.Results {
		if res.Result.Failed() {
			return true
		}
	}
	return false
}

// Changed reports whether any state made changes.
func (r *RunResult) Changed() bool {
	for _, res := range r.Results {
		if res.Result.HasChanges() {
			return true
		}
	}
	return false
}

// RetCode is the process exit status of SPEC section 11.4: 0 all
// succeeded, 1 one or more failed, 2 succeeded with no changes required.
func (r *RunResult) RetCode() int {
	switch {
	case r.Failed():
		return 1
	case !r.Changed():
		return 2
	default:
		return 0
	}
}

// Runner executes a low state.
type Runner struct {
	States *states.Registry
	Exec   *exec.Registry
	// Ctx is the module context. Test is read from it.
	Ctx *exec.Context
	// FailHard aborts the run on the first failure when no per-state
	// option says otherwise.
	FailHard bool
	// Progress is called before each chunk runs, for a live display.
	Progress func(chunk *state.Chunk, index, total int)
	// Now is the clock, overridable for a test.
	Now func() time.Time
	// Sleep is how a retry waits, overridable for a test.
	Sleep func(time.Duration)
}

// Run executes the ordered chunks.
func (r *Runner) Run(chunks []*state.Chunk) *RunResult {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Sleep == nil {
		r.Sleep = time.Sleep
	}

	out := &RunResult{Started: r.Now()}
	byIndex := make(map[*state.Chunk]*StateResult, len(chunks))

	// A requisite names a chunk by its position in the compiled slice, so
	// the lookup is by index into the same slice.
	results := make([]*StateResult, len(chunks))

	for i, ch := range chunks {
		if r.Progress != nil {
			r.Progress(ch, i, len(chunks))
		}

		sr := &StateResult{Chunk: ch, RunNum: len(out.Results), StartTime: r.Now()}
		results[i] = sr
		byIndex[ch] = sr

		if out.Aborted {
			sr.Skipped = true
			sr.Result = states.False(fmt.Sprintf(
				"This state was not attempted: the run was aborted by failhard on %s.", out.AbortedBy))
			out.Results = append(out.Results, sr)
			continue
		}

		decision := r.evaluateRequisites(ch, chunks, results)
		if decision.skip {
			sr.Skipped = true
			sr.Result = decision.result
			sr.Duration = r.Now().Sub(sr.StartTime)
			out.Results = append(out.Results, sr)
			continue
		}

		if gate, held := r.evaluateGates(ch); held {
			sr.Skipped = true
			sr.Result = gate
			sr.Duration = r.Now().Sub(sr.StartTime)
			out.Results = append(out.Results, sr)
			continue
		}

		sr.Result = r.execute(ch, decision.watchFired)
		sr.Duration = r.Now().Sub(sr.StartTime)
		out.Results = append(out.Results, sr)

		if sr.Result.Failed() && r.failHardFor(ch) {
			out.Aborted = true
			out.AbortedBy = ch.ID
		}
	}

	r.runListeners(chunks, results, out)
	out.Duration = r.Now().Sub(out.Started)
	return out
}

// decision is what requisite evaluation concluded about one chunk.
type decision struct {
	skip   bool
	result states.Result
	// watchFired means a watch requisite saw changes, so the module's
	// mod_watch reaction runs in place of its normal function.
	watchFired bool
}

// evaluateRequisites applies every requisite kind's gating rule.
func (r *Runner) evaluateRequisites(ch *state.Chunk, chunks []*state.Chunk, results []*StateResult) decision {
	var (
		sawOnChanges, onChangesSatisfied bool
		sawOnFail, onFailSatisfied       bool
		watchFired                       bool
	)

	for _, req := range ch.Reqs {
		targets := req.Resolved
		if len(targets) == 0 {
			continue
		}

		switch req.Kind {
		case state.Require, state.Watch:
			for _, idx := range targets {
				res := results[idx]
				if res == nil {
					continue
				}
				if res.Result.Failed() {
					return decision{skip: true, result: states.False(fmt.Sprintf(
						"This state was skipped because %s failed.", describe(chunks[idx])))}
				}
			}
			if req.Kind == state.Watch && anyChanged(targets, results) {
				watchFired = true
			}

		case state.RequireAny, state.WatchAny:
			if !anySucceeded(targets, results) {
				return decision{skip: true, result: states.False(fmt.Sprintf(
					"This state was skipped because none of %s succeeded.", describeAll(chunks, targets)))}
			}
			if req.Kind == state.WatchAny && anyChanged(targets, results) {
				watchFired = true
			}

		case state.OnChanges:
			sawOnChanges = true
			if allChanged(targets, results) {
				onChangesSatisfied = true
			}

		case state.OnChangesAny:
			sawOnChanges = true
			if anyChanged(targets, results) {
				onChangesSatisfied = true
			}

		case state.OnFail:
			sawOnFail = true
			if anyFailed(targets, results) {
				onFailSatisfied = true
			}

		case state.OnFailAny:
			sawOnFail = true
			if anyFailed(targets, results) {
				onFailSatisfied = true
			}

		case state.OnFailAll:
			sawOnFail = true
			if allFailed(targets, results) {
				onFailSatisfied = true
			}

		case state.Prereq:
			// The target is run in test mode only, and its result is
			// discarded except for the changes prediction. SPEC 11.5.
			if !r.prereqWouldChange(chunks, targets) {
				return decision{skip: true, result: states.True(fmt.Sprintf(
					"This state was skipped because %s would make no changes.", describeAll(chunks, targets)))}
			}

		case state.Listen, state.Use:
			// listen runs at the end of the run; use was applied at
			// compile time.
		}
	}

	// onchanges and onfail skip with a success rather than a failure,
	// because not needing to run is not a fault.
	if sawOnChanges && !onChangesSatisfied {
		return decision{skip: true, result: states.True(
			"This state was skipped because none of its onchanges requisites reported changes.")}
	}
	if sawOnFail && !onFailSatisfied {
		return decision{skip: true, result: states.True(
			"This state was skipped because none of its onfail requisites failed.")}
	}
	return decision{watchFired: watchFired}
}

// prereqWouldChange runs a prereq target in test mode and reports whether
// it predicted changes. The result is discarded, which is why the run is
// safe: nothing is applied.
func (r *Runner) prereqWouldChange(chunks []*state.Chunk, targets []int) bool {
	for _, idx := range targets {
		target := chunks[idx]
		testCtx := *r.Ctx
		testCtx.Test = true
		res, err := r.States.Call(&testCtx, target.Func(), target.Args)
		if err != nil {
			// A prereq target that will not even evaluate is treated as
			// "would change", so the dependent state still runs and the
			// real failure surfaces where it belongs.
			return true
		}
		if res.Result == nil || res.HasChanges() {
			return true
		}
	}
	return false
}

// evaluateGates applies the universal unless, onlyif, and creates options.
func (r *Runner) evaluateGates(ch *state.Chunk) (states.Result, bool) {
	for _, path := range ch.Opts.Creates {
		if fileExists(path) {
			return states.True(fmt.Sprintf("This state was skipped because %s already exists.", path)), true
		}
	}
	for _, cond := range ch.Opts.Unless {
		ok, err := r.evalCondition(ch, cond)
		if err != nil {
			return states.False(fmt.Sprintf("The unless condition could not be evaluated: %v", err)), true
		}
		if ok {
			return states.True("This state was skipped because its unless condition was met."), true
		}
	}
	if len(ch.Opts.OnlyIf) > 0 {
		for _, cond := range ch.Opts.OnlyIf {
			ok, err := r.evalCondition(ch, cond)
			if err != nil {
				return states.False(fmt.Sprintf("The onlyif condition could not be evaluated: %v", err)), true
			}
			if !ok {
				return states.True("This state was skipped because its onlyif condition was not met."), true
			}
		}
	}
	return states.Result{}, false
}

// evalCondition evaluates one unless or onlyif entry.
//
// The structured form is preferred and the string form is retained for
// compatibility, because the string form means a shell. SPEC section 11.7.
func (r *Runner) evalCondition(ch *state.Chunk, cond any) (bool, error) {
	switch t := cond.(type) {
	case string:
		res, err := r.Ctx.Run(exec.Command{
			Argv:           []string{t},
			Shell:          true,
			RunAs:          ch.Opts.RunAs,
			Umask:          ch.Opts.Umask,
			IgnoreExitCode: true,
		})
		if err != nil {
			return false, err
		}
		return res.Code == 0, nil

	case *value.Map:
		fnName := value.KeyString(mustGet(t, "fun"))
		if fnName == "" {
			return false, fmt.Errorf("a structured condition needs a `fun` naming a module function")
		}
		callArgs := value.NewMap(4)
		if kw, ok := t.Get("kwargs"); ok {
			if m, ok := kw.(*value.Map); ok {
				for _, e := range m.Entries() {
					callArgs.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
				}
			}
		}
		var positional []any
		if a, ok := t.Get("args"); ok {
			if list, ok := a.([]any); ok {
				positional = list
			}
		}
		out, err := r.Exec.CallPositional(r.Ctx, fnName, positional, callArgs)
		if err != nil {
			return false, err
		}
		return value.Truthy(out), nil

	case bool:
		return t, nil
	}
	return false, fmt.Errorf("a condition must be a command string, a list, or a `{fun: ...}` mapping, found %s",
		value.TypeName(cond))
}

// chunkContext is the module context with the chunk's per-state execution
// options bound to it.
func (r *Runner) chunkContext(ch *state.Chunk) *exec.Context {
	if ch.Opts.RunAs == "" && ch.Opts.Umask == "" {
		return r.Ctx
	}
	c := *r.Ctx
	c.RunAs = ch.Opts.RunAs
	c.Umask = ch.Opts.Umask
	return &c
}

func mustGet(m *value.Map, key string) any {
	v, _ := m.Get(key)
	return v
}

// execute runs one chunk, applying the retry loop and the check_cmd.
func (r *Runner) execute(ch *state.Chunk, watchFired bool) states.Result {
	// The chunk's own runas and umask apply to every command the state
	// runs, not only to its unless and onlyif conditions. SPEC section
	// 11.7 lists both as per-state options, and an option that governs
	// the conditions but not the state itself is the wrong half.
	ctx := r.chunkContext(ch)
	call := func() (states.Result, error) {
		if watchFired {
			return r.States.CallWatch(ctx, ch.Func(), ch.Args)
		}
		return r.States.Call(ctx, ch.Func(), ch.Args)
	}

	res, err := call()
	if err != nil {
		res = states.False(fmt.Sprintf("The state raised an error: %v", err))
	}

	if retry := ch.Opts.Retry; retry != nil {
		for attempt := 1; attempt < retry.Attempts; attempt++ {
			if res.Succeeded() == retry.Until {
				break
			}
			wait := retry.Interval + splay(retry.Splay)
			r.Sleep(wait)
			res, err = call()
			if err != nil {
				res = states.False(fmt.Sprintf("The state raised an error: %v", err))
			}
		}
	}

	if len(ch.Opts.CheckCmd) > 0 && res.Succeeded() {
		res = r.applyCheckCmd(ch, res)
	}
	if res.Name == "" {
		res.Name = ch.Name
	}
	return res
}

// applyCheckCmd runs the check_cmd commands and lets them decide the
// result.
func (r *Runner) applyCheckCmd(ch *state.Chunk, res states.Result) states.Result {
	for _, cmd := range ch.Opts.CheckCmd {
		out, err := r.Ctx.Run(exec.Command{
			Argv:           []string{cmd},
			Shell:          true,
			RunAs:          ch.Opts.RunAs,
			Umask:          ch.Opts.Umask,
			IgnoreExitCode: true,
		})
		if err != nil {
			return states.False(fmt.Sprintf("The check_cmd %q could not be run: %v", cmd, err))
		}
		if out.Code != 0 {
			failed := states.False(fmt.Sprintf("The check_cmd %q failed with exit status %d.", cmd, out.Code))
			failed.Changes = res.Changes
			return failed
		}
	}
	return res
}

// runListeners runs the `listen` reactions at the end of the run, which is
// what distinguishes listen from watch.
func (r *Runner) runListeners(chunks []*state.Chunk, results []*StateResult, out *RunResult) {
	if out.Aborted {
		return
	}
	for i, ch := range chunks {
		var fire bool
		for _, req := range ch.Reqs {
			if req.Kind != state.Listen {
				continue
			}
			if anyChanged(req.Resolved, results) {
				fire = true
			}
		}
		if !fire {
			continue
		}
		sr := &StateResult{Chunk: ch, RunNum: len(out.Results), StartTime: r.Now()}
		res, err := r.States.CallWatch(r.Ctx, ch.Func(), ch.Args)
		if err != nil {
			res = states.False(fmt.Sprintf("The listen reaction raised an error: %v", err))
		}
		if res.Name == "" {
			res.Name = ch.Name
		}
		res.Comment = "Listening reaction, run at the end of the state run. " + res.Comment
		sr.Result = res
		sr.Duration = r.Now().Sub(sr.StartTime)
		out.Results = append(out.Results, sr)
		_ = i
	}
}

func (r *Runner) failHardFor(ch *state.Chunk) bool {
	if ch.Opts.FailHard != nil {
		return *ch.Opts.FailHard
	}
	return r.FailHard
}

// ---- requisite predicates ----

func anyChanged(targets []int, results []*StateResult) bool {
	for _, idx := range targets {
		if idx < len(results) && results[idx] != nil && results[idx].Result.HasChanges() {
			return true
		}
	}
	return false
}

func allChanged(targets []int, results []*StateResult) bool {
	if len(targets) == 0 {
		return false
	}
	for _, idx := range targets {
		if idx >= len(results) || results[idx] == nil || !results[idx].Result.HasChanges() {
			return false
		}
	}
	return true
}

func anySucceeded(targets []int, results []*StateResult) bool {
	for _, idx := range targets {
		if idx < len(results) && results[idx] != nil && results[idx].Result.Succeeded() && !results[idx].Skipped {
			return true
		}
	}
	return false
}

func anyFailed(targets []int, results []*StateResult) bool {
	for _, idx := range targets {
		if idx < len(results) && results[idx] != nil && results[idx].Result.Failed() {
			return true
		}
	}
	return false
}

func allFailed(targets []int, results []*StateResult) bool {
	if len(targets) == 0 {
		return false
	}
	for _, idx := range targets {
		if idx >= len(results) || results[idx] == nil || !results[idx].Result.Failed() {
			return false
		}
	}
	return true
}

func describe(ch *state.Chunk) string {
	return fmt.Sprintf("%s (%s)", ch.ID, ch.Func())
}

func describeAll(chunks []*state.Chunk, targets []int) string {
	parts := make([]string, 0, len(targets))
	for _, idx := range targets {
		if idx < len(chunks) {
			parts = append(parts, describe(chunks[idx]))
		}
	}
	return strings.Join(parts, ", ")
}
