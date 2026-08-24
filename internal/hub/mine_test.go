package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// withMine gives the lab somewhere to keep published data, as `serve`
// does.
func (l *lab) withMine(t *testing.T) *lab {
	t.Helper()
	store, err := OpenMineStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l.server.Mine = store
	return l
}

// publish sends one function's data as a node.
func publish(t *testing.T, client *transport.Client, name, data, allowTgt string) {
	t.Helper()
	err := client.PublishMine(context.Background(), transport.MineRequest{
		Functions: map[string]transport.MinePublished{
			name: {Data: json.RawMessage(data), AllowTgt: allowTgt},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The mine is how a load balancer's state learns its backend list: the
// backends publish, and the balancer reads.
func TestANodePublishesAndAnotherReads(t *testing.T) {
	l := newLab(t).withJobs(t).withMine(t)
	web1 := l.enrolled(t, "web1.example")
	web2 := l.enrolled(t, "web2.example")
	lb := l.enrolled(t, "lb1.example")
	defer l.connect(t, web1, "web1.example", `{"os":"FreeBSD","role":"web"}`)()
	defer l.connect(t, web2, "web2.example", `{"os":"FreeBSD","role":"web"}`)()
	defer l.connect(t, lb, "lb1.example", `{"os":"FreeBSD","role":"lb"}`)()

	publish(t, web1, "backend", `{"address":"10.0.0.1","port":8080}`, "")
	publish(t, web2, "backend", `{"address":"10.0.0.2","port":8080}`, "")

	// The reader is a `node:` principal, and the policy has to say so.
	loaded, _, err := policy.Load([]byte(`
roles:
  backends-may-be-read:
    - target: 'web*'
      functions: ['backend']
bindings:
  - principal: 'node:lb1.example'
    roles: ['backends-may-be-read']
`), "mine-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded

	got, err := lb.FetchMine(context.Background(), transport.MineGetRequest{
		Target: "web*", Function: "backend",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 2 {
		t.Fatalf("the balancer saw %d backend(s): %v", len(got.Data), got.Data)
	}
	for _, node := range []string{"web1.example", "web2.example"} {
		if _, ok := got.Data[node]; !ok {
			t.Errorf("%s is missing from %v", node, got.Data)
		}
	}

	// A node publishes its own data and nobody else's: the identity is
	// the certificate's, which is what makes the answer worth
	// believing.
	stored, err := l.server.Mine.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored.Functions["backend"]; !ok {
		t.Errorf("web1 published %v", stored.Functions)
	}
}

// The peer interface is deny-by-default, expressed in the one RBAC
// policy rather than in a second configuration dialect as Salt does.
func TestReadingTheMineIsDeniedByDefault(t *testing.T) {
	l := newLab(t).withJobs(t).withMine(t)
	web1 := l.enrolled(t, "web1.example")
	other := l.enrolled(t, "other.example")
	defer l.connect(t, web1, "web1.example", `{"os":"FreeBSD"}`)()
	defer l.connect(t, other, "other.example", `{"os":"FreeBSD"}`)()
	publish(t, web1, "backend", `{"address":"10.0.0.1"}`, "")

	// No binding for `node:other.example`.
	l.server.Policy = labPolicy(t)
	_, err := other.FetchMine(context.Background(), transport.MineGetRequest{
		Target: "*", Function: "backend",
	})
	if err == nil {
		t.Fatal("an unbound node read the mine")
	}
	if !strings.Contains(err.Error(), "node:other.example") {
		t.Errorf("the refusal should name the principal: %v", err)
	}
}

// A grant names the functions and the targets, and nothing wider. A
// node permitted to read `backend` from `web*` may not read it from
// everything, and may not read a different function at all.
func TestAMineGrantIsAsNarrowAsItIsWritten(t *testing.T) {
	l := newLab(t).withJobs(t).withMine(t)
	web1 := l.enrolled(t, "web1.example")
	db1 := l.enrolled(t, "db1.example")
	lb := l.enrolled(t, "lb1.example")
	defer l.connect(t, web1, "web1.example", `{"os":"FreeBSD"}`)()
	defer l.connect(t, db1, "db1.example", `{"os":"FreeBSD"}`)()
	defer l.connect(t, lb, "lb1.example", `{"os":"FreeBSD"}`)()

	publish(t, web1, "backend", `{"address":"10.0.0.1"}`, "")
	publish(t, db1, "credentials", `{"password":"hunter2"}`, "")

	loaded, _, err := policy.Load([]byte(`
roles:
  reader:
    - target: 'web*'
      functions: ['backend']
bindings:
  - principal: 'node:lb1.example'
    roles: ['reader']
`), "mine-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded

	// The wrong function, against a target the grant covers.
	if _, err := lb.FetchMine(context.Background(), transport.MineGetRequest{
		Target: "web*", Function: "credentials",
	}); err == nil {
		t.Error("a grant of `backend` read `credentials`")
	}
	// The right function, against a wider target than the grant.
	if _, err := lb.FetchMine(context.Background(), transport.MineGetRequest{
		Target: "*", Function: "backend",
	}); err == nil {
		t.Error("a grant against `web*` read against `*`")
	}
}

// `allow_tgt` is the publisher's own restriction, and it is a second
// gate rather than the only one: a node publishing something sensitive
// decides who may see it without trusting every reader's policy.
func TestAllowTgtRestrictsWhoMayRead(t *testing.T) {
	l := newLab(t).withJobs(t).withMine(t)
	db1 := l.enrolled(t, "db1.example")
	app := l.enrolled(t, "app1.example")
	other := l.enrolled(t, "other.example")
	defer l.connect(t, db1, "db1.example", `{"os":"FreeBSD"}`)()
	defer l.connect(t, app, "app1.example", `{"os":"FreeBSD"}`)()
	defer l.connect(t, other, "other.example", `{"os":"FreeBSD"}`)()

	publish(t, db1, "dsn", `{"host":"db1"}`, "app*")

	// Both readers are permitted by the policy; only one is permitted
	// by the publisher.
	loaded, _, err := policy.Load([]byte(`
roles:
  reader:
    - target: '*'
      functions: ['dsn']
bindings:
  - principal: 'node:app1.example'
    roles: ['reader']
  - principal: 'node:other.example'
    roles: ['reader']
`), "mine-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded

	got, err := app.FetchMine(context.Background(), transport.MineGetRequest{
		Target: "*", Function: "dsn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 1 {
		t.Errorf("the permitted reader saw %d entries", len(got.Data))
	}

	denied, err := other.FetchMine(context.Background(), transport.MineGetRequest{
		Target: "*", Function: "dsn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(denied.Data) != 0 {
		t.Errorf("a reader outside allow_tgt saw %v", denied.Data)
	}
}

// A full publication replaces, so a function taken out of
// `mine_functions` stops being served rather than lingering for ever.
func TestAFullPublicationReplaces(t *testing.T) {
	l := newLab(t).withJobs(t).withMine(t)
	web1 := l.enrolled(t, "web1.example")
	defer l.connect(t, web1, "web1.example", `{"os":"FreeBSD"}`)()

	publish(t, web1, "a", `1`, "")
	publish(t, web1, "b", `2`, "")
	stored, err := l.server.Mine.Get("web1.example")
	if err != nil || len(stored.Functions) != 2 {
		t.Fatalf("after two sends the node has %v (%v)", stored, err)
	}

	err = web1.PublishMine(context.Background(), transport.MineRequest{
		Functions: map[string]transport.MinePublished{"a": {Data: json.RawMessage(`3`)}},
		Replace:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err = l.server.Mine.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Functions) != 1 {
		t.Errorf("a replacing publication left %v", stored.Functions)
	}
	if _, gone := stored.Functions["b"]; gone {
		t.Error("a function no longer published is still being served")
	}
}

// The operator's side: `mine.get` reads across the fleet and
// `mine.valid` says what is there and how old it is.
func TestTheMineRunnersReadAndReport(t *testing.T) {
	l := newLab(t).withJobs(t).withMine(t)
	web1 := l.enrolled(t, "web1.example")
	defer l.connect(t, web1, "web1.example", `{"os":"FreeBSD"}`)()
	publish(t, web1, "backend", `{"address":"10.0.0.1"}`, "lb*")

	op := l.operator(t, "ed")
	got := mapOf(t, returned(t, call(t, op, "mine.get", "*", "backend")))
	entry, ok := got.Get("web1.example")
	if !ok {
		t.Fatalf("mine.get returned %v", got.StringKeys())
	}
	if m, ok := entry.(*value.Map); !ok {
		t.Errorf("the entry came back as %T", entry)
	} else if addr, _ := m.Get("address"); addr != "10.0.0.1" {
		t.Errorf("the entry is %v", m.StringKeys())
	}

	// An operator is not restricted by `allow_tgt`: it names which
	// nodes may read, and an operator is already through the policy.
	valid := mapOf(t, returned(t, call(t, op, "mine.valid")))
	node, ok := valid.Get("web1.example")
	if !ok {
		t.Fatalf("mine.valid returned %v", valid.StringKeys())
	}
	functions := mapOf(t, node)
	backend, ok := functions.Get("backend")
	if !ok {
		t.Fatalf("mine.valid lists %v", functions.StringKeys())
	}
	if allow, _ := mapOf(t, backend).Get("allow_tgt"); allow != "lb*" {
		t.Errorf("mine.valid does not report allow_tgt: %v", allow)
	}

	// And flushing takes it away.
	if res := call(t, op, "mine.flush", "web1.example"); !res.Success {
		t.Fatalf("mine.flush: %s", res.Error)
	}
	after := mapOf(t, returned(t, call(t, op, "mine.get", "*", "backend")))
	if after.Len() != 0 {
		t.Errorf("after a flush the mine holds %v", after.StringKeys())
	}
}

// A node ID that is not one must not become a path.
func TestAMineNodeIdCannotEscapeTheStore(t *testing.T) {
	store, err := OpenMineStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "..", ".", "../escape", `a\b`, "a/b"} {
		if err := store.Put(&MineData{NodeID: bad}); err == nil {
			t.Errorf("%q was accepted as a node identifier", bad)
		}
	}
}

// Every entry carries its own timestamp. An entry whose age reads as
// the year zero is a stale answer nobody can spot, and a replacing
// publication arrives with none set.
func TestEveryMineEntryIsTimestamped(t *testing.T) {
	store, err := OpenMineStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = store.Put(&MineData{
		NodeID: "web1.example",
		Functions: map[string]*MineEntry{
			"a": {Data: json.RawMessage(`1`)},
			"b": {Data: json.RawMessage(`2`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("web1.example")
	if err != nil {
		t.Fatal(err)
	}
	for name, entry := range got.Functions {
		if entry.Updated.IsZero() {
			t.Errorf("%s was stored with no timestamp", name)
		}
	}
}
