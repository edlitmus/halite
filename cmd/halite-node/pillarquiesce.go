package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/value"
)

// A pillar may quiesce this node, and may never lift a quiesce its
// configuration sets (issue 780, option B; DIVERGENCE 5.703).
//
// The keys are halite's own, `quiesce` and `quiesce_allowlist`, and
// Salt's, `minion_blackout` and `minion_blackout_whitelist`, at the top // lexicon:allow
// of the pillar. Salt's are read because a pillar tree is the one thing
// an estate brings across unchanged -- the hub compiles it as Salt
// would, and `migrate` converts nothing in it -- and pillar is where
// Salt's own code reads blackout. Reading only halite's name would leave
// every node a Salt estate had blacked out back in automation after
// migration, which is the defect issue 780 is about. Each pair is a
// source of its own, so a pillar that sets both is quiesced by both and
// the stricter list applies.
//
// Grains are not read, though Salt reads them too. A grain here is
// either a fact the node measured, which no operator sets, or one set in
// `grains:` in the node's configuration, which is where `quiesce`
// already lives and is the setting `migrate` points at; or one written by
// `grains.setval` from the hub, which is a hub-driven quiesce by a second
// road that would need its own rules about lifting. One hub-driven road,
// pillar, is one thing to reason about.
var pillarQuiesceKeys = []struct{ flag, allowlist, name string }{
	{"quiesce", "quiesce_allowlist", "its pillar's quiesce"},
	{"minion_blackout", "minion_blackout_whitelist", "its pillar's minion_blackout"}, // lexicon:allow
}

// pillarQuiesceSources reads a compiled pillar.
func pillarQuiesceSources(p *value.Map) []quiesceSource {
	if p == nil {
		return nil
	}
	var sources []quiesceSource
	for _, k := range pillarQuiesceKeys {
		v, ok := p.Get(k.flag)
		if !ok || !quiesceValue(v) {
			continue
		}
		list, _ := p.Get(k.allowlist)
		sources = append(sources, quiesceSource{Name: k.name, Allowlist: allowlistValue(list)})
	}
	return sources
}

// allowlistValue reads a pillar list the way config.StringSlice reads
// the configuration's: a list of names, or one name on its own. Anything
// else is an empty list, which refuses everything -- the safe reading.
func allowlistValue(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
				continue
			}
			out = append(out, value.KeyString(item))
		}
		return out
	}
	return nil
}

// pillarQuiesceFile is where the agent keeps what the last pillar it
// read said about quiesce, under state_dir. It exists only while a
// pillar quiesces the node.
const pillarQuiesceFile = "pillar-quiesce.json"

func pillarQuiescePath(cfg *config.Config) string {
	return filepath.Join(cfg.String("state_dir", config.DefaultStateDir), pillarQuiesceFile)
}

// pillarQuiesce is what the last pillar this agent read said, shared by
// the agent and every copy of it a job runs on.
//
// It changes only when a pillar is read successfully. A fetch that
// fails leaves it as it was, so the hub going away, a pillar that does
// not compile, or a node restarted while the hub is down cannot lift a
// quiesce a pillar set: that is why it is on disk, and why it is kept
// even with `pillar_cache_disk: false`, which is about secrets and this
// holds none. The two paths that must agree are the pillar a job runs
// with and the quiesce it was admitted under; both are the last pillar
// read, because every successful read comes through note.
type pillarQuiesce struct {
	mu      sync.Mutex
	path    string
	sources []quiesceSource
}

type pillarQuiesceRecord struct {
	Sources []quiesceSource `json:"sources"`
	SavedAt time.Time       `json:"saved_at"`
}

