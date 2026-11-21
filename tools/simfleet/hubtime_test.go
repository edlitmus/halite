package main

import (
	"strings"
	"testing"
)

// The hub's time for the run is after minus before, bucket by bucket, so
// requests over a bound are the count less those at or under it.
func TestTheHubsEventTimeIsTheRunsShareOfItsHistogram(t *testing.T) {
	before := map[string]float64{
		hubEventSeries("_count", ""): 10, hubEventSeries("_sum", ""): 1,
		hubEventSeries("_bucket", "0.1"): 10, hubEventSeries("_bucket", "0.5"): 10,
	}
	after := map[string]float64{
		hubEventSeries("_count", ""): 90, hubEventSeries("_sum", ""): 3,
		hubEventSeries("_bucket", "0.1"): 87, hubEventSeries("_bucket", "0.5"): 89,
	}
	r := eventsResult{Accepted: 80}
	if err := r.hubEventTime(before, after); err != nil {
		t.Fatal(err)
	}
	if r.HubEventRequests != 80 || r.HubEventSeconds != 2 || r.HubEventOver100ms != 3 || r.HubEventOver500ms != 1 {
		t.Fatalf("requests %.0f, seconds %g, over 100 ms %.0f, over 500 ms %.0f; want 80, 2, 3, 1",
			r.HubEventRequests, r.HubEventSeconds, r.HubEventOver100ms, r.HubEventOver500ms)
	}
}

// A run that had posts accepted and finds no series for them says so,
// rather than printing a hub that was never slow.
func TestAMissingHubEventSeriesIsAnError(t *testing.T) {
	r := eventsResult{Accepted: 1}
	err := r.hubEventTime(map[string]float64{}, map[string]float64{})
	if err == nil || !strings.Contains(err.Error(), "halite_hub_request_duration_seconds_count") {
		t.Fatalf("err %v; want one naming the missing series", err)
	}
	if err := (&eventsResult{}).hubEventTime(map[string]float64{}, map[string]float64{}); err != nil {
		t.Fatalf("a run with nothing accepted: %v", err)
	}
}
