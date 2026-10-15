package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/log"
)

// loginFrom posts a login as if from remote, which httptest's real
// listener cannot do: every connection to it is from 127.0.0.1.
func loginFrom(t *testing.T, s *Server, remote, body string) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, PathLogin, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("Retry-After"), rec.Body.String()
}

const (
	attacker = "198.51.100.7:40000"
	owner    = "203.0.113.9:50000"
)

// DIVERGENCE 5.290: keyed by name alone, one failure each time the
// window opened kept a name refused all day, for its owner as well. The
// day is simulated against the limiter itself, with no hashing: an
// attacker at one address fails the moment each window opens, and at
// every one of those moments the owner's address is not held.
func TestAnAttackerFailingAtEveryWindowDoesNotHoldTheOwnersAddress(t *testing.T) {
	lim := newLoginLimit()
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	end := now.Add(24 * time.Hour)
	attackerKey := loginKey("local", "198.51.100.7", "ed")
	ownerKey := loginKey("local", "203.0.113.9", "ed")
	attempts, attackerHeld := 0, time.Duration(0)
	for now.Before(end) {
		if w := lim.wait(attackerKey, now); w > 0 {
			attackerHeld += w
			now = now.Add(w)
			continue
		}
		lim.failed(attackerKey, now)
		attempts++
		if w := lim.wait(ownerKey, now); w > 0 {
			t.Fatalf("after attempt %d the owner's address was held for %v", attempts, w)
		}
	}
	// The attack ran, and held the address it came from: the property
	// is about the owner, not about the attacker getting through.
	if attempts < 90 || attackerHeld < 23*time.Hour {
		t.Fatalf("the simulation made %d attempts and held the attacker for %v; it did not run the attack", attempts, attackerHeld)
	}
}

// The same, through the endpoint: a name in its backoff window from one
// address is refused there, even with the right password, and the right
// password from another address is accepted at the same instant.
func TestALoginBackedOffAtOneAddressIsAcceptedFromAnother(t *testing.T) {
	l := newLab(t)
	now := time.Now()
	l.server.Now = func() time.Time { return now }
	lim := l.server.logins()

	if code, _, body := loginFrom(t, l.server, attacker, `{"username":"ed","password":"wrong"}`); code != http.StatusUnauthorized {
		t.Fatalf("the attacker's failure answered %d: %s", code, body)
	}
	// Grown to the longest window without hashing ten more times.
	for i := 0; i < 10; i++ {
		lim.failed(loginKey("local", "198.51.100.7", "ed"), now)
	}
	if code, wait, body := loginFrom(t, l.server, attacker, `{"username":"ed","password":"wrong"}`); code != http.StatusTooManyRequests || wait != "900" {
		t.Fatalf("the attacker inside its window answered %d, Retry-After %q: %s", code, wait, body)
	}
	// The decision recorded in 5.290: from the address being backed
	// off, a right password is refused too, or the backoff would answer
	// every guess for free.
	if code, _, _ := loginFrom(t, l.server, attacker, `{"username":"ed","password":"hunter2"}`); code != http.StatusTooManyRequests {
		t.Errorf("the right password from the backed-off address answered %d", code)
	}
	if code, _, body := loginFrom(t, l.server, owner, `{"username":"ed","password":"hunter2"}`); code != http.StatusOK {
		t.Fatalf("the owner from another address answered %d: %s", code, body)
	}
	// And the owner's success does not clear the attacker's window.
	if code, _, _ := loginFrom(t, l.server, attacker, `{"username":"ed","password":"wrong"}`); code != http.StatusTooManyRequests {
		t.Errorf("the owner's login cleared the attacker's backoff: %d", code)
	}
}

// One address cycling through names had a fresh window for each. Its
// failures now share a budget: past it, every name is refused from that
// address, other addresses are not, and the budget drains with time.
func TestSprayingNamesFromOneAddressIsThrottled(t *testing.T) {
	l := newLab(t)
	now := time.Now()
	l.server.Now = func() time.Time { return now }
	lim := l.server.logins()

	// All but one of the budget spent without hashing; the last through
	// the endpoint, so a handler that never charged the budget fails here.
	for i := 0; i < loginSourceBurst-1; i++ {
		lim.sourceFailed("198.51.100.7", now)
	}
	if code, _, body := loginFrom(t, l.server, attacker, `{"username":"spray-last","password":"x"}`); code != http.StatusUnauthorized {
		t.Fatalf("the last failure in the budget answered %d: %s", code, body)
	}
	code, wait, body := loginFrom(t, l.server, attacker, `{"username":"spray-fresh","password":"x"}`)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "from this address") || wait != "30" {
		t.Fatalf("a fresh name past the budget answered %d, Retry-After %q: %s", code, wait, body)
	}
	if code, _, _ := loginFrom(t, l.server, attacker, `{"username":"ed","password":"hunter2"}`); code != http.StatusTooManyRequests {
		t.Errorf("past the budget the right password from that address answered %d", code)
	}
	if code, _, body := loginFrom(t, l.server, owner, `{"username":"spray-fresh","password":"x"}`); code != http.StatusUnauthorized {
		t.Errorf("another address was held by the sprayer's budget: %d %s", code, body)
	}
	now = now.Add(loginSourceDrain)
	if code, _, body := loginFrom(t, l.server, attacker, `{"username":"spray-later","password":"x"}`); code != http.StatusUnauthorized {
		t.Errorf("after one drain the address answered %d: %s", code, body)
	}
}

