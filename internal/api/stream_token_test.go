package api

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/apitoken"
)

// An open event stream ends when its token stops being usable. The token was
// redeemed once, as the request arrived, and the stream kept delivering
// events for as long as it stayed open: after a logout, a revocation, the
// token's own expiry (GHSA-cg5g-h9vh-8h3x, DIVERGENCE 5.685).

const streamPolicy = `
roles:
  operator:
    - target: '*'
      functions: ['*']
      runners: ['*']
bindings:
  - principal: 'local:ed'
    roles: ['operator']
`

func streamLab(t *testing.T) (*lab, *stubHub) {
	t.Helper()
	l, hub := executeLab(t, streamPolicy)
	hub.eventLines = []string{`{"_tag":"halite/job/20260824T1/new","_offset":"1:1"}`}
	hub.eventsHold = true
	l.server.tokenRecheck = 50 * time.Millisecond
	return l, hub
}

// openSSE starts GET /v1/events and returns its lines as they come, and a
// channel closed when the stream ends.
func openSSE(t *testing.T, l *lab, secret string) (<-chan string, <-chan struct{}) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, l.http.URL+PathEvents, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the stream answered %d", res.StatusCode)
	}
	lines, ended := make(chan string, 64), make(chan struct{})
	go func() {
		defer close(ended)
		defer close(lines)
		defer res.Body.Close()
		scanner := bufio.NewScanner(res.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	t.Cleanup(func() { res.Body.Close() })
	return lines, ended
}

func awaitLine(t *testing.T, lines <-chan string, prefix string) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line := <-lines:
			if strings.HasPrefix(line, prefix) {
				return line
			}
		case <-deadline:
			t.Fatalf("no line starting %q within 5s", prefix)
		}
	}
}

func awaitEnd(t *testing.T, ended <-chan struct{}, within time.Duration, why string) {
	t.Helper()
	select {
	case <-ended:
	case <-time.After(within):
		t.Fatalf("the stream was still open %s after %s", within, why)
	}
}

func TestARevokedTokensEventStreamEnds(t *testing.T) {
	l, _ := streamLab(t)
	out := l.login(t, "ed", "hunter2")
	lines, ended := openSSE(t, l, out.Token)
	awaitLine(t, lines, "event: halite/job/")

	// From another Store on the same directory, as `halite-api token
	// revoke` does from another process.
	other, err := apitoken.Open(l.server.Tokens.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Revoke(out.TokenID); err != nil {
		t.Fatal(err)
	}
	awaitEnd(t, ended, 3*time.Second, "its token was revoked")

	var said []string
	for line := range lines {
		said = append(said, line)
	}
	if !strings.Contains(strings.Join(said, "\n"), "this token was revoked") {
		t.Errorf("the stream ended without saying why: %q", said)
	}
}

// The expiry is passed on a clock the test moves, as the idle-limit test
// below does and for the same reason. It used to be a one-second token
// on the real clock, and the request opening the stream had to be
// authorised inside that second: on the macOS race CI leg it was not,
// and the stream was refused 401 before expiry could end it
// (DIVERGENCE 5.685.2).
//
// The recheck itself still runs on a real 50 ms ticker, which only sets
// how soon the moved clock is noticed; nothing here can expire early,
// because the clock does not move until the stream has delivered.
func TestAnExpiredTokensEventStreamEnds(t *testing.T) {
	l, _ := streamLab(t)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	l.server.Now, l.server.Tokens.Now = now, now

	_, secret, err := l.server.Tokens.Issue(apitoken.Options{
		Principal: "local:ed", Roles: []string{"operator"}, Lifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	lines, ended := openSSE(t, l, secret)
	awaitLine(t, lines, "event: halite/job/")
	clock.Add(int64(time.Hour + time.Second))
	awaitEnd(t, ended, 3*time.Second, "its token expired")

	var said []string
	for line := range lines {
		said = append(said, line)
	}
	if !strings.Contains(strings.Join(said, "\n"), "this token expired at") {
		t.Errorf("the stream ended without saying it expired: %q", said)
	}
}

// An open stream is the token in use, so the idle limit does not end it;
// a revocation still does.
//
// The idle limit is passed on a clock the test moves, not by waiting. It
// used to be 200 ms of real time from Issue, which the request opening the
// stream also had to land inside: on the FIPS CI leg it did not, and the
// stream was refused 401 before the idle limit could be tested at all
// (DIVERGENCE 5.685.1).
func TestAnOpenStreamIsNotEndedByTheIdleLimit(t *testing.T) {
	l, _ := streamLab(t)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	l.server.Now, l.server.Tokens.Now = now, now

	token, secret, err := l.server.Tokens.Issue(apitoken.Options{
		Principal: "local:ed", Roles: []string{"operator"},
		Lifetime: time.Hour, IdleFor: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	lines, ended := openSSE(t, l, secret)
	awaitLine(t, lines, "event: halite/job/")
	clock.Add(int64(10 * time.Minute))
	select {
	case <-ended:
		t.Fatal("the idle limit ended a stream that was open")
	case <-time.After(1 * time.Second): // twenty rechecks at streamLab's 50 ms
	}
	if _, err := l.server.Tokens.Revoke(token.ID); err != nil {
		t.Fatal(err)
	}
	awaitEnd(t, ended, 3*time.Second, "its token was revoked")
}

func TestARevokedTokensWebSocketStreamClosesSayingWhy(t *testing.T) {
	l, _ := streamLab(t)
	out := l.login(t, "ed", "hunter2")

	conn, err := net.Dial("tcp", strings.TrimPrefix(l.http.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	key := base64.StdEncoding.EncodeToString(make([]byte, 16))
	fmt.Fprintf(conn, "GET /v1/ws/events HTTP/1.1\r\nHost: 127.0.0.1\r\n"+
		"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n"+
		"Authorization: Bearer %s\r\n\r\n", key, out.Token)
	reader := bufio.NewReader(conn)
	if status, _ := reader.ReadString('\n'); !strings.Contains(status, "101") {
		t.Fatalf("the upgrade answered %q", strings.TrimSpace(status))
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	revoked := false
	for {
		opcode, payload := readServerFrame(t, reader)
		if opcode == 0x1 && !revoked {
			if _, err := l.server.Tokens.Revoke(out.TokenID); err != nil {
				t.Fatal(err)
			}
			revoked = true
			continue
		}
		if opcode != 0x8 {
			continue
		}
		if !revoked {
			t.Fatal("the stream closed before any event arrived")
		}
		if len(payload) < 2 {
			t.Fatalf("a close frame with no code: %q", payload)
		}
		code, reason := binary.BigEndian.Uint16(payload), string(payload[2:])
		if code != 1008 || !strings.Contains(reason, "revoked") {
			t.Errorf("closed with %d %q, want 1008 naming the revocation", code, reason)
		}
		return
	}
}

// readServerFrame reads one unmasked frame of under 64 KiB.
func readServerFrame(t *testing.T, r *bufio.Reader) (byte, []byte) {
	t.Helper()
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		t.Fatalf("no frame: %v", err)
	}
	n := int(header[1] & 0x7f)
	if n == 126 {
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			t.Fatal(err)
		}
		n = int(binary.BigEndian.Uint16(ext))
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		t.Fatal(err)
	}
	return header[0] & 0x0f, payload
}
