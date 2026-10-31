package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// registerJobsRunner installs the `jobs` runner of SPEC section 19.2:
// what the job cache holds, read from the hub rather than from a node.
func registerJobsRunner(r *Runners) {
	r.Add(
		RunnerModule{
			Reach: ReachNodes,
			Sig: runnerSig("jobs", "list_jobs",
				"List recent jobs, most recent first.", "19.2",
				runnerOpt("limit", signature.Int, 20, "How many to list."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				jobs, err := c.jobs()
				if err != nil {
					return nil, err
				}
				list, err := c.visibleJobs(jobs, c.argInt("limit"))
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(list))
				for _, j := range list {
					out.Set(string(j.JID), c.scopedSummary(j))
				}
				return out, nil
			},
		},
		RunnerModule{
			Reach: ReachNodes,
			Sig: runnerSig("jobs", "list_job",
				"The record of one job, with its returns.", "19.2",
				runnerArg("jid", signature.String, "The job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) { return c.jobDetail(c.arg("jid")) },
		},
		RunnerModule{
			Reach: ReachNodes,
			Sig: runnerSig("jobs", "print_job",
				"The record of one job, with its returns. The same as list_job, "+
					"which is what Salt's two names amount to.", "19.2",
				runnerArg("jid", signature.String, "The job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) { return c.jobDetail(c.arg("jid")) },
		},
		RunnerModule{
			Reach: ReachNodes,
			Sig: runnerSig("jobs", "lookup_jid",
				"What each node returned for a job.", "19.2",
				runnerArg("jid", signature.String, "The job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				returns, err := c.returns(c.arg("jid"))
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(returns))
				for _, ret := range returns {
					out.Set(ret.NodeID, decodedReturn(ret))
				}
				return out, nil
			},
		},
		RunnerModule{
			Reach: ReachNodes,
			Sig: runnerSig("jobs", "active",
				"Jobs the hub is still expecting returns for.", "19.2",
				runnerOpt("limit", signature.Int, 50, "How many recent jobs to consider."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				jobs, err := c.jobs()
				if err != nil {
					return nil, err
				}
				list, err := jobs.List(c.argInt("limit"))
				if err != nil {
					return nil, err
				}
				out := value.NewMap(0)
				for _, j := range list {
					if j.State != job.Dispatched && j.State != job.Batching {
						continue
					}
					if !c.jobVisible(j) {
						continue
					}
					missing, err := jobs.Missing(j.JID)
					if err != nil {
						return nil, err
					}
					summary := c.scopedSummary(j)
					summary.Set("missing", stringList(c.visibleOnly(missing)))
					out.Set(string(j.JID), summary)
				}
				return out, nil
			},
		},
		RunnerModule{
			Reach: ReachNodes,
			Sig: runnerSig("jobs", "missing",
				"The nodes a job has not heard from.", "19.2",
				runnerArg("jid", signature.String, "The job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				jobs, err := c.jobs()
				if err != nil {
					return nil, err
				}
				id, err := c.visibleJID(c.arg("jid"))
				if err != nil {
					return nil, err
				}
				missing, err := jobs.Missing(id)
				if err != nil {
					return nil, err
				}
				return stringList(c.visibleOnly(missing)), nil
			},
		},
		RunnerModule{
			Reach: ReachNodes,
			Sig: runnerSig("jobs", "exit_success",
				"Whether every expected node returned success. A node that "+
					"never answered is not a success, which is the distinction "+
					"this exists to make.", "19.2",
				runnerArg("jid", signature.String, "The job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				jobs, err := c.jobs()
				if err != nil {
					return nil, err
				}
				// Over the nodes in the caller's scope: whether the
				// nodes it may see all succeeded, which is the question
				// it can be told the answer to.
				id, err := c.visibleJID(c.arg("jid"))
				if err != nil {
					return nil, err
				}
				missing, err := jobs.Missing(id)
				if err != nil {
					return nil, err
				}
				if len(c.visibleOnly(missing)) > 0 {
					return false, nil
				}
				returns, err := jobs.Returns(id)
				if err != nil {
					return nil, err
				}
				j, err := jobs.Get(id)
				if err != nil {
					return nil, err
				}
				seen := 0
				for _, ret := range returns {
					if !c.returnVisible(j, ret.NodeID) {
						continue
					}
					seen++
					if !ret.Success {
						return false, nil
					}
				}
				return seen > 0, nil
			},
		},
		RunnerModule{
			Reach: ReachEstate,
			Sig: runnerSig("jobs", "prune",
				"Drop cached jobs past the retention policy, and report how many.", "19.2"),
			Fn: func(c *RunnerContext) (any, error) {
				jobs, err := c.jobs()
				if err != nil {
					return nil, err
				}
				n, err := jobs.Prune()
				if err != nil {
					return nil, err
				}
				return int64(n), nil
			},
		},
	)
}

// registerSurveyRunner installs `survey`, which answers "did the fleet
// agree" without an operator reading several hundred returns.
func registerSurveyRunner(r *Runners) {
	r.Add(
		RunnerModule{
			Reach: ReachNodes,
			Sig: runnerSig("survey", "hash",
				"Group a job's nodes by the digest of what they returned.", "19.2",
				runnerArg("jid", signature.String, "The job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				groups, order, err := c.surveyGroups(c.arg("jid"))
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(order))
				for _, digest := range order {
					out.Set(digest, stringList(groups[digest].nodes))
				}
				return out, nil
			},
		},
		RunnerModule{
			Reach: ReachNodes,
			Sig: runnerSig("survey", "diff",
				"Group a job's nodes by what they returned, and show one "+
					"representative return per group.", "19.2",
				runnerArg("jid", signature.String, "The job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				groups, order, err := c.surveyGroups(c.arg("jid"))
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(order))
				for _, digest := range order {
					g := groups[digest]
					entry := value.NewMap(2)
					entry.Set("nodes", stringList(g.nodes))
					entry.Set("return", decodedReturn(g.sample))
					out.Set(digest, entry)
				}
				return out, nil
			},
		},
	)
}

// registerErrorRunner installs `error`, which exists so that an
// orchestration or a reaction can fail on purpose.
func registerErrorRunner(r *Runners) {
	r.Add(RunnerModule{
		Reach: ReachHub,
		Sig: runnerSig("error", "error",
			"Raise an error. Used by an orchestration that has decided to stop.", "19.2",
			runnerOpt("message", signature.String, "an error raised by error.error",
				"What the failure says."),
		),
		Fn: func(c *RunnerContext) (any, error) {
			return nil, fmt.Errorf("%s", c.arg("message"))
		},
	})
}

// ---- shared helpers ----

func (c *RunnerContext) jobs() (*job.Cache, error) {
	if c.Server.Jobs == nil {
		return nil, fmt.Errorf("this hub keeps no job cache, so there is nothing to read")
	}
	return c.Server.Jobs, nil
}

// scopedJobWindow is how many of the most recent jobs are looked through to
// find a scoped caller's jobs.
const scopedJobWindow = 5000

// visibleJobs is up to limit of the most recent jobs the caller may see.
//
// The limit used to be applied to the cache and the scope afterwards, so a
// caller scoped to a few nodes got the most recent N jobs of the whole estate
// cut down to its own: after 25 jobs on db1, `limit=20` listed nothing of a
// web-scoped caller's one job on web1, and "how many to list" was not what it
// said. A caller of every node is unchanged. A scoped one is looked for among
// the most recent scopedJobWindow, a bound and not a promise: a job older than
// that is not found. DIVERGENCE 5.503.
func (c *RunnerContext) visibleJobs(jobs *job.Cache, limit int) ([]*job.Job, error) {
	if c.Scope.Everything() {
		return jobs.List(limit)
	}
	window := scopedJobWindow
	if limit > window {
		window = limit
	}
	all, err := jobs.List(window)
	if err != nil {
		return nil, err
	}
	var out []*job.Job
	for _, j := range all {
		if !c.jobVisible(j) {
			continue
		}
		out = append(out, j)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func parseJID(s string) (job.ID, error) {
	id := job.ID(s)
	if !id.Valid() {
		return "", fmt.Errorf("%q is not a job identifier", s)
	}
	return id, nil
}

// returns is a visible job's returns from the nodes the caller may see.
func (c *RunnerContext) returns(jid string) ([]*job.Return, error) {
	jobs, err := c.jobs()
	if err != nil {
		return nil, err
	}
	id, err := c.visibleJID(jid)
	if err != nil {
		return nil, err
	}
	j, err := jobs.Get(id)
	if err != nil {
		return nil, err
	}
	all, err := jobs.Returns(id)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, ret := range all {
		if c.returnVisible(j, ret.NodeID) {
			out = append(out, ret)
		}
	}
	return out, nil
}

// returnVisible reports whether one node's return to a visible job may
// be shown: one from a node in scope, or any return to a runner call,
// which only its caller can see at all.
func (c *RunnerContext) returnVisible(j *job.Job, node string) bool {
	return j.Target == RunnerTarget || c.Visible(node)
}

func (c *RunnerContext) jobDetail(jid string) (any, error) {
	jobs, err := c.jobs()
	if err != nil {
		return nil, err
	}
	id, err := c.visibleJID(jid)
	if err != nil {
		return nil, err
	}
	j, err := jobs.Get(id)
	if err != nil {
		return nil, err
	}
	returns, err := jobs.Returns(id)
	if err != nil {
		return nil, err
	}
	missing, err := jobs.Missing(id)
	if err != nil {
		return nil, err
	}
	out := c.scopedSummary(j)
	rets := value.NewMap(len(returns))
	for _, ret := range returns {
		if c.returnVisible(j, ret.NodeID) {
			rets.Set(ret.NodeID, decodedReturn(ret))
		}
	}
	out.Set("returns", rets)
	out.Set("missing", stringList(c.visibleOnly(missing)))
	return out, nil
}

// jobVisible reports whether the caller may see a job at all: one that
// went to a node in its scope, or a runner call it made itself. A runner
// call's arguments name nodes -- `pillar.show_pillar db1` -- and it went
// to the hub, which is no node in anybody's scope, so only its caller and
// a scope of '*' see it (issue 321, M-56).
func (c *RunnerContext) jobVisible(j *job.Job) bool {
	if c.Scope.Everything() {
		return true
	}
	if j.Target == RunnerTarget {
		return j.Submitter == c.Principal
	}
	for _, n := range j.Nodes {
		if c.Visible(n) {
			return true
		}
	}
	return false
}

// visibleJID is parseJID for a job the caller may see. One it may not
// is answered as one that does not exist, so that asking is not a way to
// learn which jobs there are.
func (c *RunnerContext) visibleJID(jid string) (job.ID, error) {
	id, err := parseJID(jid)
	if err != nil {
		return "", err
	}
	jobs, err := c.jobs()
	if err != nil {
		return "", err
	}
	j, err := jobs.Get(id)
	if err != nil {
		return "", err
	}
	if !c.jobVisible(j) {
		return "", fmt.Errorf("%s: %w", id, job.ErrNoJob)
	}
	return id, nil
}

// scopedSummary is jobSummary with the node list cut to the caller's
// scope.
func (c *RunnerContext) scopedSummary(j *job.Job) *value.Map {
	out := jobSummary(j)
	if len(j.Nodes) > 0 && j.Target != RunnerTarget {
		out.Set("nodes", stringList(c.visibleOnly(j.Nodes)))
	}
	return out
}

// surveyGroup is one set of nodes that returned the same thing.
type surveyGroup struct {
	nodes  []string
	sample *job.Return
}

// surveyGroups buckets a job's returns by the digest of the bytes each
// node sent.
//
// The digest is over the encoded return exactly as it arrived, not over
// a re-encoding: two nodes that sent the same mapping in a different key
// order genuinely returned different bytes, and a survey that hid that
// would be answering a question nobody asked.
func (c *RunnerContext) surveyGroups(jid string) (map[string]*surveyGroup, []string, error) {
	returns, err := c.returns(jid)
	if err != nil {
		return nil, nil, err
	}
	groups := map[string]*surveyGroup{}
	var order []string
	for _, ret := range returns {
		sum := sha256.Sum256(ret.Return)
		digest := hex.EncodeToString(sum[:])[:16]
		g, seen := groups[digest]
		if !seen {
			g = &surveyGroup{sample: ret}
			groups[digest] = g
			order = append(order, digest)
		}
		g.nodes = append(g.nodes, ret.NodeID)
	}
	for _, g := range groups {
		sort.Strings(g.nodes)
	}
	// The largest group first: the answer to "what did the fleet say"
	// is the majority, and the interesting part is what follows it.
	sort.SliceStable(order, func(i, j int) bool {
		return len(groups[order[i]].nodes) > len(groups[order[j]].nodes)
	})
	return groups, order, nil
}

// jobSummary is the record without the returns.
func jobSummary(j *job.Job) *value.Map {
	out := value.NewMap(8)
	out.Set("jid", string(j.JID))
	out.Set("fun", j.Fun)
	if j.Target != "" {
		out.Set("target", j.Target)
	}
	if len(j.Arg) > 0 {
		out.Set("arg", stringList(j.Arg))
	}
	if j.Submitter != "" {
		out.Set("submitter", j.Submitter)
	}
	if j.OnBehalfOf != "" {
		out.Set("on_behalf_of", j.OnBehalfOf)
	}
	out.Set("created", j.Created.UTC().Format("2006-01-02T15:04:05.000000Z"))
	if j.State != "" {
		out.Set("state", string(j.State))
	}
	if len(j.Nodes) > 0 {
		out.Set("nodes", stringList(j.Nodes))
	}
	return out
}

// decodedReturn reads a return's payload back into the nine-type model,
// so that an ordered mapping stays ordered on the way out.
func decodedReturn(ret *job.Return) any {
	v, err := value.DecodeJSON(ret.Return)
	if err != nil {
		return string(ret.Return)
	}
	return v
}

// stringList converts to the []any the value model carries.
func stringList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
