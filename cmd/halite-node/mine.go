package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/exec"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// useHubMine lets a module on this node publish to and read from the
// mine, which lives on the hub.
func (n *node) useHubMine(client *transport.Client) {
	n.mine = hubMine{client: client, log: n.log}
}

// hubMine is the node's side of SPEC 19.5.
type hubMine struct {
	client *transport.Client
	log    *hlog.Logger
}

// Publish sends what this node has computed.
func (h hubMine) Publish(functions map[string]exec.MineValue, replace bool) error {
	published := make(map[string]transport.MinePublished, len(functions))
	for name, v := range functions {
		// Encoded with the model's own codec, so an ordered mapping
		// stays ordered and a 64-bit integer stays exact. SPEC 6.4.
		raw, err := value.EncodeJSON(v.Data, 0)
		if err != nil {
			return fmt.Errorf("the mine value for %s will not encode: %w", name, err)
		}
		published[name] = transport.MinePublished{
			Data:         raw,
			AllowTgt:     v.AllowTgt,
			AllowTgtType: v.AllowTgtType,
		}
	}
	return h.client.PublishMine(context.Background(), transport.MineRequest{
		Functions: published, Replace: replace,
	})
}

// Fetch reads what the matched nodes published.
func (h hubMine) Fetch(tgt, tgtType, function string) (*value.Map, error) {
	res, err := h.client.FetchMine(context.Background(), transport.MineGetRequest{
		Target: tgt, TargetKind: tgtType, Function: function,
	})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(len(res.Data))
	for _, node := range sortedNodes(res.Data) {
		decoded, err := value.DecodeJSON(res.Data[node])
		if err != nil {
			return nil, fmt.Errorf("the mine data from %s is not readable: %w", node, err)
		}
		out.Set(node, decoded)
	}
	return out, nil
}

func sortedNodes(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// startMine publishes this node's mine data on its interval.
//
// SPEC 19.5's `mine_interval` is in minutes, which is Salt's unit and
// what an existing configuration is written in.
func (n *node) startMine(ctx context.Context) {
	if n.mine == nil {
		return
	}
	c := n.context(n.compilePillarOrNothing())
	configured, err := builtin.MineFunctions(c)
	if err != nil {
		n.log.Error("the mine configuration will not parse", "error", err.Error())
		return
	}
	if len(configured) == 0 {
		return
	}

	interval := time.Duration(n.cfg.Int("mine_interval", 60)) * time.Minute
	if interval <= 0 {
		interval = time.Hour
	}
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	n.log.Info("mine started", "functions", names, "interval", interval.String())

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			// Published at start as well as on the interval: a node
			// that has just come up has data other nodes need now, not
			// in an hour.
			if _, err := n.registry.Exec.Call(
				n.context(n.compilePillarOrNothing()), "mine.update", value.NewMap(0)); err != nil {
				n.log.Warn("could not publish to the mine", "error", err.Error())
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
