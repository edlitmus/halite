package main

import (
	"context"
	"time"

	"github.com/edlitmus/halite/internal/config"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/tracing"
	"github.com/edlitmus/halite/internal/version"
)

// Tracing on the hub: SPEC 26.3's span per job, and where the trace
// starts.
//
// The hub is where a trace begins, because the hub is where a job
// begins. Its dispatch span's identity goes onto the job message, each
// node continues from it, and the states and file transfers that job
// causes are the rest of the trace. A node with no hub starts its own
// root instead, so a `--local` highstate is still one trace; it is
// simply a shorter one.

// buildTracer reads the three settings and returns the tracer this hub
// runs with, or nil.
//
// A configuration error is logged and tracing is left off. It is never
// fatal: a hub that refuses to start because a collector URL has a typo
// in it takes a fleet's management with it, and no telemetry is worth
// that. The operator is told once, at startup, at error level.
func buildTracer(cfg *config.Config, log *hlog.Logger, service string) *tracing.Tracer {
	t, err := tracing.TracerFrom(tracing.Settings{
		Mode:          cfg.String("tracing", "off"),
		Endpoint:      cfg.String("tracing_endpoint", ""),
		SampleRate:    cfg.Float("tracing_sample_rate", tracing.DefaultSampleRate),
		SampleRateSet: cfg.IsSet("tracing_sample_rate"),
		Service:       service,
		Version:       version.String(),
	})
	if err != nil {
		log.Error("tracing is configured incorrectly and is off",
			"error", err.Error(), "component", "tracing")
		return nil
	}
	t.Start()
	return t
}

// stopTracer drains the queue on the way out.
//
// Bounded at five seconds: a hub being restarted must not wait on a
// collector that has gone away, and the spans worth having are already
// batched by then.
func stopTracer(t *tracing.Tracer, log *hlog.Logger) {
	if t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Stop(ctx)
	if dropped := t.Dropped(); dropped > 0 {
		log.Warn("spans were dropped because the export queue was full",
			"dropped", dropped, "component", "tracing")
	}
}
