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
			Sig: runnerSig("jobs", "list_jobs",
				"List recent jobs, most recent first.", "19.2",
				runnerOpt("limit", signature.Int, 20, "How many to list."),
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
				out := value.NewMap(len(list))
				for _, j := range list {
					out.Set(string(j.JID), jobSummary(j))
				}
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("jobs", "list_job",
				"The record of one job, with its returns.", "19.2",
				runnerArg("jid", signature.String, "The job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) { return c.jobDetail(c.arg("jid")) },
		},
		RunnerModule{
			Sig: runnerSig("jobs", "print_job",
				"The record of one job, with its returns. The same as list_job, "+
					"which is what Salt's two names amount to.", "19.2",
				runnerArg("jid", signature.String, "The job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) { return c.jobDetail(c.arg("jid")) },
		},
		RunnerModule{
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
					missing, err := jobs.Missing(j.JID)
					if err != nil {
						return nil, err
					}
					summary := jobSummary(j)
					summary.Set("missing", stringList(missing))
					out.Set(string(j.JID), summary)
				}
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("jobs", "missing",
				"The nodes a job has not heard from.", "19.2",
				runnerArg("jid", signature.String, "The job identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				jobs, err := c.jobs()
				if err != nil {
					return nil, err
				}
				id, err := parseJID(c.arg("jid"))
				if err != nil {
					return nil, err
				}
				missing, err := jobs.Missing(id)
				if err != nil {
					return nil, err
				}
				return stringList(missing), nil
			},
		},
		RunnerModule{
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
				id, err := parseJID(c.arg("jid"))
				if err != nil {
					return nil, err
				}
				missing, err := jobs.Missing(id)
				if err != nil {
					return nil, err
				}
				if len(missing) > 0 {
					return false, nil
				}
				returns, err := jobs.Returns(id)
				if err != nil {
					return nil, err
				}
				if len(returns) == 0 {
					return false, nil
				}
				for _, ret := range returns {
					if !ret.Success {
						return false, nil
					}
				}
				return true, nil
			},
		},
		RunnerModule{
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

func parseJID(s string) (job.ID, error) {
	id := job.ID(s)
	if !id.Valid() {
		return "", fmt.Errorf("%q is not a job identifier", s)
	}
	return id, nil
}

func (c *RunnerContext) returns(jid string) ([]*job.Return, error) {
	jobs, err := c.jobs()
	if err != nil {
		return nil, err
	}
	id, err := parseJID(jid)
	if err != nil {
		return nil, err
	}
	return jobs.Returns(id)
}

func (c *RunnerContext) jobDetail(jid string) (any, error) {
	jobs, err := c.jobs()
	if err != nil {
		return nil, err
	}
	id, err := parseJID(jid)
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
	out := jobSummary(j)
	rets := value.NewMap(len(returns))
	for _, ret := range returns {
		rets.Set(ret.NodeID, decodedReturn(ret))
	}
	out.Set("returns", rets)
	out.Set("missing", stringList(missing))
	return out, nil
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
