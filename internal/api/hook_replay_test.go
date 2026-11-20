package api

import (
	"net/http"
	"testing"
	"time"
)

// Two hmac hooks with different replay windows, on a fixed clock.
const twoWindowHooks = "hooks:\n" +
	"  slow:\n    auth: hmac\n    secret: s3cret\n    replay_window: 1h\n    rate_limit: 100\n" +
	"  fast:\n    auth: hmac\n    secret: s3cret\n    replay_window: 1m\n    rate_limit: 100\n"

func fixedClockHookLab(t *testing.T) (*lab, *time.Time) {
	t.Helper()
	l, _ := hookLab(t, twoWindowHooks)
	now := time.Now().UTC().Truncate(time.Second)
	l.server.Now = func() time.Time { return now }
	return l, &now
}

// A delivery to a hook with a short window does not forget another hook's
// deliveries. The cache was pruned with the window of the hook being called,
// so a delivery to `fast` two minutes later dropped `slow`'s record, and
// `slow` accepted the same signature again (GHSA-5g95-mq9j-vphm, DIVERGENCE
// 5.684).
func TestAShortWindowedHookDoesNotForgetAnotherHooksDeliveries(t *testing.T) {
	l, now := fixedClockHookLab(t)
	body, stamp := `{"a":1}`, now.Format(time.RFC3339)
	signature := sign("s3cret", stamp, body)
	if res, out := l.deliver(t, "slow", body, stamp, signature); res.StatusCode != http.StatusAccepted {
		t.Fatalf("the first delivery answered %d: %s", res.StatusCode, out)
	}

	*now = now.Add(2 * time.Minute)
	fresh := now.Format(time.RFC3339)
	if res, out := l.deliver(t, "fast", `{"b":1}`, fresh, sign("s3cret", fresh, `{"b":1}`)); res.StatusCode != http.StatusAccepted {
		t.Fatalf("the delivery to fast answered %d: %s", res.StatusCode, out)
	}

	if res, out := l.deliver(t, "slow", body, stamp, signature); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("slow's delivery, replayed inside its hour, answered %d: %s", res.StatusCode, out)
	}
}

// A signature stamped ahead of the clock is remembered for as long as it
// verifies. The record was kept for one window from delivery, and the
// signature verifies until its own timestamp plus the window, so a delivery
// stamped 59 s ahead could be replayed 90 s later (DIVERGENCE 5.684).
func TestASignatureStampedAheadIsRememberedWhileItVerifies(t *testing.T) {
	l, now := fixedClockHookLab(t)
	body, stamp := `{"c":1}`, now.Add(59*time.Second).Format(time.RFC3339)
	signature := sign("s3cret", stamp, body)
	if res, out := l.deliver(t, "fast", body, stamp, signature); res.StatusCode != http.StatusAccepted {
		t.Fatalf("the first delivery answered %d: %s", res.StatusCode, out)
	}

	*now = now.Add(90 * time.Second) // the stamp is 31 s old: inside the minute
	if res, out := l.deliver(t, "fast", body, stamp, signature); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("the replay 90 s later answered %d: %s", res.StatusCode, out)
	}
}

// A record goes once its signature can no longer verify, so the cache stays
// bounded by what the windows allow, and that replay is refused by the window
// itself.
func TestARecordGoesOnceItsSignatureCannotVerify(t *testing.T) {
	l, now := fixedClockHookLab(t)
	body, stamp := `{"d":1}`, now.Format(time.RFC3339)
	signature := sign("s3cret", stamp, body)
	if res, _ := l.deliver(t, "fast", body, stamp, signature); res.StatusCode != http.StatusAccepted {
		t.Fatalf("the first delivery answered %d", res.StatusCode)
	}

	*now = now.Add(61 * time.Second)
	fresh := now.Format(time.RFC3339)
	if res, _ := l.deliver(t, "fast", `{"e":1}`, fresh, sign("s3cret", fresh, `{"e":1}`)); res.StatusCode != http.StatusAccepted {
		t.Fatalf("a fresh delivery answered %d", res.StatusCode)
	}
	hooks := l.server.hooks()
	hooks.mu.Lock()
	records := len(hooks.seen)
	hooks.mu.Unlock()
	if records != 1 {
		t.Errorf("%d records kept, want 1: the expired one should have gone", records)
	}
	res, out := l.deliver(t, "fast", body, stamp, signature)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("the expired signature answered %d: %s", res.StatusCode, out)
	}
}
