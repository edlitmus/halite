// Package agent is the halite side that runs on a managed host: it
// enrolls with the control plane, reports its facts, waits for work, and
// executes it with the same engine `halite apply` uses.
//
// The agent fetches the state tree as an archive and its pillar as JSON,
// then runs entirely locally. A job executes against a cached copy of the
// tree, so an agent behaves identically whether it was told to converge by
// an operator or by cron.
package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/edlitmus/halite/internal/archive"
	"github.com/edlitmus/halite/internal/beacon"
	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/event"
	"github.com/edlitmus/halite/internal/mine"
	"github.com/edlitmus/halite/internal/transport"
)

// Config is what an agent needs to reach its control plane.
type Config struct {
	ID       string // this agent's identity; defaults to the host grain
	Master   string // host or host:port of the control plane
	PKIDir   string // holds ca.crt, agent.key, agent.crt
	CacheDir string // where the fetched state tree lives
	Version  string // reported to the control plane

	// RetryInterval paces reconnection and enrollment attempts.
	RetryInterval time.Duration

	// Beacons watch the host and raise events.
	Beacons []beacon.Beacon

	// MineJobs are facts this agent publishes for the rest of the fleet.
	MineJobs []mine.Job
}

func (c *Config) withDefaults() {
	if c.RetryInterval == 0 {
		c.RetryInterval = 10 * time.Second
	}
}

func (c Config) caCert() string    { return filepath.Join(c.PKIDir, "ca.crt") }
func (c Config) agentKey() string  { return filepath.Join(c.PKIDir, "agent.key") }
func (c Config) agentCert() string { return filepath.Join(c.PKIDir, "agent.crt") }

// Agent is a running agent.
type Agent struct {
	cfg    Config
	log    *log.Logger
	client *transport.Client
	grains map[string]any
}

// New builds an agent. Grains are collected once by the caller: they
// describe the host, and a long-running agent that re-derived them on every
// poll would spend most of its life shelling out.
func New(cfg Config, grains map[string]any, logger *log.Logger) (*Agent, error) {
	cfg.withDefaults()
	if cfg.Master == "" {
		return nil, fmt.Errorf("no control plane address (-master)")
	}
	if cfg.ID == "" {
		host, _ := grains["host"].(string)
		cfg.ID = host
	}
	if err := ca.ValidateID(cfg.ID); err != nil {
		return nil, fmt.Errorf("agent id: %w", err)
	}
	// Under a control plane the enrolled identity is what operators target
	// and what states branch on, so it replaces the hostname-derived value.
	grains["id"] = cfg.ID
	return &Agent{cfg: cfg, log: logger, grains: grains}, nil
}

// Run enrolls if needed, then polls for work until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.ensureEnrolled(ctx); err != nil {
		return err
	}
	if err := a.connect(); err != nil {
		return err
	}
	a.log.Printf("agent %q connected to %s", a.cfg.ID, a.client.Host)

	// Beacons run for the life of the agent, independent of the poll loop:
	// a host whose control plane is unreachable should still notice its
	// disk filling, and report it when the connection comes back.
	if len(a.cfg.Beacons) > 0 {
		runner := beacon.NewRunner(a.cfg.Beacons, a.raiseBeacon, a.log)
		a.log.Printf("watching with %d beacon(s)", runner.Count())
		go runner.Run(ctx)
	}

	if len(a.cfg.MineJobs) > 0 {
		runner := mine.NewRunner(a.cfg.MineJobs, a.grains, a.publishMine, a.log)
		a.log.Printf("publishing %d function(s) to the mine", runner.Count())
		go runner.Run(ctx)
	}

	for ctx.Err() == nil {
		if err := a.sayHello(ctx); err != nil {
			a.log.Printf("hello: %v", err)
			if !sleepCtx(ctx, a.cfg.RetryInterval) {
				return nil
			}
			continue
		}
		a.pollLoop(ctx)
	}
	return nil
}

