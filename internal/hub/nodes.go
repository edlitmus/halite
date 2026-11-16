package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/value"
)

// NodeData is what the hub remembers about a node between connections:
// the grains it reported and when.
//
// Kept on disk so that targeting works immediately after a hub restart
// rather than only for nodes that have reconnected since. SPEC 8.3
// calls this the node data cache and requires that stale entries be
// annotated rather than hidden -- a job that silently misses half the
// fleet because the hub had forgotten it is worse than one that reports
// the doubt.
type NodeData struct {
	NodeID string `json:"node_id"`
	// Grains is the raw JSON the node sent, kept as it arrived so that
	// a 64-bit integer grain is not rounded through float64 on the way
	// to disk. SPEC 6.4.
	Grains   json.RawMessage `json:"grains,omitempty"`
	Version  string          `json:"version,omitempty"`
	LastSeen time.Time       `json:"last_seen"`
}

// errNoNodeCache marks a hub running without one. Targeting on a node
// ID still works; targeting on a grain matches nothing, which is the
// honest answer when nothing has been cached.
var errNoNodeCache = errors.New("this hub has no node data cache")

// ErrUnknownNode is what Get reports for a node the cache holds nothing
// about.
//
// Absence used to be `(nil, nil)`: no data, no error. Every caller
// checks the error, and three of the four then dereferenced the nil --
// so `halite-hub runner pillar.show_pillar node=x` panicked the hub for
// any node that had never pushed its grains, and so did `cachedNode`,
// and so did the fleet version survey for any accepted node in the same
// state. A nil pointer behind a nil error is a trap that has to be
// remembered at every call site, and this package forgot it three times
// out of four.
//
// Reporting absence as an error is the fix that cannot be forgotten:
// the callers that were wrong already check the error and are now
// right without being touched, and the two that genuinely want "no data
// is fine" say so with errors.Is.
var ErrUnknownNode = errors.New("this hub holds no data for the node")

// NodeCache is the store of node data.
type NodeCache struct {
	dir string
	// StaleAfter is grain_stale_after: how old cached grains may be
	// before a match on them is annotated.
	StaleAfter time.Duration
	Now        func() time.Time

	// memo is what Matchable has read, so that a dispatch does not read
	// and decode every node's file again. See NodeView.
	memo nodeMemo
	// reads counts node files Matchable has read, for the tests that
	// show it does not read them again.
	reads atomic.Int64
	// margin is how long after a file's mtime it must have been read
	// for what was read to be trusted again. nodeMemoMargin, except in
	// the tests that change it to show what it is for.
	margin time.Duration
}

// nodeMemoMargin is how far behind the clock a file's mtime must be for
// what was read from it to be used again, and the directory's mtime for
// a whole dispatch to skip looking at the files. A timestamp is coarse:
// a few milliseconds on Linux, whole seconds on some filesystems. A
// write in the same tick as the mtime a read saw leaves that mtime as it
// was, and trusting the read would hide the write. The key store has
// the same margin for the same reason (keystore.listCacheMargin,
// DIVERGENCE 5.669).
const nodeMemoMargin = 2 * time.Second

// nodeMemo is what has been read from the node cache.
type nodeMemo struct {
	mu      sync.Mutex
	entries map[string]*nodeEntry
	// dirMtime and dirOK: when the directory's mtime is still dirMtime
	// and dirOK is set, every entry was checked against its file since
	// the directory last changed, and none needs looking at again.
	dirMtime time.Time
	dirOK    bool
	// gen counts the times a dispatch made dirOK true. An entry from a
	// view that began before the last of them was read against an
	// older directory, and is not stored. See NodeView.Close.
	gen uint64
}

// nodeEntry is one node's file as it was read.
type nodeEntry struct {
	mtime time.Time
	size  int64
	// solid is that the file was read nodeMemoMargin or more after its
	// mtime, so a later write would carry a later one. An entry that is
	// not solid is read again rather than trusted.
	solid    bool
	grains   *value.Map
	lastSeen time.Time
	// err is that the file would not decode: an unreadable entry, which
	// resolve skips and reports. It is kept so as not to decode it again,
	// and is never mistaken for good data.
	err error
}

// OpenNodeCache prepares the store.
func OpenNodeCache(dir string) (*NodeCache, error) {
	if dir == "" {
		return nil, fmt.Errorf("the node cache needs a directory")
	}
	if err := fileperm.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the node cache: %w", err)
	}
	// MkdirAll is satisfied by a directory that already exists, whoever
	// owns it. A cache left behind by a hand-run as root is then opened
	// without complaint by a hub running as its service account, which
	// can read nothing in it — and the symptom is every target matching
	// no node, because a node whose cached data cannot be read is
	// skipped. Checked here so it is one message at startup rather than
	// a puzzle at the first job.
	probe := filepath.Join(dir, ".writable")
	if err := fileperm.WriteFile(probe, nil, 0o600); err != nil {
		return nil, fmt.Errorf("the node cache at %s is not usable by this process: %w", dir, err)
	}
	if err := os.Remove(probe); err != nil {
		return nil, fmt.Errorf("the node cache at %s is not usable by this process: %w", dir, err)
	}
	return &NodeCache{dir: dir}, nil
}

