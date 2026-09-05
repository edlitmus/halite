package transport

import "testing"

// A caller timing its requests to the hub gets a route rather than a
// path. `/v1/jobs/{jid}` is one route and a series per job identifier
// is exactly the unbounded label a metrics endpoint dies of; on the
// file server it would be one series per file in the tree.
func TestTheObservedRouteDropsTheIdentifier(t *testing.T) {
	for _, c := range []struct{ path, want string }{
		{PathHealth, PathHealth},
		{PathReturn, PathReturn},
		{PathJobs, PathJobs},
		{PathMetrics, PathMetrics},
		{PathJob + "20260904T101010101010", PathJob + "{jid}"},
		{PathJob + "20260904T101010101010/kill", PathJob + "{jid}/kill"},
		// The whole tail, not the first segment of it: here the tail
		// is the file, so anything but this is a series per file.
		{PathFiles + "base/web/nginx.conf", PathFiles + "{path}"},
		{PathFiles + "base/pillar/top.sls", PathFiles + "{path}"},
	} {
		if got := clientRoute(c.path); got != c.want {
			t.Errorf("clientRoute(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}
