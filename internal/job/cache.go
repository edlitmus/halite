package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Cache is the job cache of SPEC 9.4: a local store under the state
// directory, segmented by day, holding each job and the returns filed
// against it.
//
// A directory of files rather than a database, for the same reason the
// key store is: the thing an operator does when something has gone
// wrong is read it.
//
// Retention is by age and by total size, whichever binds first, and the
// hub enforces it rather than an external cron job. Salt's local_cache
// growing without bound is a common cause of a full disk on a master, // lexicon:allow
// and this will not do that.
type Cache struct {
	dir string
	// Retention is the maximum age of a job's records. Zero keeps them
	// for ever, which is a choice an operator can make and not a
	// default.
	Retention time.Duration
	// MaxBytes is the ceiling on the whole store.
	MaxBytes int64
	Now      func() time.Time
}

// ErrNoJob is returned for a jid the hub has no record of.
var ErrNoJob = errors.New("no such job")

// OpenCache prepares the store.
func OpenCache(dir string) (*Cache, error) {
	if dir == "" {
		return nil, fmt.Errorf("the job cache needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the job cache: %w", err)
	}
	return &Cache{dir: dir}, nil
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Dir is where the cache lives, for a message that names it.
func (c *Cache) Dir() string { return c.dir }

// jobDir is the segment and job directory for a jid, refusing anything
// that is not a jid: this becomes a path.
func (c *Cache) jobDir(id ID) (string, error) {
	if !id.Valid() {
		return "", fmt.Errorf("%q is not a job identifier", id)
	}
	return filepath.Join(c.dir, id.Day(), string(id)), nil
}

// Put writes a job's record. SPEC 9.1 step 4: this happens before
// delivery, with the resolved node set, so that a missing return is
// detectable rather than invisible.
func (c *Cache) Put(j *Job) error {
	dir, err := c.jobDir(j.JID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "returns"), 0o700); err != nil {
		return fmt.Errorf("creating the record for %s: %w", j.JID, err)
	}
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the record for %s: %w", j.JID, err)
	}
	return writeAtomic(filepath.Join(dir, "job.json"), append(raw, '\n'), 0o600)
}

// Get reads a job's record.
func (c *Cache) Get(id ID) (*Job, error) {
	dir, err := c.jobDir(id)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", id, ErrNoJob)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the record for %s: %w", id, err)
	}
	var j Job
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("the record for %s is not readable: %w", id, err)
	}
	return &j, nil
}