// loadPillarQuiesce reads the file the agent left. A file that is there
// and cannot be read is taken as quiesced with an empty allowlist, and
// the error says so: the file exists only while a pillar quiesces the
// node, so its presence is the one fact that is certain, and the next
// pillar read replaces it either way.
func loadPillarQuiesce(path string) (*pillarQuiesce, *pillarQuiesceRecord, error) {
	q := &pillarQuiesce{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return q, nil, nil
	}
	var rec pillarQuiesceRecord
	if err == nil {
		err = json.Unmarshal(raw, &rec)
	}
	if err == nil && len(rec.Sources) == 0 {
		err = errors.New("it names no source")
	}
	if err != nil {
		q.sources = []quiesceSource{{Name: "its pillar, as last recorded in an unreadable " + path}}
		return q, &pillarQuiesceRecord{Sources: q.sources},
			fmt.Errorf("%s cannot be read (%v), so this node stays quiesced and runs nothing "+
				"until a pillar is read", path, err)
	}
	q.sources = rec.Sources
	return q, &rec, nil
}

// current is what the last pillar said. Nil-safe, because only the agent
// has one: a one-shot command and `doctor` do not record anything.
func (q *pillarQuiesce) current() []quiesceSource {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.sources)
}

// note records what a pillar just read says, and returns what it said
// before when that changed.
func (q *pillarQuiesce) note(p *value.Map) (before, after []quiesceSource, changed bool, err error) {
	if q == nil {
		return nil, nil, false, nil
	}
	after = pillarQuiesceSources(p)
	q.mu.Lock()
	defer q.mu.Unlock()
	before = q.sources
	if sameSources(before, after) {
		return before, after, false, nil
	}
	q.sources = after
	return before, after, true, q.persist()
}

// persist writes the file, or removes it once no pillar quiesces. Called
// with the lock held. A failure leaves the memory as the pillar said,
// which is right for this process; only a restart would lose it.
func (q *pillarQuiesce) persist() error {
	if len(q.sources) == 0 {
		if err := os.Remove(q.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, err := json.MarshalIndent(pillarQuiesceRecord{Sources: q.sources, SavedAt: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(q.path), 0o755); err != nil {
		return err
	}
	// 0644: whether a node is quiesced and what it still runs is not a
	// secret, and doctor reads it.
	return atomicfile.Write(q.path, append(raw, '\n'), 0o644)
}

func sameSources(a, b []quiesceSource) bool {
	return slices.EqualFunc(a, b, func(x, y quiesceSource) bool {
		return x.Name == y.Name && slices.Equal(x.Allowlist, y.Allowlist)
	})
}

// notePillarQuiesce is called with every pillar the agent reads
// successfully, and says so when the answer changes.
func (n *node) notePillarQuiesce(p *value.Map) {
	before, after, changed, err := n.pillarQuiesce.note(p)
	if err != nil {
		n.log.Warn("could not record what the pillar says about quiesce; "+
			"a restart before the next pillar read would forget it",
			"file", n.pillarQuiesce.path, "error", err.Error())
	}
	if !changed {
		return
	}
	switch {
	case len(after) > 0 && len(before) == 0:
		n.log.Warn("the pillar has quiesced this node: it refuses every job and scheduled job but its allowlist",
			"by", sourceNames(after), "quiesce_allowlist", strings.Join(effectiveAllowlist(n.quiesceSources()), ", "))
	case len(after) > 0:
		n.log.Warn("the pillar's quiesce has changed",
			"by", sourceNames(after), "quiesce_allowlist", strings.Join(effectiveAllowlist(n.quiesceSources()), ", "))
	default:
		if _, local := localQuiesce(n.cfg); local {
			n.log.Warn("the pillar no longer quiesces this node; its configuration still does",
				"was", sourceNames(before))
			return
		}
		n.log.Warn("the pillar no longer quiesces this node: it runs every job again", "was", sourceNames(before))
	}
}

// startPillarQuiesce gives the agent its record, and says at startup
// what it holds.
func (n *node) startPillarQuiesce() {
	q, rec, err := loadPillarQuiesce(pillarQuiescePath(n.cfg))
	n.pillarQuiesce = q
	if err != nil {
		n.log.Error("the record of the pillar's quiesce is unreadable", "error", err.Error())
		return
	}
	if rec != nil {
		n.log.Warn("this node's pillar quiesced it when it was last read, and it stays quiesced until a pillar says otherwise",
			"by", sourceNames(rec.Sources), "since", rec.SavedAt.Format(time.RFC3339),
			"quiesce_allowlist", strings.Join(effectiveAllowlist(n.quiesceSources()), ", "))
	}
}
