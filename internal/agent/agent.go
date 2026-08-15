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
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/archive"
	"github.com/edlitmus/halite/internal/beacon"
	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/event"
	"github.com/edlitmus/halite/internal/mine"
	"github.com/edlitmus/halite/internal/schedule"
	"github.com/edlitmus/halite/internal/transport"
)

// Config is what an agent needs to reach its control plane.
type Config struct {
	ID string // this agent's identity; defaults to the host grain
	// Masters are control planes to try, in order. More than one is for
	// failover: they must share a CA, and an agent uses one at a time.
	Masters  []string
	PKIDir   string // holds ca.crt, agent.key, agent.crt
	CacheDir string // where the fetched state tree lives
	Version  string // reported to the control plane
	// RetryInterval paces reconnection and enrollment attempts.
	RetryInterval time.Duration
	// RenewBefore is how much of the certificate's life may remain when
	// the agent asks for a new one. Zero means ca.RenewBefore.
	RenewBefore time.Duration

	// Beacons watch the host and raise events.
	Beacons []beacon.Beacon

	// MineJobs are facts this agent publishes for the rest of the fleet.
	MineJobs []mine.Job

	// Schedule is work this agent runs on its own clock.
	Schedule []schedule.Job
}

func (c *Config) withDefaults() {
	if c.RetryInterval == 0 {
		c.RetryInterval = 10 * time.Second
	}
	if c.RenewBefore == 0 {
		c.RenewBefore = ca.RenewBefore
	}
}

func (c Config) caCert() string    { return filepath.Join(c.PKIDir, "ca.crt") }
func (c Config) agentKey() string  { return filepath.Join(c.PKIDir, "agent.key") }
func (c Config) agentCert() string { return filepath.Join(c.PKIDir, "agent.crt") }

// Agent is a running agent.
type Agent struct {
	cfg    Config
	log    *log.Logger
	grains map[string]any

	// client is replaced on failover while beacons and the mine are
	// publishing through it, so it is only reached under the lock.
	mu     sync.RWMutex
	client *transport.Client

	// expiresAt is when the certificate this agent holds stops working,
	// and nextRenewal is the earliest another renewal may be attempted
	// after one failed. Both belong to the Run goroutine.
	expiresAt   time.Time
	nextRenewal time.Time
}

// currentClient returns the control plane connection in use, or nil before
// the first one is established.
func (a *Agent) currentClient() *transport.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

func (a *Agent) setClient(client *transport.Client) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.client = client
}

// New builds an agent. Grains are collected once by the caller: they
// describe the host, and a long-running agent that re-derived them on every
// poll would spend most of its life shelling out.
func New(cfg Config, grains map[string]any, logger *log.Logger) (*Agent, error) {
	cfg.withDefaults()
	if len(cfg.Masters) == 0 {
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

// Run enrolls if needed, then works for a control plane until cancelled,
// failing over to the next configured address whenever the connection
// breaks.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.ensureEnrolled(ctx); err != nil {
		return err
	}
	// ensureEnrolled has just made sure there is a usable certificate, so
	// what is left to decide is whether it is close enough to expiring to
	// renew on the first connection.
	if expiry, err := a.certExpiry(); err == nil {
		a.expiresAt = expiry
		if time.Until(expiry) <= a.cfg.RenewBefore {
			a.log.Printf("certificate expires %s; renewing on the first connection",
				expiry.Format(time.RFC3339))
		}
	}

	started := false
	for ctx.Err() == nil {
		// Built inside the loop: a renewal replaces the certificate on
		// disk, and the next connection has to be made with the new one.
		tlsCfg, err := transport.ClientTLS(a.cfg.agentCert(), a.cfg.agentKey(), a.cfg.caCert())
		if err != nil {
			return err
		}
		connected := a.connectSomewhere(ctx, tlsCfg)
		if !connected {
			if !sleepCtx(ctx, a.cfg.RetryInterval) {
				return nil
			}
			continue
		}
		if !started {
			// Beacons and the mine run for the life of the agent, not of one
			// connection: a host whose control plane is unreachable should
			// still notice its disk filling and report it when it returns.
			a.startWatchers(ctx)
			started = true
		}
		a.pollLoop(ctx)
	}
	return nil
}

// connectSomewhere tries each control plane in order until one answers a
// hello. Every reconnection starts from the top of the list, so an agent
// that failed over returns to its preferred master once it is back.
func (a *Agent) connectSomewhere(ctx context.Context, tlsCfg *tls.Config) bool {
	for _, addr := range a.cfg.Masters {
		if ctx.Err() != nil {
			return false
		}
		// The timeout must exceed the control plane's poll window, which the
		// agent does not know, so it is generous.
		a.setClient(transport.NewJSONClient(addr, tlsCfg, 5*time.Minute))
		if err := a.sayHello(ctx); err != nil {
			if ctx.Err() == nil {
				a.log.Printf("%s: %v", addr, err)
			}
			continue
		}
		a.log.Printf("agent %q connected to %s", a.cfg.ID, addr)
		return true
	}
	return false
}