// AddReturn files one node's answer.
//
// It is idempotent by (jid, node, chunk), per SPEC 6.2: a node that
// retries because it lost the acknowledgement must not produce two
// returns, and a hub that counted them twice would report a job
// complete that is not.
func (c *Cache) AddReturn(r *Return) (bool, error) {
	dir, err := c.jobDir(r.JID)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(filepath.Join(dir, "job.json")); err != nil {
		return false, fmt.Errorf("%s: %w", r.JID, ErrNoJob)
	}
	name := returnFile(r.NodeID, r.Chunk)
	if name == "" {
		return false, fmt.Errorf("%q is not a node identity", r.NodeID)
	}
	path := filepath.Join(dir, "returns", name)
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return false, fmt.Errorf("encoding the return from %s: %w", r.NodeID, err)
	}
	if err := writeAtomic(path, append(raw, '\n'), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// returnFile names a return's file, and refuses a node identity that
// would escape the directory.
func returnFile(nodeID string, chunk int) string {
	if nodeID == "" || strings.ContainsAny(nodeID, "/\\") || strings.Contains(nodeID, "..") {
		return ""
	}
	if chunk == 0 {
		return nodeID + ".json"
	}
	return fmt.Sprintf("%s.%d.json", nodeID, chunk)
}

// Returns reads every answer filed against a job, in node order.
func (c *Cache) Returns(id ID) ([]*Return, error) {
	dir, err := c.jobDir(id)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "returns"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the returns for %s: %w", id, err)
	}
	var out []*Return
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "returns", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading the returns for %s: %w", id, err)
		}
		var r Return
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("the return at %s is not readable: %w", e.Name(), err)
		}
		out = append(out, &r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID == out[j].NodeID {
			return out[i].Chunk < out[j].Chunk
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out, nil
}

// Missing lists the nodes a job was dispatched to that have not
// returned. It is the question `halite-hub jobs missing` asks, and the
// reason the node set is recorded before delivery.
func (c *Cache) Missing(id ID) ([]string, error) {
	j, err := c.Get(id)
	if err != nil {
		return nil, err
	}
	returns, err := c.Returns(id)
	if err != nil {
		return nil, err
	}
	answered := make(map[string]bool, len(returns))
	for _, r := range returns {
		answered[r.NodeID] = true
	}
	var missing []string
	for _, node := range j.Nodes {
		if !answered[node] {
			missing = append(missing, node)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// List returns the most recent jobs, newest first.
func (c *Cache) List(limit int) ([]*Job, error) {
	days, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, fmt.Errorf("reading the job cache at %s: %w", c.dir, err)
	}
	var names []string
	for _, d := range days {
		if d.IsDir() {
			names = append(names, d.Name())
		}
	}
	// Newest day first, and newest job within it: the answer to "what
	// just happened" should not need paging.
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	var out []*Job
	for _, day := range names {
		jobs, err := os.ReadDir(filepath.Join(c.dir, day))
		if err != nil {
			return nil, err
		}
		var ids []string
		for _, e := range jobs {
			if e.IsDir() {
				ids = append(ids, e.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(ids)))
		for _, id := range ids {
			j, err := c.Get(ID(id))
			if errors.Is(err, ErrNoJob) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, j)
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

// Prune enforces retention: by age first, because an old record is the
// one an operator is least likely to want, and then by total size until
// the store is under its ceiling.
func (c *Cache) Prune() (int, error) {
	removed := 0
	now := c.now()

	type segment struct {
		path  string
		id    ID
		at    time.Time
		bytes int64
	}
	var segments []segment
	var total int64

	days, err := os.ReadDir(c.dir)
	if err != nil {
		return 0, fmt.Errorf("reading the job cache at %s: %w", c.dir, err)
	}
	for _, day := range days {
		if !day.IsDir() {
			continue
		}
		jobs, err := os.ReadDir(filepath.Join(c.dir, day.Name()))
		if err != nil {
			return removed, err
		}
		for _, e := range jobs {
			if !e.IsDir() {
				continue
			}
			id := ID(e.Name())
			at, err := id.Time()
			if err != nil {
				continue
			}
			path := filepath.Join(c.dir, day.Name(), e.Name())
			size, err := dirSize(path)
			if err != nil {
				return removed, err
			}
			if c.Retention > 0 && now.Sub(at) > c.Retention {
				if err := os.RemoveAll(path); err != nil {
					return removed, err
				}
				removed++
				continue
			}
			segments = append(segments, segment{path: path, id: id, at: at, bytes: size})
			total += size
		}
	}

	if c.MaxBytes > 0 && total > c.MaxBytes {
		sort.Slice(segments, func(i, j int) bool { return segments[i].at.Before(segments[j].at) })
		for _, s := range segments {
			if total <= c.MaxBytes {
				break
			}
			if err := os.RemoveAll(s.path); err != nil {
				return removed, err
			}
			total -= s.bytes
			removed++
		}
	}

	// An empty day directory left behind reads as a day with jobs in
	// it until you look.
	for _, day := range days {
		if !day.IsDir() {
			continue
		}
		path := filepath.Join(c.dir, day.Name())
		if entries, err := os.ReadDir(path); err == nil && len(entries) == 0 {
			os.Remove(path)
		}
	}
	return removed, nil
}

func dirSize(path string) (int64, error) {
	var total int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// writeAtomic writes through a temporary file in the same directory.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return os.Rename(name, path)
}