// An LDAP failure is charged to the same budget as a local one.
func TestAnLDAPFailureSpendsTheSourcesBudget(t *testing.T) {
	l := ldapLab(t, nil)
	lim := l.server.logins()
	now := l.server.now()
	l.server.Now = func() time.Time { return now }
	for i := 0; i < loginSourceBurst-1; i++ {
		lim.sourceFailed("198.51.100.7", now)
	}
	if code, _, body := loginFrom(t, l.server, attacker, `{"username":"ed","password":"wrong","eauth":"ldap"}`); code != http.StatusUnauthorized {
		t.Fatalf("the LDAP failure answered %d: %s", code, body)
	}
	if code, _, body := loginFrom(t, l.server, attacker, `{"username":"other","password":"wrong","eauth":"ldap"}`); code != http.StatusTooManyRequests {
		t.Errorf("the LDAP failure did not spend the budget: %d %s", code, body)
	}
}

// A name past the bound is refused before it is keyed, stored or logged.
func TestAnOversizedUsernameIsRefusedBeforeItIsKeptOrLogged(t *testing.T) {
	l := newLab(t)
	var logged bytes.Buffer
	logger, err := log.New(log.Options{Level: log.Debug, Stderr: &logged})
	if err != nil {
		t.Fatal(err)
	}
	l.server.Log = logger
	lim := l.server.logins()

	long := "x" + strings.Repeat("y", loginNameMax)
	code, _, body := loginFrom(t, l.server, attacker, `{"username":"`+long+`","password":"x"}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "longer than 256 bytes") {
		t.Fatalf("a %d-byte name answered %d: %s", len(long), code, body)
	}
	if strings.Contains(logged.String(), "yyyyyyyy") || strings.Contains(body, "yyyyyyyy") {
		t.Errorf("the oversized name was logged or echoed:\nlog: %s\nbody: %s", logged.String(), body)
	}
	if len(lim.names) != 0 || len(lim.sources) != 0 {
		t.Errorf("the oversized name was kept: %d names, %d sources", len(lim.names), len(lim.sources))
	}
	// LDAP is refused the same way, before it reaches the directory.
	if code, _, _ := loginFrom(t, l.server, attacker, `{"username":"`+long+`","password":"x","eauth":"ldap"}`); code != http.StatusBadRequest {
		t.Errorf("an oversized LDAP name answered %d", code)
	}
	// The bound itself is allowed through to the ordinary failure.
	exact := strings.Repeat("z", loginNameMax)
	if code, _, body := loginFrom(t, l.server, attacker, `{"username":"`+exact+`","password":"x"}`); code != http.StatusUnauthorized {
		t.Errorf("a name of exactly the bound answered %d: %s", code, body)
	}
}

// The source is the connection's peer: a header cannot choose it, an
// IPv6 host is held by its /64, and an IPv4-mapped peer is its IPv4.
func TestALoginsSourceIsThePeerAndAnIPv6PeerIsItsSlash64(t *testing.T) {
	for _, c := range []struct{ remote, want string }{
		{"198.51.100.7:1234", "198.51.100.7"},
		{"[::ffff:198.51.100.7]:1234", "198.51.100.7"},
		{"[2001:db8:1:2:aaaa::1]:1234", "2001:db8:1:2::/64"},
		{"[2001:db8:1:2:bbbb::9]:9", "2001:db8:1:2::/64"},
		{"[fe80::1%en0]:1234", "fe80::/64"},
		{"not-an-address", "not-an-address"},
	} {
		req := httptest.NewRequest(http.MethodPost, PathLogin, nil)
		req.RemoteAddr = c.remote
		req.Header.Set("X-Forwarded-For", "192.0.2.1")
		req.Header.Set("X-Real-IP", "192.0.2.1")
		if got := loginSource(req); got != c.want {
			t.Errorf("%s: the source is %q, want %q", c.remote, got, c.want)
		}
	}
}

// The source table is bounded like the name table, and a full one gives
// up the address with the least spent, so a flood from fresh addresses
// cannot reset the budget of the address it covers for.
func TestAFloodOfSourcesCannotResetTheSprayersBudget(t *testing.T) {
	lim := newLoginLimit()
	now := time.Now()
	for i := 0; i < loginSourceBurst; i++ {
		lim.sourceFailed("sprayer", now)
	}
	for i := 0; i < loginSources+500; i++ {
		lim.sourceFailed(fmt.Sprintf("flood-%d", i), now)
	}
	if len(lim.sources) > loginSources {
		t.Errorf("the table holds %d sources; the bound is %d", len(lim.sources), loginSources)
	}
	if lim.sourceWait("sprayer", now) <= 0 {
		t.Error("the flood evicted the sprayer's budget")
	}
}