// connect builds the mTLS client once the agent holds a certificate. The
// timeout must exceed the control plane's poll window, which the agent
// does not know, so it is generous.
func (a *Agent) connect() error {
	tlsCfg, err := transport.ClientTLS(a.cfg.agentCert(), a.cfg.agentKey(), a.cfg.caCert())
	if err != nil {
		return err
	}
	a.client = transport.NewJSONClient(a.cfg.Master, tlsCfg, 5*time.Minute)
	return nil
}

// pollLoop asks for work until a poll fails, at which point Run says hello
// again — a failed poll usually means the control plane restarted.
func (a *Agent) pollLoop(ctx context.Context) {
	for ctx.Err() == nil {
		var jobs []transport.Job
		if err := a.client.Get(ctx, transport.PathJobs, &jobs); err != nil {
			if ctx.Err() == nil {
				a.log.Printf("poll: %v", err)
				sleepCtx(ctx, a.cfg.RetryInterval)
			}
			return
		}
		for _, job := range jobs {
			result := a.execute(ctx, job)
			if err := a.client.Post(ctx, transport.PathResults, result, nil); err != nil {
				a.log.Printf("job %s: reporting result: %v", job.ID, err)
			}
		}
	}
}

func (a *Agent) sayHello(ctx context.Context) error {
	body := transport.HelloRequest{Grains: a.grains, Version: a.cfg.Version}
	return a.client.Post(ctx, transport.PathHello, body, nil)
}

// raiseBeacon posts a beacon event to the control plane. A failure is
// logged and dropped: the next check will report the condition again if it
// still holds, and blocking a watcher on a flaky connection helps nobody.
func (a *Agent) raiseBeacon(name string, data map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ev := transport.Event{
		Tag:  fmt.Sprintf(event.TagBeacon, a.cfg.ID, name),
		Time: time.Now().UTC(),
		Data: data,
	}
	if err := a.client.Post(ctx, transport.PathEvents, ev, nil); err != nil {
		a.log.Printf("beacon %s: %v", name, err)
		return
	}
	a.log.Printf("beacon %s fired: %v", name, data)
}

// publishMine sends one function's output to the control plane. As with
// beacons, a failure is logged and dropped: the next tick republishes.
func (a *Agent) publishMine(function string, data map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report := transport.MineReport{Function: function, Data: data}
	if err := a.client.Post(ctx, transport.PathMine, report, nil); err != nil {
		a.log.Printf("mine %s: %v", function, err)
	}
}

// fetchMine reads the fleet's published facts, for {{ .Mine }} in states.
// A failure is not fatal: a state tree that does not use the mine should
// not fail because the mine is empty or unreachable.
func (a *Agent) fetchMine(ctx context.Context) map[string]any {
	var raw transport.Mine
	if err := a.client.Get(ctx, transport.PathMine, &raw); err != nil {
		a.log.Printf("mine: %v", err)
		return map[string]any{}
	}
	return mine.ForTemplates(raw)
}

// fetchPillar asks the control plane to render this agent's pillar.
func (a *Agent) fetchPillar(ctx context.Context) (map[string]any, error) {
	var data map[string]any
	if err := a.client.Get(ctx, transport.PathPillar, &data); err != nil {
		return nil, err
	}
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

// fetchStateTree replaces the cached tree with the control plane's copy.
// The download lands in a staging directory and is swapped into place, so
// an interrupted fetch never leaves a half-written tree to run against.
func (a *Agent) fetchStateTree(ctx context.Context) (string, error) {
	body, err := a.client.Stream(ctx, transport.PathStateTree)
	if err != nil {
		return "", err
	}
	defer body.Close()

	staging := filepath.Join(a.cfg.CacheDir, "states.incoming")
	final := filepath.Join(a.cfg.CacheDir, "states")
	previous := filepath.Join(a.cfg.CacheDir, "states.previous")

	if err := os.RemoveAll(staging); err != nil {
		return "", err
	}
	if err := archive.UnpackTarGz(body, staging); err != nil {
		return "", fmt.Errorf("unpack state tree: %w", err)
	}
	if err := os.RemoveAll(previous); err != nil {
		return "", err
	}
	if err := os.Rename(final, previous); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(staging, final); err != nil {
		return "", err
	}
	_ = os.RemoveAll(previous)
	return final, nil
}

// sleepCtx waits for d, reporting false if ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