func (a *Agent) startWatchers(ctx context.Context) {
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
	if len(a.cfg.Schedule) > 0 {
		runner := schedule.NewRunner(a.cfg.Schedule, a.runScheduled, a.log)
		a.log.Printf("running %d scheduled job(s)", runner.Count())
		go runner.Run(ctx)
	}
}

// runScheduled runs one scheduled job and reports it on the event bus.
// Scheduled work is the agent's own, not the control plane's: it carries no
// dispatched job to answer, so it is announced rather than returned.
func (a *Agent) runScheduled(ctx context.Context, job schedule.Job) {
	result := a.execute(ctx, transport.Job{
		ID:   fmt.Sprintf("schedule/%s/%d", job.Name, time.Now().UnixNano()),
		Kind: job.Kind,
		SLS:  job.SLS,
		Fn:   job.Fn,
		Args: job.Args,
		Test: job.Test,
	})
	data := map[string]any{
		"job":       job.Name,
		"kind":      job.Kind,
		"result":    result.Ok,
		"succeeded": result.Succeeded,
		"failed":    result.Failed,
		"changed":   result.Changed,
		"duration":  result.Duration.String(),
		"test":      job.Test,
	}
	if result.Error != "" {
		data["error"] = result.Error
	}
	a.postEvent(fmt.Sprintf(event.TagSchedule, a.cfg.ID, job.Name), data)
	a.log.Printf("schedule %s: ok=%v changed=%d failed=%d",
		job.Name, result.Ok, result.Changed, result.Failed)
}

// pollLoop asks for work until a poll fails, at which point Run says hello
// again — a failed poll usually means the control plane restarted. It also
// returns after renewing the certificate, so the next connection is made
// with the new one.
func (a *Agent) pollLoop(ctx context.Context) {
	client := a.currentClient()
	for ctx.Err() == nil {
		if a.renewIfDue(ctx) {
			return // reconnect with the certificate that was just issued
		}
		var jobs []transport.Job
		if err := client.Get(ctx, transport.PathJobs, &jobs); err != nil {
			if ctx.Err() == nil {
				a.log.Printf("poll: %v", err)
				sleepCtx(ctx, a.cfg.RetryInterval)
			}
			return
		}
		for _, job := range jobs {
			result := a.execute(ctx, job)
			if err := client.Post(ctx, transport.PathResults, result, nil); err != nil {
				a.log.Printf("job %s: reporting result: %v", job.ID, err)
			}
		}
	}
}

func (a *Agent) sayHello(ctx context.Context) error {
	client := a.currentClient()
	if client == nil {
		return fmt.Errorf("not connected")
	}
	helloCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body := transport.HelloRequest{Grains: a.grains, Version: a.cfg.Version}
	return client.Post(helloCtx, transport.PathHello, body, nil)
}

// raiseBeacon posts a beacon event to the control plane. A failure is
// logged and dropped: the next check will report the condition again if it
// still holds, and blocking a watcher on a flaky connection helps nobody.
func (a *Agent) raiseBeacon(name string, data map[string]any) {
	if a.postEvent(fmt.Sprintf(event.TagBeacon, a.cfg.ID, name), data) {
		a.log.Printf("beacon %s fired: %v", name, data)
	}
}

// postEvent puts one event on the control plane's bus, reporting whether it
// landed. A disconnected agent drops it rather than queueing: the next
// check or run says the same thing again.
func (a *Agent) postEvent(tag string, data map[string]any) bool {
	client := a.currentClient()
	if client == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ev := transport.Event{Tag: tag, Time: time.Now().UTC(), Data: data}
	if err := client.Post(ctx, transport.PathEvents, ev, nil); err != nil {
		a.log.Printf("event %s: %v", tag, err)
		return false
	}
	return true
}

// publishMine sends one function's output to the control plane. As with
// beacons, a failure is logged and dropped: the next tick republishes.
func (a *Agent) publishMine(function string, data map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := a.currentClient()
	if client == nil {
		return
	}
	report := transport.MineReport{Function: function, Data: data}
	if err := client.Post(ctx, transport.PathMine, report, nil); err != nil {
		a.log.Printf("mine %s: %v", function, err)
	}
}

// fetchMine reads the fleet's published facts, for {{ .Mine }} in states.
// A failure is not fatal: a state tree that does not use the mine should
// not fail because the mine is empty or unreachable.
func (a *Agent) fetchMine(ctx context.Context) map[string]any {
	var raw transport.Mine
	if err := a.currentClient().Get(ctx, transport.PathMine, &raw); err != nil {
		a.log.Printf("mine: %v", err)
		return map[string]any{}
	}
	return mine.ForTemplates(raw)
}

// fetchPillar asks the control plane to render this agent's pillar.
func (a *Agent) fetchPillar(ctx context.Context) (map[string]any, error) {
	var data map[string]any
	if err := a.currentClient().Get(ctx, transport.PathPillar, &data); err != nil {
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
	body, err := a.currentClient().Stream(ctx, transport.PathStateTree)
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
