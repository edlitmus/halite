package tracing

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Settings is a process's tracing configuration, already read.
//
// Taken as a struct of plain values rather than as a *config.Config so
// that internal/tracing does not depend on internal/config: the hub, the
// node and the API each read their own three settings and hand them
// over, and a test builds one without a configuration file.
type Settings struct {
	// Mode is `off` or `otlp`, as SPEC 26.3's `tracing` key spells it.
	Mode string
	// Endpoint is the collector's base URL, or its full traces URL.
	Endpoint string
	// SampleRate is the fraction of root traces recorded, 0 to 1.
	SampleRate float64
	// SampleRateSet distinguishes "the operator wrote 0" from "the
	// operator wrote nothing". They mean opposite things and a float
	// cannot tell them apart.
	SampleRateSet bool

	// Service and Version identify this process in every exported span.
	Service string
	Version string
}

// Modes.
const (
	ModeOff  = "off"
	ModeOTLP = "otlp"
)

// DefaultSampleRate is what an operator gets who turns tracing on and
// says nothing about sampling.
//
// SPEC 26.3 says "sampled when on", so the default cannot be 1. A tenth
// is chosen to be useful on a small estate rather than statistically
// tidy on a large one: at 1% a five-host fleet applying a highstate
// every half hour records roughly one trace a day, which is a feature
// that appears not to work.
const DefaultSampleRate = 0.1

// DefaultEndpoint is where an OTLP/HTTP collector listens by default.
const DefaultEndpoint = "http://127.0.0.1:4318"

// TracerFrom builds the tracer a process runs with.
//
// It returns (nil, nil) when tracing is off, and a nil *Tracer is the
// off switch throughout: every method tolerates a nil receiver, so a
// caller never branches on whether tracing is configured.
//
// # What it refuses, and why refusing is not fatal
//
// A mode that is not one of the two names, an endpoint that is not a
// URL, and a sample rate outside 0..1 are all errors, because each of
// them is an operator asking for something and silently getting nothing.
// An explicit `tracing_sample_rate: 0` alongside `tracing: otlp` is
// refused for the same reason: it is indistinguishable in behaviour from
// `tracing: off` and distinguishable in intent, and the operator who
// wrote it believed they had turned something on.
//
// The caller's response to that error must be to log it and run without
// tracing, never to refuse to start. Telemetry is allowed to lose data;
// a fleet is not allowed to stop because a collector URL has a typo in
// it. The refusal exists so that the operator is told, not so that the
// node is.
func TracerFrom(s Settings) (*Tracer, error) {
	mode := strings.TrimSpace(strings.ToLower(s.Mode))
	switch mode {
	case "", ModeOff:
		return nil, nil
	case ModeOTLP:
	default:
		return nil, fmt.Errorf("`tracing` is %q; it is %q or %q", s.Mode, ModeOff, ModeOTLP)
	}

	rate := s.SampleRate
	switch {
	case !s.SampleRateSet:
		rate = DefaultSampleRate
	case rate == 0:
		return nil, fmt.Errorf(
			"`tracing` is %q and `tracing_sample_rate` is 0, which records nothing; "+
				"set a rate above 0, or set `tracing: %s` if that is what you mean",
			ModeOTLP, ModeOff)
	case rate < 0 || rate > 1:
		return nil, fmt.Errorf("`tracing_sample_rate` is %v; it is a fraction between 0 and 1", rate)
	}

	endpoint := strings.TrimSpace(s.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("`tracing_endpoint` %q is not a URL; it looks like %q", s.Endpoint, DefaultEndpoint)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("`tracing_endpoint` %q is %s; OTLP over HTTP is http or https", s.Endpoint, u.Scheme)
	}

	return &Tracer{
		Service: s.Service,
		Version: s.Version,
		Sampler: RatioSampler{Ratio: rate},
		Export:  &OTLPExporter{Endpoint: endpoint, Timeout: 10 * time.Second},
	}, nil
}
