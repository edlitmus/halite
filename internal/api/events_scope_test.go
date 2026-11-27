package api

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/account"
)

// A job's creation carries the function, its arguments and the nodes it
// reached, and has no node of its own, so the stream's node filter let it
// through to anyone with `event.listen`: a caller scoped to web* read
// `cmd.run` and its arguments for a job sent to db1 (issue 886). A runner
// call's creation carries its arguments and has no node either.
//
// The test goes through the assembled server: the policy is loaded, the
// accounts log in, and the events are read off the stream a client reads.
const eventScopePolicy = `
roles:
  web-operator:
    - target: 'web*'
      functions: ['test.ping', 'state.apply']
    - runners: ['event.listen']
  administrator:
    - target: '*'
      functions: ['*']
      runners: ['*']
bindings:
  - principal: 'local:web'
    roles: ['web-operator']
  - principal: 'local:other'
    roles: ['web-operator']
  - principal: 'local:ed'
    roles: ['administrator']
`

// What the hub's stream answers with, one event per line.
var eventScopeEvents = []string{
	// 1: a job that reached web1 only.
	`{"_tag":"halite/job/J1/new","_offset":"1:1","data":{"jid":"J1","fun":"test.ping","nodes":["web1"],"tgt":"web1"}}`,
	// 2: a job that reached web1 and db1. It names what was run on db1.
	`{"_tag":"halite/job/J2/new","_offset":"1:2","data":{"jid":"J2","fun":"cmd.run","arg":["mysql -psecret"],"nodes":["web1","db1"],"tgt":"*"}}`,
	// 3: a job that reached db1 only.
	`{"_tag":"halite/job/J3/new","_offset":"1:3","data":{"jid":"J3","fun":"cmd.run","arg":["mysqldump"],"nodes":["db1"],"tgt":"db1"}}`,
	// 4: another operator's runner call, made through the API.
	`{"_tag":"halite/run/R1/new","_offset":"1:4","data":{"jid":"R1","fun":"pillar.show_pillar","arg":["db1"],"principal":"cert:CN=api","on_behalf_of":"local:other"}}`,
	// 5: this operator's runner call, made through the API.
	`{"_tag":"halite/run/R2/new","_offset":"1:5","data":{"jid":"R2","fun":"jobs.list_jobs","principal":"cert:CN=api","on_behalf_of":"local:web"}}`,
	// 6: the end of the other operator's run, which carries no node.
	`{"_tag":"halite/run/R1/ret","_offset":"1:6","data":{"jid":"R1","fun":"pillar.show_pillar","success":true,"principal":"cert:CN=api","on_behalf_of":"local:other"}}`,
	// 7: the compatibility copy of event 2, under the other root.
	`{"_tag":"salt/job/J2/new","_offset":"1:7","data":{"jid":"J2","fun":"cmd.run","arg":["mysql -psecret"],"nodes":["web1","db1"],"tgt":"*"}}`,
	// 8: a kill that told db1.
	`{"_tag":"halite/job/J3/kill","_offset":"1:8","data":{"jid":"J3","principal":"local:ed","told":["db1"]}}`,
	// 9: an event that is not about a job or a run: unchanged.
	`{"_tag":"halite/reactor/error","_offset":"1:9","data":{"why":"x"}}`,
}

func eventTagsSeenBy(t *testing.T, l *lab, user string) []string {
	t.Helper()
	token := l.login(t, user, "hunter2").Token
	req, err := http.NewRequest(http.MethodGet, l.http.URL+PathEvents, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, line := range strings.Split(string(body), "\n") {
		if tag, ok := strings.CutPrefix(line, "event: "); ok {
			tags = append(tags, tag)
		}
	}
	return tags
}

func TestAJobOrARunIsShownOnlyToACallerWhoseScopeItFitsInside(t *testing.T) {
	l, hub := executeLab(t, eventScopePolicy)
	hash, err := account.Hash("hunter2", account.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "other"} {
		l.server.Accounts.Accounts[name] = &account.Account{Name: name, Hash: hash}
	}
	hub.eventLines = eventScopeEvents

	cases := []struct {
		user string
		want []string
	}{
		{"web", []string{
			"halite/job/J1/new", // every node it reached is web*
			"halite/run/R2/new", // its own runner call
			"halite/reactor/error",
		}},
		{"other", []string{
			"halite/job/J1/new",
			"halite/run/R1/new", // its own runner call
			"halite/run/R1/ret", // and the end of it
			"halite/reactor/error",
		}},
		{"ed", []string{ // a target of `*` sees them all
			"halite/job/J1/new", "halite/job/J2/new", "halite/job/J3/new",
			"halite/run/R1/new", "halite/run/R2/new", "halite/run/R1/ret",
			"salt/job/J2/new", "halite/job/J3/kill", "halite/reactor/error",
		}},
	}
	for _, c := range cases {
		got := eventTagsSeenBy(t, l, c.user)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s was shown\n  %v\nwant\n  %v", c.user, got, c.want)
		}
	}
}
