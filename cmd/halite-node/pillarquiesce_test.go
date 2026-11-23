package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/schedule"
	"github.com/edlitmus/halite/internal/value"
)

// hubPillarStub is the hub's answer to a pillar fetch, changeable between
// reads, and counted, so a test can say whether the node asked again.
type hubPillarStub struct {
	mu     sync.Mutex
	pillar *value.Map
	err    error
	reads  int
}

func (h *hubPillarStub) set(p *value.Map, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pillar, h.err = p, err
}

func (h *hubPillarStub) fetch(string, bool) (*value.Map, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reads++
	if h.err != nil {
		return nil, h.err
	}
	return h.pillar, nil
}

// pillarQuiescedNode is an agent as runConnect builds one: a record of
// the pillar's quiesce loaded from state_dir, and a hub that serves the
// pillar the test says.
func pillarQuiescedNode(t *testing.T, config string, pillar *value.Map) (*node, *hubPillarStub, *bytes.Buffer) {
	t.Helper()
	n := quiescedNode(t, config)
	var logged bytes.Buffer
	n.log = testLogger(t, &logged)
	hub := &hubPillarStub{pillar: pillar}
	n.hubPillar = hub.fetch
	n.startPillarQuiesce()
	return n, hub, &logged
}

func quiescingPillar(flag, list string, allowed ...any) *value.Map {
	p := value.MapOf(flag, true)
	if allowed != nil {
		p.Set(list, allowed)
	}
	return p
}

func readPillar(t *testing.T, n *node) {
	t.Helper()
	if _, err := n.compilePillarOrErr(); err != nil {
		t.Fatalf("the pillar did not compile: %v", err)
	}
}

var jidCounter struct {
	sync.Mutex
	n int
}

func nextJID() string {
	jidCounter.Lock()
	defer jidCounter.Unlock()
	jidCounter.n++
	return fmt.Sprintf("20261122T1%011d", jidCounter.n)
}

// Issue 780, option B: a pillar quiesces the node, by halite's key or by
// Salt's, and its allowlist is matched exactly as the configuration's is.
func TestAPillarQuiescesTheNodeByEitherName(t *testing.T) {
	for _, keys := range [][2]string{
		{"quiesce", "quiesce_allowlist"},
		{"minion_blackout", "minion_blackout_whitelist"}, // lexicon:allow
	} {
		t.Run(keys[0], func(t *testing.T) {
			n, _, _ := pillarQuiescedNode(t, "", quiescingPillar(keys[0], keys[1], "test.ping"))
			readPillar(t, n)

			refusedQueued, refusal := offer(t, n, nextJIDFor(t), "state.apply")
			pingQueued, _ := offer(t, n, nextJIDFor(t), "test.ping")

			if refusedQueued {
				t.Fatal("a pillar-quiesced node queued state.apply")
			}
			if refusal == nil || !strings.Contains(string(refusal.Return), job.ReasonQuiesced) ||
				!strings.Contains(string(refusal.Return), "its pillar's "+keys[0]) {
				t.Errorf("the refusal does not say the pillar quiesced the node: %v", refusal)
			}
			if !pingQueued {
				t.Error("the pillar's allowlist did not let test.ping through")
			}
		})
	}
}

// The heart of option B: a pillar can never lift a quiesce the node's
// own configuration sets, whether it says quiesce is off or offers a
// wider allowlist.
func TestAPillarCannotLiftALocalQuiesce(t *testing.T) {
	cases := map[string]*value.Map{
		"the pillar says quiesce is off":          value.MapOf("quiesce", false),
		"the pillar allowlists state.apply":       quiescingPillar("quiesce", "quiesce_allowlist", "test.ping", "state.apply"),
		"Salt's key allowlists state.apply":       quiescingPillar("minion_blackout", "minion_blackout_whitelist", "state.apply"), // lexicon:allow
		"the pillar does not mention quiesce":     value.NewMap(0),
		"the pillar sets an allowlist on its own": value.MapOf("quiesce_allowlist", []any{"state.apply"}),
	}
	for name, pillar := range cases {
		t.Run(name, func(t *testing.T) {
			n, _, _ := pillarQuiescedNode(t, "quiesce: true\nquiesce_allowlist: [test.ping]\n", pillar)
			readPillar(t, n)

			queued, refusal := offer(t, n, nextJIDFor(t), "state.apply")

			if queued || refusal == nil {
				t.Fatalf("the pillar lifted a local quiesce: queued=%v", queued)
			}
		})
	}
}

// Both quiesce the node: the stricter list applies, which is the
// intersection, so neither can widen the other.
func TestTheAllowlistIsTheIntersectionWhenBothQuiesce(t *testing.T) {
	n, _, _ := pillarQuiescedNode(t, "quiesce: true\nquiesce_allowlist: [test.ping, grains.items]\n",
		quiescingPillar("quiesce", "quiesce_allowlist", "grains.items", "state.apply"))
	readPillar(t, n)

	for fun, want := range map[string]bool{"grains.items": true, "test.ping": false, "state.apply": false} {
		if queued, _ := offer(t, n, nextJIDFor(t), fun); queued != want {
			t.Errorf("%s: queued=%v, want %v", fun, queued, want)
		}
	}
}

