package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// registerMineRunner installs the `mine` runner of SPEC 19.2, which is
// how an operator reads what nodes have published.
func registerMineRunner(r *Runners) {
	r.Add(
		RunnerModule{
			Sig: runnerSig("mine", "get",
				"What the matched nodes published for one function.", "19.5",
				runnerArg("tgt", signature.String, "Which nodes."),
				runnerArg("fun", signature.String, "The mine function."),
				runnerOpt("tgt_type", signature.String, "", "The target kind of SPEC section 8."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				// An operator reading, not a node: `allow_tgt` names
				// which nodes may read an entry, and an operator has
				// already been through the policy to get here.
				got, err := c.Server.MineGet("", c.arg("tgt"), c.arg("tgt_type"), c.arg("fun"))
				if err != nil {
					return nil, err
				}
				return mineJSON(got), nil
			},
		},
		RunnerModule{
			Sig: runnerSig("mine", "update",
				"Tell the matched nodes to recompute and republish their mine data.", "19.5",
				runnerOpt("tgt", signature.String, "*", "Which nodes."),
				runnerOpt("tgt_type", signature.String, "", "The target kind of SPEC section 8."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				j, err := c.dispatch(Submission{
					Target:     c.arg("tgt"),
					TargetKind: c.arg("tgt_type"),
					Fun:        "mine.update",
				})
				if err != nil {
					return nil, err
				}
				out := value.NewMap(2)
				out.Set("jid", string(j.JID))
				out.Set("nodes", stringList(j.Nodes))
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("mine", "flush",
				"Drop everything one node has published.", "19.5",
				runnerArg("node", signature.String, "The node identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				if err := c.Server.mine().Delete(c.arg("node"), ""); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("mine", "delete",
				"Drop one function's entry for one node.", "19.5",
				runnerArg("node", signature.String, "The node identifier."),
				runnerArg("fun", signature.String, "The mine function."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				if err := c.Server.mine().Delete(c.arg("node"), c.arg("fun")); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("mine", "valid",
				"What each node has published, with when it last did and who may read it. "+
					"An entry nobody has refreshed is what a stale answer looks like before "+
					"it becomes a wrong one.", "19.5",
				runnerOpt("node", signature.String, "", "One node, or every node."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				return c.Server.mineValid(c.arg("node"))
			},
		},
	)
}

// mineValid describes what the mine holds.
func (s *Server) mineValid(only string) (any, error) {
	nodes, err := s.mine().Nodes()
	if err != nil {
		return nil, err
	}
	out := value.NewMap(len(nodes))
	for _, id := range nodes {
		if only != "" && id != only {
			continue
		}
		data, err := s.mine().Get(id)
		if errors.Is(err, ErrNoMineData) {
			continue
		}
		if err != nil {
			return nil, err
		}
		functions := value.NewMap(len(data.Functions))
		for _, name := range sortedMineKeys(data.Functions) {
			entry := data.Functions[name]
			item := value.NewMap(3)
			item.Set("updated", entry.Updated.UTC().Format(time.RFC3339))
			if entry.AllowTgt != "" {
				item.Set("allow_tgt", entry.AllowTgt)
				if entry.AllowKind != "" {
					item.Set("allow_tgt_type", entry.AllowKind)
				}
			}
			item.Set("bytes", int64(len(entry.Data)))
			functions.Set(name, item)
		}
		out.Set(id, functions)
	}
	if only != "" && out.Len() == 0 {
		return nil, fmt.Errorf("%s has published nothing to the mine", only)
	}
	return out, nil
}

// mineJSON decodes what the nodes published back into the model.
func mineJSON(got map[string]json.RawMessage) *value.Map {
	out := value.NewMap(len(got))
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		decoded, err := value.DecodeJSON(got[k])
		if err != nil {
			decoded = string(got[k])
		}
		out.Set(k, decoded)
	}
	return out
}

func sortedMineKeys(m map[string]*MineEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