func (c *NodeCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *NodeCache) path(nodeID string) (string, error) {
	if c.dir == "" {
		// An empty directory would make filepath.Join produce a
		// relative path and read the working directory, which is how a
		// cache miss turns into reading whatever happens to be there.
		return "", errNoNodeCache
	}
	if err := pki.ValidateNodeID(nodeID); err != nil {
		return "", err
	}
	return filepath.Join(c.dir, nodeID+".json"), nil
}

// Put records what a node reported when it connected.
func (c *NodeCache) Put(data *NodeData) error {
	path, err := c.path(data.NodeID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the node data for %s: %w", data.NodeID, err)
	}
	return writeAtomic(path, append(raw, '\n'), 0o600)
}

// Get reads one node's data.
func (c *NodeCache) Get(nodeID string) (*NodeData, error) {
	path, err := c.path(nodeID)
	if err != nil {
		return nil, err
	}
	raw, err := atomicfile.Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w; it has to have connected and pushed its grains first",
			nodeID, ErrUnknownNode)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the node data for %s: %w", nodeID, err)
	}
	var data NodeData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, &nodeDecodeError{fmt.Errorf("the node data for %s is not readable: %w", nodeID, err)}
	}
	return &data, nil
}

// nodeDecodeError is a node's file that was read and would not decode,
// as against one that could not be read. NodeView keeps the first and
// not the second.
type nodeDecodeError struct{ err error }

func (e *nodeDecodeError) Error() string { return e.err.Error() }
func (e *nodeDecodeError) Unwrap() error { return e.err }

// Delete forgets a node, for one that has been removed from the estate.
func (c *NodeCache) Delete(nodeID string) error {
	path, err := c.path(nodeID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("forgetting %s: %w", nodeID, err)
	}
	return nil
}

// Matchable builds the value a target expression is evaluated against.
//
// A node with no cached grains still matches on its ID, because a glob
// or a list target needs nothing else, and refusing to consider it
// would mean a freshly enrolled node silently missing from every job
// until it happened to connect.
//
// The Pillar it returns is always empty: the cache holds grains, and a
// node's pillar exists only when the hub compiles it. A caller that has
// to honour `-I` or `-J` installs a loader, as Server.resolve does
// through targetPillar; one that does not gets a pillar target that
// matches nothing and a `not I@...` that matches everything.
//
// It goes through a NodeView of its own. A caller that asks for many
// nodes at once, as resolve does for every dispatch, should open one
// view for all of them, which is what saves the reading.
func (c *NodeCache) Matchable(nodeID string) (target.Node, error) {
	return c.View().Matchable(nodeID)
}

// NodeView is one look at the node cache, for one dispatch.
//
// resolve evaluates a target against every targetable node, and every
// node's file used to be read and decoded for every job: a few thousand
// reads per dispatch, even for a target that names one node by its ID.
// A reaction that dispatched a job took 0.1 to 0.5 seconds on a hub of
// 8,000 nodes, nearly all of it here (DIVERGENCE 5.670).
//
// A view keeps what it reads in the cache's memo and uses it again:
//
//   - When the directory's mtime has not changed since a view checked
//     every entry (dirOK), a node's entry is used without looking at its
//     file at all. Every write to the cache changes the directory: Put
//     writes a new file beside the old one and renames it over, and
//     Delete removes it.
//   - When it has changed, each node's file is looked at (a stat), and
//     read and decoded again only when its mtime or size is not the one
//     the entry was read at. A busy estate, whose nodes connect and push
//     grains all the time, changes the directory constantly; this is
//     what it pays.
//
// An entry is trusted only when its file was read nodeMemoMargin after
// its mtime (solid), and the directory skipped only when it was looked
// at that long after its mtime, so that a write in the same timestamp
// tick is not hidden. A file that would not decode is kept as such and
// skipped by resolve exactly as before; one that could not be read at
// all is not kept, and is tried again next time.
type NodeView struct {
	c        *NodeCache
	fast     bool
	ok       bool
	dirMtime time.Time
	started  time.Time
	gen      uint64
	seen     map[string]bool
}

// View opens a view. Close it when the dispatch has looked at every
// node, so that the next can skip the files.
func (c *NodeCache) View() *NodeView {
	v := &NodeView{c: c, started: time.Now(), seen: map[string]bool{}}
	if c.dir == "" {
		return v
	}
	info, err := os.Stat(c.dir)
	if err != nil {
		return v
	}
	v.ok, v.dirMtime = true, info.ModTime()
	c.memo.mu.Lock()
	v.gen = c.memo.gen
	v.fast = c.memo.dirOK && c.memo.dirMtime.Equal(v.dirMtime)
	c.memo.mu.Unlock()
	return v
}

func (c *NodeCache) memoMargin() time.Duration {
	if c.margin != 0 {
		return c.margin
	}
	return nodeMemoMargin
}