// A pillar lifts its own quiesce, at the next job: the refused job makes
// the node read its pillar again, so nothing like Salt's always-allowed
// saltutil.refresh_pillar is needed, and the log says it was lifted.
func TestAPillarLiftsItsOwnQuiesceAtTheNextJob(t *testing.T) {
	n, hub, logged := pillarQuiescedNode(t, "", quiescingPillar("quiesce", "quiesce_allowlist"))
	readPillar(t, n)
	if queued, _ := offer(t, n, nextJIDFor(t), "state.apply"); queued {
		t.Fatal("the node was not quiesced to begin with")
	}

	hub.set(value.NewMap(0), nil)
	queued, refusal := offer(t, n, nextJIDFor(t), "state.apply")

	if !queued {
		t.Fatalf("the pillar lifted quiesce and the job was still refused: %v", refusal)
	}
	if !strings.Contains(logged.String(), "the pillar has quiesced this node") ||
		!strings.Contains(logged.String(), "the pillar no longer quiesces this node") {
		t.Errorf("the log does not say quiesce was set and lifted:\n%s", logged)
	}
	if _, err := os.Stat(pillarQuiescePath(n.cfg)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the record outlived the quiesce: %v", err)
	}
}

// A fetch that fails is not a pillar that said "off". The quiesce
// stands, and the refusal says why it could not be checked.
func TestAFailedPillarFetchDoesNotLiftAPillarQuiesce(t *testing.T) {
	n, hub, _ := pillarQuiescedNode(t, "", quiescingPillar("quiesce", "quiesce_allowlist", "test.ping"))
	readPillar(t, n)

	hub.set(nil, errors.New("dial tcp 127.0.0.1:4506: connection refused"))
	reads := hub.reads
	queued, refusal := offer(t, n, nextJIDFor(t), "state.apply")

	if queued {
		t.Fatal("a failed pillar fetch lifted the quiesce")
	}
	if hub.reads != reads+1 {
		t.Errorf("the node did not ask for its pillar again before refusing: %d reads, want %d", hub.reads, reads+1)
	}
	if refusal == nil || !strings.Contains(string(refusal.Return), "could not be read again") {
		t.Errorf("the refusal does not say the pillar could not be re-read: %v", refusal)
	}
}

// And a restart while the hub is unreachable does not lift it either:
// that is why the record is on disk.
func TestAPillarQuiesceSurvivesARestart(t *testing.T) {
	n, hub, _ := pillarQuiescedNode(t, "", quiescingPillar("quiesce", "quiesce_allowlist", "test.ping"))
	readPillar(t, n)

	hub.set(nil, errors.New("dial tcp 127.0.0.1:4506: connection refused"))
	var logged bytes.Buffer
	n.log = testLogger(t, &logged)
	n.startPillarQuiesce()
	queued, _ := offer(t, n, nextJIDFor(t), "state.apply")
	pingQueued, _ := offer(t, n, nextJIDFor(t), "test.ping")

	if queued {
		t.Fatal("a restart lifted the pillar's quiesce")
	}
	if !pingQueued {
		t.Error("the restored allowlist did not let test.ping through")
	}
	if !strings.Contains(logged.String(), "stays quiesced until a pillar says otherwise") {
		t.Errorf("startup does not say the pillar's quiesce was restored:\n%s", logged.String())
	}
}

