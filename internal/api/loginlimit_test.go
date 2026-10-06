package api

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/metrics"
)

// A failed login holds its name to a window that doubles, refused with
// 429 and Retry-After inside it; a refusal inside the window costs no
// hash and does not lengthen it; a success clears it. And a name with no
// account behind it gets exactly the same answers, so the 429 says
// nothing about which names are real. DIVERGENCE 5.232.
func TestAFailedLoginBacksOffItsNameWhetherOrNotTheAccountExists(t *testing.T) {
	for _, name := range []string{"ed", "nobody-by-this-name"} {
		t.Run(name, func(t *testing.T) {
			l := newLab(t)
			now := time.Now()
			l.server.Now = func() time.Time { return now }
			try := func(password string) (int, string) {
				t.Helper()
				res, body := l.post(t, PathLogin, `{"username":"`+name+`","password":"`+password+`"}`, "")
				return res.StatusCode, res.Header.Get("Retry-After") + "|" + body
			}

			if code, _ := try("wrong"); code != http.StatusUnauthorized {
				t.Fatalf("the first failure answered %d", code)
			}
			code, got := try("wrong")
			if code != http.StatusTooManyRequests || !strings.HasPrefix(got, "1|") {
				t.Errorf("a retry inside the first window answered %d %s", code, got)
			}
			now = now.Add(1100 * time.Millisecond)
			if code, _ := try("wrong"); code != http.StatusUnauthorized {
				t.Fatalf("after the first window the attempt answered %d", code)
			}
			// The second window is two seconds. A refused attempt one
			// second in must not move it.
			now = now.Add(time.Second)
			if code, _ := try("wrong"); code != http.StatusTooManyRequests {
				t.Errorf("inside the doubled window the attempt answered %d", code)
			}
			now = now.Add(1100 * time.Millisecond)
			if code, _ := try("wrong"); code != http.StatusUnauthorized {
				t.Errorf("a refusal inside the window lengthened it: answered %d", code)
			}

			if name != "ed" {
				return
			}
			now = now.Add(5 * time.Second)
			if code, _ := try("hunter2"); code != http.StatusOK {
				t.Fatalf("the right password after the window answered %d", code)
			}
			if code, _ := try("wrong"); code != http.StatusUnauthorized {
				t.Errorf("a success did not clear the backoff: answered %d", code)
			}
			if code, got := try("wrong"); code != http.StatusTooManyRequests || !strings.HasPrefix(got, "1|") {
				t.Errorf("after a success the window did not start again at one second: %d %s", code, got)
			}
		})
	}
}

// Password checks in flight are bounded: with every slot taken a login
// is refused with 503 before any hash is computed, and counted.
func TestALoginIsRefusedWhenEveryPasswordCheckSlotIsTaken(t *testing.T) {
	l := newLab(t)
	l.server.Metrics = metrics.NewRegistry()
	lim := l.server.logins()
	// Exactly as many as there are, so that an acquire that never says
	// no shows up as a login let through rather than as a hang.
	for i := 0; i < cap(lim.checking); i++ {
		lim.acquire()
	}
	res, body := l.post(t, PathLogin, `{"username":"ed","password":"hunter2"}`, "")
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "too many logins") {
		t.Errorf("a login with no slot free answered %d: %s", res.StatusCode, body)
	}
	// Without blocking: a broken acquire releases a slot it never took,
	// and a blocking release here would hang the test instead of letting
	// it say so.
	for i := 0; i < cap(lim.checking); i++ {
		select {
		case <-lim.checking:
		default:
		}
	}
	if res, body := l.post(t, PathLogin, `{"username":"ed","password":"hunter2"}`, ""); res.StatusCode != http.StatusOK {
		t.Errorf("with the slots free again the login answered %d: %s", res.StatusCode, body)
	}
	var out bytes.Buffer
	if err := l.server.Metrics.Write(&out); err != nil {
		t.Fatal(err)
	}
	metricsBody := out.String()
	if !strings.Contains(metricsBody, `halite_auth_attempts_total{method="local",result="busy"} 1`) {
		t.Errorf("the refusal was not counted:\n%s", section(metricsBody, "halite_auth_attempts_total"))
	}
}

// The table of names is bounded, and a full one gives up the name whose
// window ends soonest -- so a flood of new names cannot evict the name
// it is aimed at, whose window has grown long.
func TestAFloodOfNamesCannotClearTheBackoffOfTheNameItIsAimedAt(t *testing.T) {
	lim := newLoginLimit()
	now := time.Now()
	for i := 0; i < 10; i++ {
		lim.failed("target", now)
	}
	for i := 0; i < loginBackoffNames+500; i++ {
		lim.failed(fmt.Sprintf("flood-%d", i), now)
	}
	if len(lim.names) > loginBackoffNames {
		t.Errorf("the table holds %d names; the bound is %d", len(lim.names), loginBackoffNames)
	}
	if lim.wait("target", now) <= 0 {
		t.Error("the flood evicted the name it was aimed at")
	}
}

// The LDAP login is held to the same backoff: the directory does the
// comparing, so there is no hash to cap, but the guessing is the same.
func TestAnLDAPLoginBacksOffItsName(t *testing.T) {
	l := ldapLab(t, nil)
	if res, _ := l.post(t, PathLogin, `{"username":"ed","password":"wrong","eauth":"ldap"}`, ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the first failure answered %d", res.StatusCode)
	}
	if res, _ := l.post(t, PathLogin, `{"username":"ed","password":"wrong","eauth":"ldap"}`, ""); res.StatusCode != http.StatusTooManyRequests {
		t.Errorf("a retry inside the window answered %d", res.StatusCode)
	}
}
