package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/job"
)

// OrchRun is one orchestration, as SPEC 19.1 requires it to be kept: a
// first-class object with a jid, a step-by-step timeline, and per-step
// per-node results.
//
// Salt keeps an orchestration only as the return of the runner that ran
// it, so "which step failed, on which node, and what had already
// happened" is a question its own output cannot answer once the
// terminal is closed. This is why `orch resume` is possible here and
// not there.
type OrchRun struct {
	JID       job.ID   `json:"jid"`
	SLS       []string `json:"sls,omitempty"`
	Env       string   `json:"env,omitempty"`
	Principal string   `json:"principal,omitempty"`
	// ResumedFrom names the step a resumed run picked up at, and the
	// run it picked up from.
	ResumedFrom string `json:"resumed_from,omitempty"`
	ResumedOf   job.ID `json:"resumed_of,omitempty"`

	Started    time.Time   `json:"started"`
	DurationMS int64       `json:"duration_ms"`
	State      string      `json:"state"`
	Steps      []*OrchStep `json:"steps,omitempty"`
}

// The states an orchestration reports.
const (
	OrchRunning  = "running"
	OrchComplete = "complete"
	OrchFailed   = "failed"
)

// OrchStep is one step's outcome.
type OrchStep struct {
	// ID is the step as written in the SLS, which is what
	// `orch resume --from` names.
	ID  string `json:"id"`
	Fun string `json:"fun"`
	SLS string `json:"sls,omitempty"`
	// Order is the position in the run, which is the compiler's
	// ordering rather than the file's.
	Order int `json:"order"`

	Result  *bool           `json:"result"`
	Comment string          `json:"comment,omitempty"`
	Skipped bool            `json:"skipped,omitempty"`
	Changes json.RawMessage `json:"changes,omitempty"`

	Started    time.Time `json:"started"`
	DurationMS int64     `json:"duration_ms"`

	// JID and Nodes name the fleet job this step dispatched, for a step
	// that dispatched one.
	JID   string   `json:"job_jid,omitempty"`
	Nodes []string `json:"nodes,omitempty"`
	// Returns is the per-node result of that job, already encoded.
	Returns map[string]json.RawMessage `json:"returns,omitempty"`
	// Failed names the nodes that returned a failure, so a timeline can
	// be read without decoding every return.
	Failed []string `json:"failed,omitempty"`
	// Missing names the nodes that never answered, which is a different
	// thing from a failure and calls for a different response.
	Missing []string `json:"missing,omitempty"`
}

// Succeeded reports the step's own verdict.
func (s *OrchStep) Succeeded() bool { return s.Result == nil || *s.Result }

// OrchStore keeps the runs on disk.
//
// A separate store rather than the job cache: a job has one function
// and a set of respondents, and an orchestration has a sequence of
// steps each with its own. Writing one as the other loses the sequence,
// which is the part that makes resuming possible.
type OrchStore struct {
	dir string
	Now func() time.Time
}

// ErrNoOrchRun is returned for a jid this hub has no record of.
var ErrNoOrchRun = errors.New("no such orchestration")

var errNoOrchStore = errors.New("this hub keeps no orchestration records")

// OpenOrchStore prepares the store.
func OpenOrchStore(dir string) (*OrchStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("the orchestration store needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the orchestration store: %w", err)
	}
	return &OrchStore{dir: dir}, nil
}

// Dir is where the records are.
func (c *OrchStore) Dir() string { return c.dir }

// path refuses a jid that is not one, so that a record name cannot
// become a path.
func (c *OrchStore) path(id job.ID) (string, error) {
	if c == nil || c.dir == "" {
		return "", errNoOrchStore
	}
	if !id.Valid() {
		return "", fmt.Errorf("%q is not a job identifier", string(id))
	}
	return filepath.Join(c.dir, string(id)+".json"), nil
}

// Put writes a run, replacing any earlier version of it.
func (c *OrchStore) Put(run *OrchRun) error {
	path, err := c.path(run.JID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encoding orchestration %s: %w", run.JID, err)
	}
	return writeAtomic(path, raw, 0o600)
}

// Get reads one run.
func (c *OrchStore) Get(id job.ID) (*OrchRun, error) {
	path, err := c.path(id)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", id, ErrNoOrchRun)
	}
	if err != nil {
		return nil, err
	}
	var run OrchRun
	if err := json.Unmarshal(raw, &run); err != nil {
		return nil, fmt.Errorf("the record for %s is unreadable: %w", id, err)
	}
	return &run, nil
}

// List returns the most recent runs, newest first.
func (c *OrchStore) List(limit int) ([]*OrchRun, error) {
	if c == nil || c.dir == "" {
		return nil, errNoOrchStore
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	// The jid is a timestamp, so lexical order is chronological.
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]*OrchRun, 0, len(ids))
	for _, id := range ids {
		run, err := c.Get(job.ID(id))
		if err != nil {
			// One unreadable record must not hide the rest, and must
			// not be silently dropped either.
			continue
		}
		out = append(out, run)
	}
	return out, nil
}
