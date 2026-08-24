package hub

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// registerPillarRunner installs `pillar`, which answers what a node
// would be given without asking the node.
func registerPillarRunner(r *Runners) {
	nodeAndEnv := []signature.Param{
		runnerArg("node", signature.String, "The node identifier."),
		runnerOpt("env", signature.String, "", "The environment, default the hub's."),
	}

	r.Add(
		RunnerModule{
			Sig: runnerSig("pillar", "show_pillar",
				"The pillar this hub would compile for one node.", "19.2", nodeAndEnv...),
			Fn: func(c *RunnerContext) (any, error) {
				out, err := c.compilePillarFor()
				if err != nil {
					return nil, err
				}
				return out.Pillar, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("pillar", "show_top",
				"The pillar files the top file selects for one node, in merge order.",
				"19.2", nodeAndEnv...),
			Fn: func(c *RunnerContext) (any, error) {
				out, err := c.compilePillarFor()
				if err != nil {
					return nil, err
				}
				return stringList(out.SLS), nil
			},
		},
		RunnerModule{
			Sig: runnerSig("pillar", "clear_cache",
				"Drop the hub's compiled-pillar cache.", "19.2"),
			Pending: "the phase that gives the hub a compiled-pillar cache; today it " +
				"compiles on every request, so there is nothing to clear (SPEC section 12.8)",
		},
	)
}

// registerCacheRunner installs `cache`, which reads and drops what the
// hub holds about a node.
func registerCacheRunner(r *Runners) {
	r.Add(
		RunnerModule{
			Sig: runnerSig("cache", "grains",
				"The grains this hub last received from a node, or from every node "+
					"when none is named.", "19.2",
				runnerOpt("node", signature.String, "", "One node, or every node."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				if node := c.arg("node"); node != "" {
					return c.Server.cachedNode(node)
				}
				// Every node in one answer, because the alternative is
				// a round trip per node and an estate is not small.
				known, err := c.Server.nodes().Known()
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(known))
				for _, id := range known {
					entry, err := c.Server.cachedNode(id)
					if err != nil {
						// One unreadable record must not hide the
						// rest, and must not be silently dropped
						// either.
						c.Server.warn("skipping a node whose cached data is unreadable",
							"node_id", id, "error", err.Error())
						continue
					}
					out.Set(id, entry)
				}
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("cache", "clear_grains",
				"Forget one node's cached grains, so the next match on a grain "+
					"waits for the node to push again.", "19.2",
				runnerArg("node", signature.String, "The node identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				if err := c.Server.nodes().Delete(c.arg("node")); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("cache", "clear_all",
				"Forget every node's cached grains. The mine is cleared with "+
					"`cache.clear_mine`, which names the node.", "19.2"),
			Fn: func(c *RunnerContext) (any, error) {
				known, err := c.Server.nodes().Known()
				if err != nil {
					return nil, err
				}
				cleared := 0
				for _, id := range known {
					if err := c.Server.nodes().Delete(id); err != nil {
						return nil, err
					}
					cleared++
				}
				return int64(cleared), nil
			},
		},
		RunnerModule{
			Sig:     runnerSig("cache", "pillar", "One node's cached pillar.", "19.2"),
			Pending: "the phase that gives the hub a compiled-pillar cache; `pillar.show_pillar` compiles it now (SPEC section 12.8)",
		},
		RunnerModule{
			Sig: runnerSig("cache", "mine", "One node's published mine data.", "19.2",
				runnerArg("node", signature.String, "The node identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				data, err := c.Server.mine().Get(c.arg("node"))
				if err != nil {
					return nil, err
				}
				out := value.NewMap(len(data.Functions))
				for _, name := range sortedMineKeys(data.Functions) {
					decoded, err := value.DecodeJSON(data.Functions[name].Data)
					if err != nil {
						decoded = string(data.Functions[name].Data)
					}
					out.Set(name, decoded)
				}
				return out, nil
			},
		},
		RunnerModule{
			Sig:     runnerSig("cache", "clear_pillar", "Drop cached pillar.", "19.2"),
			Pending: "the phase that gives the hub a compiled-pillar cache (SPEC section 12.8)",
		},
		RunnerModule{
			Sig: runnerSig("cache", "clear_mine", "Drop one node's published mine data.", "19.2",
				runnerArg("node", signature.String, "The node identifier."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				if err := c.Server.mine().Delete(c.arg("node"), ""); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
	)
}

// registerFileserverRunner installs `fileserver`, which reports what
// the hub is serving.
func registerFileserverRunner(r *Runners) {
	envParam := runnerOpt("env", signature.String, "base", "The environment.")

	r.Add(
		RunnerModule{
			Sig: runnerSig("fileserver", "envs", "The environments this hub serves.", "19.2"),
			Fn: func(c *RunnerContext) (any, error) {
				roots, err := c.roots()
				if err != nil {
					return nil, err
				}
				return stringList(roots.Envs()), nil
			},
		},
		RunnerModule{
			Sig: runnerSig("fileserver", "file_list", "The files in one environment.", "19.2", envParam),
			Fn: func(c *RunnerContext) (any, error) {
				files, err := c.fileList()
				if err != nil {
					return nil, err
				}
				return stringList(files), nil
			},
		},
		RunnerModule{
			Sig: runnerSig("fileserver", "dir_list", "The directories in one environment.", "19.2", envParam),
			Fn: func(c *RunnerContext) (any, error) {
				files, err := c.fileList()
				if err != nil {
					return nil, err
				}
				seen := map[string]bool{}
				for _, f := range files {
					for dir := path.Dir(f); dir != "." && dir != "/"; dir = path.Dir(dir) {
						seen[dir] = true
					}
				}
				dirs := make([]string, 0, len(seen))
				for dir := range seen {
					dirs = append(dirs, dir)
				}
				sort.Strings(dirs)
				return stringList(dirs), nil
			},
		},
		RunnerModule{
			Sig: runnerSig("fileserver", "symlink_list",
				"The symbolic links in one environment.", "19.2", envParam),
			Pending: "the phase that adds symlink reporting to the roots backend; " +
				"it resolves links today and does not list them (SPEC section 13.5)",
		},
		RunnerModule{
			Sig:     runnerSig("fileserver", "update", "Refresh a remote backend.", "19.2"),
			Pending: "phase 5, with gitfs and s3fs; the roots backend reads the filesystem and has nothing to fetch (SPEC section 13.2)",
		},
		RunnerModule{
			Sig:     runnerSig("fileserver", "clear_cache", "Drop a remote backend's cache.", "19.2"),
			Pending: "phase 5, with gitfs and s3fs (SPEC section 13.2)",
		},
		RunnerModule{
			Sig:     runnerSig("fileserver", "lock", "Take a remote backend's update lock.", "19.2"),
			Pending: "phase 5, with gitfs and s3fs (SPEC section 13.2)",
		},
		RunnerModule{
			Sig:     runnerSig("fileserver", "clear_lock", "Release a remote backend's update lock.", "19.2"),
			Pending: "phase 5, with gitfs and s3fs (SPEC section 13.2)",
		},
		RunnerModule{
			Sig:     runnerSig("fileserver", "versions", "The revision each backend is serving.", "19.2"),
			Pending: "phase 5, with gitfs and s3fs (SPEC section 13.2)",
		},
	)
}

// registerEventRunner installs `event`, which puts a record on the bus
// and reads records back off it.
func registerEventRunner(r *Runners) {
	r.Add(
		RunnerModule{
			Sig: runnerSig("event", "send", "Put an event on this hub's bus.", "19.2",
				runnerArg("tag", signature.String, "The tag, rooted at halite/."),
				runnerOpt("data", signature.Map, nil, "The payload."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				bus, err := c.bus()
				if err != nil {
					return nil, err
				}
				tag := c.arg("tag")
				if err := eventbus.ValidTag(tag); err != nil {
					return nil, err
				}
				data, err := eventData(c.Args)
				if err != nil {
					return nil, err
				}
				// The principal is recorded rather than trusted from
				// the payload: a reaction that fires on this tag will
				// be asked who caused it, and the answer has to come
				// from the connection, not from the body.
				data["_principal"] = c.Principal
				offset, err := bus.Append(&eventbus.Event{
					Tag:         tag,
					Data:        data,
					Correlation: string(c.JID),
				})
				if err != nil {
					return nil, err
				}
				out := value.NewMap(2)
				out.Set("tag", tag)
				out.Set("offset", offset)
				return out, nil
			},
		},
		RunnerModule{
			Sig: runnerSig("event", "replay",
				"Read events already on the bus, from an offset.", "19.2",
				runnerOpt("tag", signature.String, "halite/**", "A tag glob, or a comma list of them."),
				runnerOpt("from", signature.String, "earliest", "earliest, latest, or an offset."),
				runnerOpt("limit", signature.Int, 100, "How many events at most."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				bus, err := c.bus()
				if err != nil {
					return nil, err
				}
				events, next, err := bus.Read(c.arg("from"), tagGlobs(c.arg("tag")), c.argInt("limit"))
				if err != nil {
					return nil, err
				}
				return eventPage(events, next)
			},
		},
		RunnerModule{
			Sig: runnerSig("event", "listen",
				"Wait for events on the bus and return what arrived.", "19.2",
				runnerOpt("tag", signature.String, "halite/**", "A tag glob, or a comma list of them."),
				runnerOpt("timeout", signature.Duration, "30s", "How long to wait for the first event."),
				runnerOpt("limit", signature.Int, 100, "How many events at most."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				bus, err := c.bus()
				if err != nil {
					return nil, err
				}
				globs := tagGlobs(c.arg("tag"))
				limit := c.argInt("limit")
				// From latest, not earliest: `listen` means "what
				// happens next", and starting at the beginning of the
				// log would return history and call it an arrival.
				from := "latest"
				deadline := time.NewTimer(c.argDuration("timeout"))
				defer deadline.Stop()
				for {
					wake := bus.Wait()
					events, next, err := bus.Read(from, globs, limit)
					if err != nil {
						return nil, err
					}
					if len(events) > 0 {
						return eventPage(events, next)
					}
					from = next
					select {
					case <-wake:
					case <-deadline.C:
						return eventPage(nil, next)
					case <-c.Ctx.Done():
						return nil, c.Ctx.Err()
					}
				}
			},
		},
	)
}

// cachedNode is what the hub holds about one node.
func (s *Server) cachedNode(id string) (*value.Map, error) {
	data, err := s.nodes().Get(id)
	if err != nil {
		return nil, err
	}
	out := value.NewMap(3)
	grains, err := value.DecodeJSON(data.Grains)
	if err != nil {
		return nil, fmt.Errorf("the cached grains for %s will not decode: %w", data.NodeID, err)
	}
	out.Set("grains", grains)
	out.Set("last_seen", data.LastSeen.UTC().Format(time.RFC3339))
	if data.Version != "" {
		out.Set("version", data.Version)
	}
	return out, nil
}

// ---- shared helpers ----

func (c *RunnerContext) argDuration(name string) time.Duration {
	v, ok := c.Args.Get(name)
	if !ok || v == nil {
		return 30 * time.Second
	}
	if d, ok := v.(time.Duration); ok {
		return d
	}
	d, err := time.ParseDuration(value.KeyString(v))
	if err != nil {
		return 30 * time.Second
	}
	return d
}

func (c *RunnerContext) bus() (*eventbus.Bus, error) {
	if c.Server.Events == nil {
		return nil, fmt.Errorf("this hub keeps no event bus")
	}
	return c.Server.Events, nil
}

func (c *RunnerContext) roots() (*fileserver.Roots, error) {
	if c.Server.Files == nil {
		return nil, fmt.Errorf("this hub serves no state tree")
	}
	return c.Server.Files, nil
}

func (c *RunnerContext) fileList() ([]string, error) {
	roots, err := c.roots()
	if err != nil {
		return nil, err
	}
	files, err := roots.List(c.arg("env"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func (c *RunnerContext) compilePillarFor() (*pillar.Compiled, error) {
	if c.Server.Pillar == nil {
		return nil, fmt.Errorf("this hub compiles no pillar")
	}
	node := c.arg("node")
	data, err := c.Server.nodes().Get(node)
	if err != nil {
		return nil, fmt.Errorf("%s: %w; the node has to have pushed its grains before "+
			"its pillar can be compiled the way it would receive it", node, err)
	}
	grains := value.NewMap(0)
	if len(data.Grains) > 0 {
		decoded, err := value.DecodeJSON(data.Grains)
		if err != nil {
			return nil, fmt.Errorf("the cached grains for %s will not decode: %w", node, err)
		}
		if m, ok := decoded.(*value.Map); ok {
			grains = m
		}
	}
	env := c.arg("env")
	if env == "" {
		env = "base"
	}
	return c.Server.compilePillar(node, env, grains)
}

// eventData reads the `data` argument as the payload map.
func eventData(args *value.Map) (map[string]any, error) {
	raw, ok := args.Get("data")
	if !ok || raw == nil {
		return map[string]any{}, nil
	}
	switch m := raw.(type) {
	case *value.Map:
		out := make(map[string]any, m.Len())
		for _, e := range m.Entries() {
			out[value.KeyString(e.Key)] = e.Val
		}
		return out, nil
	case map[string]any:
		return m, nil
	case string:
		// A payload typed on a command line arrives as one JSON string.
		var out map[string]any
		if err := json.Unmarshal([]byte(m), &out); err != nil {
			return nil, fmt.Errorf("the data argument is neither a mapping nor JSON: %w", err)
		}
		return out, nil
	}
	return nil, fmt.Errorf("the data argument is a mapping, not %s", value.TypeName(raw))
}

func tagGlobs(spec string) []string {
	var out []string
	for _, part := range strings.Split(spec, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// eventPage is the shape both `replay` and `listen` return: the records
// and where to carry on from.
func eventPage(events []eventbus.Event, next string) (any, error) {
	list := make([]any, 0, len(events))
	for i := range events {
		encoded, err := json.Marshal(events[i])
		if err != nil {
			return nil, err
		}
		decoded, err := value.DecodeJSON(encoded)
		if err != nil {
			return nil, err
		}
		list = append(list, decoded)
	}
	out := value.NewMap(2)
	out.Set("events", list)
	out.Set("next", next)
	return out, nil
}