// A record that cannot be read is taken as quiesced with nothing
// allowed: it exists only while a pillar quiesces the node.
func TestAnUnreadableRecordQuiesces(t *testing.T) {
	n := quiescedNode(t, "")
	if err := os.WriteFile(pillarQuiescePath(n.cfg), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub := &hubPillarStub{err: errors.New("connection refused")}
	n.hubPillar = hub.fetch
	n.startPillarQuiesce()

	if queued, _ := offer(t, n, nextJIDFor(t), "test.ping"); queued {
		t.Fatal("an unreadable record left the node running jobs")
	}
}

// The first job after a pillar quiesced the node was admitted against
// the pillar before. It must not run: it is most likely the highstate the
// quiesce was set to stop. It fails with the refusal once its own pillar
// says so.
func TestTheFirstJobAfterAPillarQuiesceDoesNotRun(t *testing.T) {
	for _, fun := range []string{"test.ping", "state.apply"} {
		t.Run(fun, func(t *testing.T) {
			n, _, _ := pillarQuiescedNode(t, "", quiescingPillar("quiesce", "quiesce_allowlist", "grains.items"))
			// A tree that would apply cleanly, so a failure is the quiesce.
			n.statesRunning = new(atomic.Int64)
			n.files = fileserver.NewFetcher(fileserver.NewRoots(map[string][]string{"base": {t.TempDir()}}))

			ret := n.executeJob(&job.Job{JID: job.ID(nextJIDFor(t)), Fun: fun})

			if ret.Success {
				t.Fatalf("%s ran on a node its pillar had just quiesced: %s", fun, ret.Return)
			}
			if !strings.Contains(string(ret.Return), job.ReasonQuiesced) {
				t.Errorf("the failure is not the quiesce: %s", ret.Return)
			}
		})
	}
}

// The schedule is refused by a pillar's quiesce as it is by the
// configuration's, as Salt's scheduler is by pillar.
func TestAPillarQuiesceRefusesTheSchedule(t *testing.T) {
	n, _, _ := pillarQuiescedNode(t, "", quiescingPillar("minion_blackout", "minion_blackout_whitelist")) // lexicon:allow
	readPillar(t, n)
	run := schedule.Run{Job: &schedule.Job{Name: "hs", Function: "state.apply"}, Fire: time.Now()}
	// Nothing drains this node's queue, so a job that is wrongly admitted
	// waits for ever; the deadline turns that into a failure.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := n.runScheduled(ctx, run)

	var refusal *job.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != job.ReasonQuiesced {
		t.Fatalf("the scheduled job was not refused as quiesced: %v", err)
	}
}

// Only the agent keeps a pillar's quiesce. Agentless mode does not check
// quiesce at all, and doctor and a one-shot command compile pillars of
// their own -- doctor's from the node's local roots -- which must never
// overwrite what the agent recorded from the hub's.
func TestOnlyTheAgentRecordsAPillarQuiesce(t *testing.T) {
	n := quiescedNode(t, "")
	hub := &hubPillarStub{pillar: quiescingPillar("quiesce", "quiesce_allowlist")}
	n.hubPillar = hub.fetch

	readPillar(t, n)
	ret := n.executeJob(&job.Job{JID: job.ID(nextJIDFor(t)), Fun: "test.ping"})

	if !ret.Success {
		t.Errorf("a node with no record refused a job by its pillar: %s", ret.Return)
	}
	if _, err := os.Stat(pillarQuiescePath(n.cfg)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a node that is not the agent wrote the record: %v", err)
	}
}

// The values a pillar can hold, read the way the configuration's are,
// failing closed.
func TestAPillarsQuiesceValuesAreReadAsTheConfigurationsAre(t *testing.T) {
	cases := []struct {
		name   string
		pillar *value.Map
		want   []quiesceSource
	}{
		{"absent", value.NewMap(0), nil},
		{"false", value.MapOf("quiesce", false), nil},
		{"the string false", value.MapOf("quiesce", "false"), nil},
		{"null", value.MapOf("quiesce", nil), nil},
		{"true with no list", value.MapOf("quiesce", true), []quiesceSource{{Name: "its pillar's quiesce"}}},
		{"an unreadable value quiesces", value.MapOf("quiesce", "maybe"), []quiesceSource{{Name: "its pillar's quiesce"}}},
		{"one name on its own", value.MapOf("quiesce", true, "quiesce_allowlist", "test.ping"),
			[]quiesceSource{{Name: "its pillar's quiesce", Allowlist: []string{"test.ping"}}}},
		{"a mapping is no list", value.MapOf("quiesce", true, "quiesce_allowlist", value.MapOf("a", 1)),
			[]quiesceSource{{Name: "its pillar's quiesce"}}},
		{"both names", value.MapOf("quiesce", true, "minion_blackout", true), // lexicon:allow
			[]quiesceSource{{Name: "its pillar's quiesce"}, {Name: "its pillar's minion_blackout"}}}, // lexicon:allow
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pillarQuiesceSources(tc.pillar); !sameSources(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func nextJIDFor(t *testing.T) string {
	t.Helper()
	id := nextJID()
	if !job.ID(id).Valid() {
		t.Fatalf("%q is not a job identifier", id)
	}
	return id
}

// doctor reads the agent's record and the configuration, and never the
// pillar it compiles itself, which on a node with a hub is not the one
// the agent runs with.
func TestDoctorSaysWhatQuiescesTheNodeFromTheAgentsRecord(t *testing.T) {
	agent, _, _ := pillarQuiescedNode(t, "quiesce: true\nquiesce_allowlist: [test.ping, grains.items]\n",
		quiescingPillar("quiesce", "quiesce_allowlist", "test.ping"))
	readPillar(t, agent)
	doctorNode := quiescedNode(t, "")
	doctorNode.cfg = agent.cfg

	res := nodeQuiesceCheck(doctorNode).Run(context.Background())

	want := "quiesced by its configuration and by its pillar's quiesce; runs only test.ping"
	if res.Status != "warn" || res.Detail != want {
		t.Errorf("doctor said %s %q, want warn %q", res.Status, res.Detail, want)
	}
	if doctorNode.pillarQuiesce != nil {
		t.Error("doctor attached the agent's record to its own node, where its pillar check could overwrite it")
	}
}
