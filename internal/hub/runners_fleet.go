package hub

import (
	"fmt"
	"sort"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

// presenceStates are the names `manage.list_state` accepts.
//
// Salt distinguishes `present` (connected to the master) from `alived` // lexicon:allow
// (answers a ping) from `up` (either, depending on the runner), because
// its transport cannot tell a live connection from a dead one without
// asking. Here a node holds a stream to the hub or it does not, so the
// three are one fact under three names, and the names are kept only so
// that existing orchestration reads unchanged.
var presenceStates = []string{"up", "down", "present", "alived", "not_alived"}

// registerManageRunner installs the `manage` runner of SPEC 19.2.
func registerManageRunner(r *Runners) {
	connected := func(c *RunnerContext) (any, error) { return c.connectedNodes() }
	absent := func(c *RunnerContext) (any, error) { return c.absentNodes() }

	r.Add(
		RunnerModule{
			Sig: runnerSig("manage", "status",
				"The connected and disconnected halves of the accepted fleet.", "19.2"),
			Fn: func(c *RunnerContext) (any, error) {
				up, err := c.connectedNodes()
				if err != nil {
					return nil, err
				}
				down, err := c.absentNodes()
				if err != nil {
					return nil, err
				}
				out := value.NewMap(2)
				out.Set("up", up)
				out.Set("down", down)
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("manage", "up", "The accepted nodes holding a stream to this hub.", "19.2"),
			Fn:  connected,
		},
		RunnerModule{
			Sig: runnerSig("manage", "present",
				"The accepted nodes holding a stream to this hub. The same set as "+
					"`up`: a node here is connected or it is not.", "19.2"),
			Fn: connected,
		},
		RunnerModule{
			Sig: runnerSig("manage", "alived",
				"The accepted nodes holding a stream to this hub. The same set as `up`.", "19.2"),
			Fn: connected,
		},
		RunnerModule{
			Sig: runnerSig("manage", "down", "The accepted nodes with no stream to this hub.", "19.2"),
			Fn:  absent,
		},
		RunnerModule{
			Sig: runnerSig("manage", "not_alived",
				"The accepted nodes with no stream to this hub. The same set as `down`.", "19.2"),
			Fn: absent,
		},
		RunnerModule{
			Sig: runnerSig("manage", "list_state",
				"The nodes in one presence state.", "19.2",
				signature.Param{
					Name: "state", Type: signature.String, Default: "up",
					Doc:     "up, down, present, alived, or not_alived.",
					Choices: presenceStates,
				},
			),
			Fn: func(c *RunnerContext) (any, error) { return c.inPresenceState(c.arg("state"), true) },
		},
		RunnerModule{
			Sig: runnerSig("manage", "list_not_state",
				"The nodes not in one presence state.", "19.2",
				signature.Param{
					Name: "state", Type: signature.String, Default: "up",
					Doc:     "up, down, present, alived, or not_alived.",
					Choices: presenceStates,
				},
			),
			Fn: func(c *RunnerContext) (any, error) { return c.inPresenceState(c.arg("state"), false) },
		},
		RunnerModule{
			Sig: runnerSig("manage", "versions",
				"The build each node last reported, against this hub's own.", "19.2"),
			Fn: func(c *RunnerContext) (any, error) {
				accepted, err := c.acceptedNodes()
				if err != nil {
					return nil, err
				}
				// version.String, not version.Version: a node reports
				// the string, commit suffix and all, and comparing it
				// against the bare version reported every node in a
				// matched fleet as mismatched.
				hubVersion := version.String()
				byVersion := map[string][]string{}
				for _, id := range accepted {
					reported := "unknown"
					if data, err := c.Server.nodes().Get(id); err == nil && data.Version != "" {
						reported = data.Version
					}
					byVersion[reported] = append(byVersion[reported], id)
				}
				nodes := value.NewMap(len(byVersion))
				for _, v := range sortedMapKeys(byVersion) {
					sort.Strings(byVersion[v])
					nodes.Set(v, stringList(byVersion[v]))
				}
				out := value.NewMap(3)
				out.Set("hub", hubVersion)
				out.Set("nodes", nodes)
				// Named rather than inferred: "behind" would be a
				// version comparison, and a node reporting `unknown`
				// has not told this hub anything to compare.
				out.Set("mismatched", stringList(mismatched(byVersion, hubVersion)))
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("manage", "safe_accept",
				"Accept a pending key only after the node confirms its own "+
					"fingerprint over an already-trusted channel.", "19.2"),
			Pending: "phase 3, with the node-side key module",
		},
		RunnerModule{
			Sig: runnerSig("manage", "bootstrap",
				"Install halite-node on a machine that does not have one.", "19.2"),
			Pending: "phase 5, with agentless mode (SPEC section 21)",
		},
	)
}

// registerKeyRunner installs the `key` runner: the key lifecycle of
// SPEC 7.4, reachable from an orchestration or a reaction rather than
// only from an operator's terminal.
func registerKeyRunner(r *Runners) {
	r.Add(
		RunnerModule{
			Sig: runnerSig("key", "list", "Every key request, grouped by lifecycle state.", "19.2"),
			Fn: func(c *RunnerContext) (any, error) {
				records, err := c.keys()
				if err != nil {
					return nil, err
				}
				now := c.Server.now()
				byState := map[string][]string{}
				for _, rec := range records {
					state := string(rec.Status(now))
					byState[state] = append(byState[state], rec.NodeID)
				}
				out := value.NewMap(len(keystore.States))
				for _, state := range keystore.States {
					ids := byState[string(state)]
					sort.Strings(ids)
					out.Set(string(state), stringList(ids))
				}
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("key", "finger", "One node's public key fingerprint.", "19.2",
				runnerArg("node", signature.String, "The node identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				rec, err := c.keyRecord(c.arg("node"))
				if err != nil {
					return nil, err
				}
				return rec.Fingerprint, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("key", "accept", "Accept a pending key and issue its certificate.", "19.2",
				runnerArg("node", signature.String, "The node identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				if err := c.needAuthority(); err != nil {
					return nil, err
				}
				rec, err := c.Server.Authority.Accept(c.arg("node"))
				if err != nil {
					return nil, err
				}
				return keyRecord(rec, c.Server.now()), nil
			},
		},
		RunnerModule{
			Sig: runnerSig("key", "reject", "Refuse a pending key.", "19.2",
				runnerArg("node", signature.String, "The node identifier."),
				runnerOpt("reason", signature.String, "", "Why, for the audit record."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				if err := c.needAuthority(); err != nil {
					return nil, err
				}
				rec, err := c.Server.Authority.Reject(c.arg("node"), c.arg("reason"))
				if err != nil {
					return nil, err
				}
				return keyRecord(rec, c.Server.now()), nil
			},
		},
		RunnerModule{
			Sig: runnerSig("key", "revoke", "Withdraw an acceptance and denylist the certificate.", "19.2",
				runnerArg("node", signature.String, "The node identifier."),
				runnerOpt("reason", signature.String, "", "Why, for the audit record."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				if err := c.needAuthority(); err != nil {
					return nil, err
				}
				rec, err := c.Server.Authority.Revoke(c.arg("node"), c.arg("reason"))
				if err != nil {
					return nil, err
				}
				return keyRecord(rec, c.Server.now()), nil
			},
		},
		RunnerModule{
			Sig: runnerSig("key", "delete",
				"Remove a key record. An accepted node is revoked first, so that "+
					"deleting the record cannot leave a live certificate behind.", "19.2",
				runnerArg("node", signature.String, "The node identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				if err := c.needAuthority(); err != nil {
					return nil, err
				}
				node := c.arg("node")
				store := c.Server.Authority.Store
				if rec, err := store.Get(node); err == nil && rec.State == keystore.Accepted {
					if _, err := c.Server.Authority.Revoke(node, "deleted"); err != nil {
						return nil, err
					}
				}
				if err := store.Delete(node); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("key", "gen_signature",
				"Produce the hub's key signature for a node to pin before enrolling.", "19.2"),
			Pending: "phase 3, with the hub fingerprint pinning of SPEC section 7.3",
		},
		RunnerModule{
			Sig: runnerSig("key", "token",
				"Mint a bootstrap enrollment token.", "19.2"),
			Pending: "phase 4, with the API's token handling (SPEC section 23.6)",
		},
	)
}

// registerNodegroupsRunner installs `nodegroups`, which answers what a
// name in the configuration actually expands to.
func registerNodegroupsRunner(r *Runners) {
	r.Add(
		RunnerModule{
			Sig: runnerSig("nodegroups", "list", "Every configured nodegroup and its expression.", "19.2"),
			Fn: func(c *RunnerContext) (any, error) {
				groups := c.Server.nodegroups()
				out := value.NewMap(len(groups))
				names := make([]string, 0, len(groups))
				for name := range groups {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					out.Set(name, groups[name])
				}
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("nodegroups", "show", "One nodegroup's expression.", "19.2",
				runnerArg("name", signature.String, "The nodegroup name."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				expr, ok := c.Server.nodegroups()[c.arg("name")]
				if !ok {
					return nil, fmt.Errorf("%q is not a nodegroup on this hub", c.arg("name"))
				}
				return expr, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("nodegroups", "expand",
				"The nodes a nodegroup matches right now.", "19.2",
				runnerArg("name", signature.String, "The nodegroup name."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				matcher, err := target.Compile(target.Nodegroup, c.arg("name"), c.Server.nodegroups())
				if err != nil {
					return nil, err
				}
				matched, err := c.Server.resolve(matcher)
				if err != nil {
					return nil, err
				}
				return stringList(matched), nil
			},
		},
	)
}

// registerSaltutilRunner installs the maintenance functions Salt puts
// under saltutil. The name is retained by SPEC section 2.3.
func registerSaltutilRunner(r *Runners) {
	refresh := func(fun string) RunnerFunc {
		return func(c *RunnerContext) (any, error) {
			j, err := c.dispatch(Submission{
				Target:     c.arg("target"),
				TargetKind: c.arg("target_kind"),
				Fun:        fun,
			})
			if err != nil {
				return nil, err
			}
			out := value.NewMap(2)
			out.Set("jid", string(j.JID))
			out.Set("nodes", stringList(j.Nodes))
			return out, nil
		}
	}
	targetParams := []signature.Param{
		runnerOpt("target", signature.String, "*", "Which nodes."),
		runnerOpt("target_kind", signature.String, "", "The target kind flag of SPEC section 8, such as G or C."),
	}

	r.Add(
		RunnerModule{
			Sig: runnerSig("saltutil", "refresh_pillar",
				"Tell the matched nodes to recompile their pillar.", "19.2", targetParams...),
			Fn: refresh("pillar.refresh"),
		},
		RunnerModule{
			Sig: runnerSig("saltutil", "refresh_grains",
				"Tell the matched nodes to re-collect and re-push their grains.", "19.2", targetParams...),
			Fn: refresh("saltutil.refresh_grains"),
		},
		RunnerModule{
			Sig: runnerSig("saltutil", "sync_all",
				"Distribute the extension bundles the matched nodes are entitled to.", "19.2"),
			Pending: "phase 4, with the extension model (SPEC section 24.5)",
		},
	)
}

// ---- shared helpers ----

// dispatch sends a fleet job on a runner's behalf, and authorizes it
// again as a fleet job first.
//
// This is the escalation SPEC 18.3 describes, met one layer earlier. A
// `runners:` grant says the principal may call `saltutil.refresh_pillar`
// on the hub. It does not say the principal may run a function against
// every node in the estate, and a runner that dispatched on the
// strength of the runner grant alone would turn the narrower permission
// into the wider one.
func (c *RunnerContext) dispatch(sub Submission) (*job.Job, error) {
	return c.Server.DispatchAs(c.Principal, sub)
}

// DispatchAs sends a fleet job on a principal's behalf, authorizing it
// as a fleet job first.
//
// This is the escalation SPEC 18.3 describes, met one layer earlier. A
// `runners:` grant says the principal may call `saltutil.refresh_pillar`
// on the hub, and an orchestration grant says they may run one. Neither
// says they may run a function against every node in the estate, and
// dispatching on the strength of the outer grant alone would turn the
// narrower permission into the wider one.
func (s *Server) DispatchAs(principal string, sub Submission) (*job.Job, error) {
	decision := s.Policy.Authorize(policy.Request{
		Principal: principal,
		Target:    sub.Target,
		Fun:       sub.Fun,
		Arg:       sub.Arg,
		Kwarg:     sub.Kwarg,
	})
	if !decision.Allowed {
		s.warn("a job dispatched on a principal's behalf was refused by policy",
			"principal", principal, "target", sub.Target, "fun", sub.Fun,
			"reason", decision.Reason)
		return nil, fmt.Errorf("%s may ask for this but not %s against %q: %s",
			principal, sub.Fun, sub.Target, decision.Reason)
	}
	sub.Submitter = principal
	return s.Dispatch(sub)
}

func (c *RunnerContext) needAuthority() error {
	if c.Server.Authority == nil {
		return fmt.Errorf("this hub has no enrollment authority, so it decides no keys")
	}
	return nil
}

func (c *RunnerContext) keys() ([]*keystore.Record, error) {
	if err := c.needAuthority(); err != nil {
		return nil, err
	}
	return c.Server.Authority.Store.List()
}

func (c *RunnerContext) keyRecord(node string) (*keystore.Record, error) {
	if err := c.needAuthority(); err != nil {
		return nil, err
	}
	return c.Server.Authority.Store.Get(node)
}

// acceptedNodes is the estate: every node whose key is accepted and
// current.
func (c *RunnerContext) acceptedNodes() ([]string, error) {
	records, err := c.keys()
	if err != nil {
		return nil, err
	}
	now := c.Server.now()
	var out []string
	for _, rec := range records {
		if rec.Status(now) == keystore.Accepted {
			out = append(out, rec.NodeID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (c *RunnerContext) connectedNodes() ([]any, error) {
	accepted, err := c.acceptedNodes()
	if err != nil {
		return nil, err
	}
	live := c.Server.fleet().Connected()
	var out []string
	for _, id := range accepted {
		if _, up := live[id]; up {
			out = append(out, id)
		}
	}
	return stringList(out), nil
}

func (c *RunnerContext) absentNodes() ([]any, error) {
	accepted, err := c.acceptedNodes()
	if err != nil {
		return nil, err
	}
	live := c.Server.fleet().Connected()
	var out []string
	for _, id := range accepted {
		if _, up := live[id]; !up {
			out = append(out, id)
		}
	}
	return stringList(out), nil
}

// inPresenceState answers list_state and list_not_state from the one
// fact underneath both.
func (c *RunnerContext) inPresenceState(state string, want bool) (any, error) {
	var connected bool
	switch state {
	case "up", "present", "alived":
		connected = true
	case "down", "not_alived":
		connected = false
	default:
		return nil, fmt.Errorf("%q is not a presence state; try %v", state, presenceStates)
	}
	if !want {
		connected = !connected
	}
	if connected {
		return c.connectedNodes()
	}
	return c.absentNodes()
}

func keyRecord(rec *keystore.Record, now time.Time) *value.Map {
	out := value.NewMap(5)
	out.Set("node_id", rec.NodeID)
	out.Set("state", string(rec.Status(now)))
	out.Set("fingerprint", rec.Fingerprint)
	if rec.Reason != "" {
		out.Set("reason", rec.Reason)
	}
	if !rec.NotAfter.IsZero() {
		out.Set("not_after", rec.NotAfter.UTC().Format(time.RFC3339))
	}
	return out
}

func sortedMapKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// mismatched lists the nodes not reporting the hub's own build.
func mismatched(byVersion map[string][]string, hubVersion string) []string {
	var out []string
	for v, ids := range byVersion {
		if v == hubVersion {
			continue
		}
		out = append(out, ids...)
	}
	sort.Strings(out)
	return out
}
