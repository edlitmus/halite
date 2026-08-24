package api

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/policy"
)

// The node an event is about is read from the tag as well as from the
// envelope, because most of SPEC 17.1's namespace names it in the tag
// and the envelope carries it only when the hub set one.
func TestTheNodeIsReadOutOfTheTag(t *testing.T) {
	cases := map[string]string{
		"halite/node/web1.example/start":            "web1.example",
		"halite/beacon/web1.example/diskusage/var":  "web1.example",
		"halite/key/web1.example/accept":            "web1.example",
		"halite/job/20260824T1/ret/web1.example":    "web1.example",
		"halite/job/20260824T1/prog/web1.example/2": "web1.example",
		"halite/state/20260824T1/web1.example/ok":   "web1.example",
		"halite/mine/web1.example/update":           "web1.example",
		"halite/job/20260824T1/new":                 "",
		"halite/reactor/error":                      "",
		"halite/presence/change":                    "",
		"not-a-halite-tag":                          "",
	}
	for tag, want := range cases {
		if got := nodeFromTag(tag); got != want {
			t.Errorf("nodeFromTag(%q) = %q, want %q", tag, got, want)
		}
	}
}

// SPEC 17.4: a caller cannot subscribe to events about nodes it may not
// see. The test is target coverage, so a role that may act on `web*`
// watches `web1` whatever the function in the event happens to be —
// otherwise an operator could act on a node and not see the result.
func TestTheStreamIsFilteredByWhatTheCallerMaySee(t *testing.T) {
	loaded, _, err := policy.Load([]byte(`
roles:
  webops:
    - target: 'web*'
      functions: ['*']
  runners-only:
    - runners: ['*']
bindings:
  - principal: 'local:ed'
    roles: ['webops']
  - principal: 'local:bot'
    roles: ['runners-only']
`), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Policy: loaded}

	web := &apitoken.Token{Principal: "local:ed", Roles: []string{"webops"}}
	bot := &apitoken.Token{Principal: "local:bot", Roles: []string{"runners-only"}}
	none := &apitoken.Token{Principal: "local:nobody"}

	cases := []struct {
		tag, node string
		web, bot  bool
	}{
		// Inside the grant, named in the tag and in the envelope.
		{"halite/node/web1.example/start", "", true, false},
		{"halite/beacon/web1.example/diskusage/var", "web1.example", true, false},
		// Outside it.
		{"halite/node/db1.example/start", "", false, false},
		{"halite/state/20260824T1/db1.example/ok", "", false, false},
		// Not about a node: anyone the policy grants anything sees it.
		{"halite/job/20260824T1/new", "", true, true},
		{"halite/reactor/error", "", true, true},
	}
	for _, c := range cases {
		e := &eventbus.Event{Tag: c.tag, Node: c.node}
		if got := s.visible(web, e); got != c.web {
			t.Errorf("webops sees %q = %v, want %v", c.tag, got, c.web)
		}
		if got := s.visible(bot, e); got != c.bot {
			t.Errorf("a runners-only role sees %q = %v, want %v", c.tag, got, c.bot)
		}
		if s.visible(none, e) {
			t.Errorf("a principal bound to nothing sees %q", c.tag)
		}
	}
}

// A role that grants only runners names no target, so it says nothing
// about which nodes may be watched — and must not therefore be read as
// granting all of them.
func TestARunnerGrantDoesNotMakeEveryNodeVisible(t *testing.T) {
	loaded, _, err := policy.Load([]byte(`
roles:
  runners-only:
    - runners: ['*']
bindings:
  - principal: 'local:bot'
    roles: ['runners-only']
`), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.VisibleNode([]string{"runners-only"}, "web1.example") {
		t.Error("a runner grant made a node visible")
	}
	if !loaded.HasAnyRule([]string{"runners-only"}) {
		t.Error("a runner grant is no rule at all")
	}
}

// A WebSocket upgrade has to survive every wrapper the server puts
// around a handler.
//
// This is a lab finding: the access-log wrapper hid the connection's
// hijacker, so `/v1/ws/events` answered "this connection cannot be
// upgraded" for every caller while the endpoint's own tests — which
// called the handler directly — stayed green. The test therefore dials
// the assembled server and speaks the protocol.
func TestTheWebSocketUpgradeSurvivesTheMiddleware(t *testing.T) {
	l, hub := executeLab(t, `
roles:
  operator:
    - target: '*'
      functions: ['*']
      runners: ['*']
bindings:
  - principal: 'local:ed'
    roles: ['operator']
`)
	hub.eventLines = []string{`{"_tag":"halite/job/20260824T1/new","_offset":"1:1"}`}
	token := l.login(t, "ed", "hunter2").Token

	conn, err := net.Dial("tcp", strings.TrimPrefix(l.http.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	key := base64.StdEncoding.EncodeToString(make([]byte, 16))
	fmt.Fprintf(conn, "GET /v1/ws/events HTTP/1.1\r\nHost: %s\r\n"+
		"Upgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n"+
		"Authorization: Bearer %s\r\n\r\n",
		"127.0.0.1", key, token)

	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("no answer: %v", err)
	}
	if !strings.Contains(status, "101") {
		rest, _ := io.ReadAll(reader)
		t.Fatalf("the upgrade answered %q: %s", strings.TrimSpace(status), rest)
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

	// One unmasked text frame from the server, short enough that the
	// length is the second byte.
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		t.Fatalf("no frame: %v", err)
	}
	if header[0]&0x0F != 0x1 {
		t.Fatalf("the first frame is opcode %d, not text", header[0]&0x0F)
	}
	payload := make([]byte, int(header[1]&0x7F))
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "halite/job/20260824T1/new") {
		t.Errorf("the frame carries %s", payload)
	}
}