// Matchable is NodeCache.Matchable, through this view.
func (v *NodeView) Matchable(nodeID string) (target.Node, error) {
	c := v.c
	n := target.Node{ID: nodeID, Grains: value.NewMap(0), Pillar: value.NewMap(0)}
	path, err := c.path(nodeID)
	// A node the hub holds nothing about still matches on its own ID,
	// and so does every node when the hub has no cache at all. Both are
	// "no grains to match on" rather than a failure: SPEC 8.2's ID
	// matching does not depend on a node having connected.
	if errors.Is(err, errNoNodeCache) {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	v.seen[nodeID] = true

	c.memo.mu.Lock()
	e := c.memo.entries[nodeID]
	c.memo.mu.Unlock()
	if v.fast && e != nil && e.solid {
		return v.from(n, nodeID, e)
	}
	info, statErr := os.Stat(path)
	if errors.Is(statErr, fs.ErrNotExist) {
		c.memo.mu.Lock()
		delete(c.memo.entries, nodeID)
		c.memo.mu.Unlock()
		return n, nil
	}
	if statErr == nil && e != nil && e.solid && e.mtime.Equal(info.ModTime()) && e.size == info.Size() {
		return v.from(n, nodeID, e)
	}

	readAt := time.Now()
	c.reads.Add(1)
	data, err := c.Get(nodeID)
	if errors.Is(err, ErrUnknownNode) {
		return n, nil
	}
	var decodeErr *nodeDecodeError
	if err != nil && !errors.As(err, &decodeErr) {
		// Not read at all: permissions, say. Not kept, so that putting
		// it right is seen at once rather than at the next write.
		return n, err
	}
	fresh := &nodeEntry{err: err}
	if statErr == nil {
		fresh.mtime, fresh.size = info.ModTime(), info.Size()
		fresh.solid = readAt.Sub(info.ModTime()) >= c.memoMargin()
	}
	if err == nil {
		fresh.lastSeen = data.LastSeen
		fresh.grains = value.NewMap(0)
		if len(data.Grains) > 0 {
			decoded, derr := value.DecodeJSON(data.Grains)
			if derr != nil {
				fresh.err = fmt.Errorf("the cached grains for %s are not readable: %w", nodeID, derr)
			} else if m, ok := decoded.(*value.Map); ok {
				fresh.grains = m
			}
		}
	}
	if statErr == nil {
		v.store(nodeID, fresh)
	}
	return v.from(n, nodeID, fresh)
}

// store keeps an entry, unless the memo already holds one read at a
// later mtime, or another view has made the directory trusted since
// this one began: either way this entry may describe a file that has
// since been replaced, and in a fast view it would not be looked at
// again.
func (v *NodeView) store(nodeID string, e *nodeEntry) {
	m := &v.c.memo
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gen != v.gen {
		return
	}
	if old := m.entries[nodeID]; old != nil && old.mtime.After(e.mtime) {
		return
	}
	if m.entries == nil {
		m.entries = map[string]*nodeEntry{}
	}
	m.entries[nodeID] = e
}

// from answers with an entry. The grains are shared between views and
// dispatches; nothing that matches a target writes to them.
func (v *NodeView) from(n target.Node, nodeID string, e *nodeEntry) (target.Node, error) {
	if e.err != nil {
		return n, e.err
	}
	n.Grains = e.grains
	c := v.c
	if c.StaleAfter > 0 && !e.lastSeen.IsZero() && c.now().Sub(e.lastSeen) > c.StaleAfter {
		n.GrainsStale = true
	}
	return n, nil
}

// Close ends a view that looked at every node a dispatch could match.
// When the directory had changed, every entry this view did not look at
// is dropped, since nothing checked it against its file, and the
// directory is marked trusted at the mtime this view saw -- if the view
// began nodeMemoMargin after it, and no other view did the same in the
// meantime.
func (v *NodeView) Close() {
	if !v.ok || v.fast {
		return
	}
	m := &v.c.memo
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gen != v.gen {
		return
	}
	for id := range m.entries {
		if !v.seen[id] {
			delete(m.entries, id)
		}
	}
	m.dirMtime = v.dirMtime
	m.dirOK = v.started.Sub(v.dirMtime) >= v.c.memoMargin()
	m.gen++
}

// Known lists every node the hub has data for, in ID order.
func (c *NodeCache) Known() ([]string, error) {
	if c.dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, fmt.Errorf("reading the node cache at %s: %w", c.dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		out = append(out, e.Name()[:len(e.Name())-len(".json")])
	}
	sort.Strings(out)
	return out, nil
}

// writeAtomic writes through a temporary file in the same directory.
//
// The idiom is one package rather than six copies of it, because all six
// were wrong on Windows in the same way: see internal/atomicfile.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}

// NodeData is what the hub recorded about one node.
//
// Exported for a relay, which reports its subordinates' grains upstream
// so that targeting works on a relayed node exactly as on a directly
// connected one.
func (s *Server) NodeData(nodeID string) (*NodeData, error) {
	return s.nodes().Get(nodeID)
}
