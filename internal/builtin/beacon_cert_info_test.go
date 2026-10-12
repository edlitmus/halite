package builtin

import (
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The cert_info beacon, against the real x509 module.
//
// The beacon is a function over the node's own execution modules, and it
// asked x509.expires about `path` -- a parameter that function does not
// have; it takes `certificate`. Every poll therefore fired an error event
// ("argument \"path\": is not a parameter of this function") and a
// certificate about to expire was never reported, which is the one thing
// the beacon is for. Nothing noticed, because nothing had ever run the two
// together: the beacon had no test, and x509.expires' own test passes the
// right argument. It was found by configuring the beacon on a node while
// writing the handbook and reading what it put on the bus.
//
// Two paths that must agree: the call and the signature it calls.
func TestCertInfoBeaconAsksX509ExpiresAQuestionItCanAnswer(t *testing.T) {
	r := New()
	dir := t.TempDir()
	key := filepath.Join(dir, "k.pem")
	cert := filepath.Join(dir, "c.pem")
	x509Call(t, r, "x509.create_private_key", value.MapOf("path", key, "algo", "ec"))
	x509Call(t, r, "x509.create_certificate",
		value.MapOf("path", cert, "private_key", key, "CN", "x", "days_valid", int64(10)))

	files := []any{cert}

	// Ten days left, a thirty-day window: it fires, and says so.
	events := fire(t, "cert_info", value.MapOf("files", files, "notify_days", int64(30)))
	if len(events) != 1 {
		t.Fatalf("a certificate with 10 days left, watched for 30, fired %d events: %v", len(events), events)
	}
	if msg, bad := events[0].Data["error"]; bad {
		t.Fatalf("the beacon reported an error instead of an expiry: %v", msg)
	}
	if got := events[0].Data["expires_within_days"]; got != int64(30) {
		t.Errorf("expires_within_days = %v, want 30", got)
	}

	// A five-day window: nothing to say.
	if events := fire(t, "cert_info", value.MapOf("files", files, "notify_days", int64(5))); len(events) != 0 {
		t.Errorf("a certificate with 10 days left, watched for 5, fired: %v", events)
	}
}
