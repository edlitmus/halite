package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/metrics"
)

// A path the API does not serve is not a route. The catch-all that answers
// one needs no token, and `routeOf` returned the path itself, so a few
// hundred different paths used up the route families' series bound (and left
// a series in the in-flight gauge for each), after which every route first
// seen later was counted under `__overflow__`.
func TestUnknownPathsShareOneRouteLabel(t *testing.T) {
	l, _ := executeLab(t, metricsPolicy)
	token := l.login(t, "ed", "hunter2").Token
	for i := 0; i < metrics.MaxSeries+100; i++ {
		l.get(t, fmt.Sprintf("/scan/%d", i), "")
	}
	l.get(t, PathJob+"20260824T101500.000000", token)

	_, body := l.get(t, PathMetrics, token)
	if strings.Contains(body, "/scan/") {
		t.Errorf("a path nobody serves became a route label:\n%s", section(body, "halite_api_requests_total"))
	}
	if strings.Contains(body, metrics.OverflowLabel) {
		t.Errorf("unknown paths filled the route family and spilled into %s", metrics.OverflowLabel)
	}
	if !strings.Contains(body, `halite_api_requests_total{route="/v1/jobs/{id}",code="`) {
		t.Errorf("a real route is not counted under its own label:\n%s", section(body, "halite_api_requests_total"))
	}
}

// A hook's path is the caller's text, and the hook route is reachable without
// a token, so it is not a route label either.
//
// `routeOf` kept the tail after the first segment for every prefix it knew,
// on the reasoning that `/v1/nodes/{id}/state` and `/v1/nodes/{id}` are
// different routes. That is true of the node routes, which have a handful of
// fixed tails, and false of `/v1/hook/{path...}`, where the tail is whatever
// the sender wrote: a scanner posting to a few hundred distinct hook paths
// used up the route families' bound, as the unknown paths had (DIVERGENCE
// 5.510), and the same for a hook path on a method the route does not take,
// which the mux answers 405 with no pattern to compare with `/`.
// DIVERGENCE 5.529.
func TestHookPathsShareOneRouteLabel(t *testing.T) {
	l, _ := executeLab(t, metricsPolicy)
	token := l.login(t, "ed", "hunter2").Token
	for i := 0; i < metrics.MaxSeries+100; i++ {
		l.post(t, fmt.Sprintf("%sscan/%d", PathHook, i), `{}`, "")
		l.get(t, fmt.Sprintf("%sscan/%d", PathHook, i), "")
	}
	l.get(t, PathJob+"20260824T101500.000000", token)

	_, body := l.get(t, PathMetrics, token)
	if strings.Contains(body, "scan/") || strings.Contains(body, `/v1/hook/{id}/`) {
		t.Errorf("a hook path became a route label:\n%s", section(body, "halite_api_requests_total"))
	}
	if strings.Contains(body, metrics.OverflowLabel) {
		t.Errorf("hook paths filled the route family and spilled into %s", metrics.OverflowLabel)
	}
	if !strings.Contains(body, `halite_api_requests_total{route="/v1/hook/{id}",code="`) {
		t.Errorf("a hook request is not counted under one label:\n%s", section(body, "halite_api_requests_total"))
	}
}
