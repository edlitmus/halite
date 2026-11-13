package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/edlitmus/halite/internal/fileperm"
)

// Filing is what filing one return learned about the job it belongs to.
//
// It exists so that the hub's return handler can do a constant amount of
// work per return. It used to read the job's record three times and then
// ask Missing, which read and decoded **every return already filed**: the
// k-th return of a job read k files, a job to n nodes cost n²/2 reads,
// and one `test.ping` to 5,000 simulated nodes took a 16 GiB hub from
// 2.2 GB to 14.2 GB in a minute and accepted 1,508 of the returns.
// DIVERGENCE 5.634 has the measurements.
type Filing struct {
	// Fresh is false for a return this hub already had from that node:
	// a retry, which SPEC 6.2 says must not count twice.
	Fresh bool
	// Remaining is how many of the job's targets have not answered,
	// counting this return as answered. Zero is the moment to ask
	// whether the job is complete, and the only one: see completeIfDone
	// in the hub, which still asks Missing, so the count decides when
	// the question is asked and never what the answer is.
	Remaining int
	// Fun and Correlation are the job record's, so that the caller need
	// not read the record again to learn them.
	Fun         string
	Correlation string
}

// maxIndexedJobs is how many jobs' return indexes are kept before idle
// ones are dropped. An index is a cache of what is on disk and is
// rebuilt from the directory when it is next needed, so dropping one
// costs one read of that job's record and one listing of its returns,
// once. What this bounds is a hub with many jobs that will never
// complete -- nodes that are gone -- holding a node set for each.
const maxIndexedJobs = 256

// indexCap is maxIndexedJobs, or a test's smaller bound.
func (c *Cache) indexCap() int {
	if c.maxIndexed > 0 {
		return c.maxIndexed
	}
	return maxIndexedJobs
}

// returnIndex is, for one job, which targets have answered.
//
// It is the cache's own view of the job's returns directory, built from
// that directory the first time a return for the job is filed and kept
// current by File, which is the only thing that writes there. That is
// what makes the count trustworthy: nothing writes a return without
// going through it, and an index is never dropped while anybody is
// filing through it (users), so a rebuilt one cannot miss a write that
// was in flight.
type returnIndex struct {
	// users is how many callers hold the index. Guarded by Cache.idxMu,
	// not by mu, for the same reason jobLock.users is.
	users int

	built    bool
	nodes    []string
	targets  map[string]struct{}
	answered map[string]struct{}
	// pending is a return being written: a second return from the same
	// node waits for the first to finish rather than writing too. See
	// File.
	pending map[string]chan struct{}
	// shards is which of the job's return shards this index has made
	// sure exist. Guarded by shardMu, which is held across the mkdir and
	// the sync of the returns directory, so that a return filed into a
	// shard is never acknowledged before the shard's own name is durable.
	shards  map[string]bool
	shardMu sync.Mutex
	// marked is whether the job's record says its returns are sharded
	// (Job.ReturnsSharded). Taken from the record when the index is
	// built; guarded by shardMu after that.
	marked      bool
	remaining   int
	fun         string
	correlation string

	mu sync.Mutex
}

// File files one node's answer and says what it learned.
//
// The same contract as AddReturn, which is now a wrapper over it:
// idempotent by (jid, node), refused from a node the job was not sent
// to, chunk 0 only.
//
// **The count is decremented only after the file is on disk.** The other
// order loses completions: two returns racing, the second to take the
// count to zero calls completeIfDone, whose Missing lists the directory
// before the first has finished writing, sees one node missing and does
// nothing; and the first, which saw a count of one, does not call it at
// all. The job stayed dispatched for ever. Taking the count after the
// write means whoever brings it to zero does so after every other
// return it counted is readable.
//
// A retry that arrives while the first copy is still being written
// waits for it rather than writing a second copy. It used to be two
// writes, two "fresh" answers and two events for one return, because
// the existence check and the write were not one step.
func (c *Cache) File(r *Return) (Filing, error) {
	if _, err := c.jobDir(r.JID); err != nil {
		return Filing{}, err
	}
	if r.Chunk != 0 {
		return Filing{}, fmt.Errorf("the return from %s is chunk %d, and this hub takes one return per node, chunk 0", r.NodeID, r.Chunk)
	}
	e, release, err := c.index(r.JID)
	if err != nil {
		return Filing{}, err
	}
	defer release()
	// index returns with e.mu held.
	f := Filing{Fun: e.fun, Correlation: e.correlation}
	if _, ok := e.targets[r.NodeID]; !ok {
		e.mu.Unlock()
		return f, fmt.Errorf("%s: %w: %s", r.JID, ErrNotTargeted, r.NodeID)
	}
	name := returnFile(r.NodeID, r.Chunk)
	if name == "" {
		e.mu.Unlock()
		return f, fmt.Errorf("%q is not a node identity", r.NodeID)
	}
	for {
		if _, ok := e.answered[r.NodeID]; ok {
			f.Remaining = e.remaining
			e.mu.Unlock()
			return f, nil
		}
		wait, busy := e.pending[r.NodeID]
		if !busy {
			break
		}
		e.mu.Unlock()
		<-wait
		e.mu.Lock()
	}
	done := make(chan struct{})
	e.pending[r.NodeID] = done
	e.mu.Unlock()

	err = c.ensureShard(r.JID, e, returnShard(r.NodeID))
	if err == nil {
		err = c.writeReturn(r, name)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.pending, r.NodeID)
	close(done)
	if err != nil {
		return f, err
	}
	e.answered[r.NodeID] = struct{}{}
	// Still a target: refreshIndex may have changed the node set while
	// the file was being written, and recounted without this one.
	if _, ok := e.targets[r.NodeID]; ok {
		e.remaining--
	}
	f.Fresh, f.Remaining = true, e.remaining
	return f, nil
}

func (c *Cache) writeReturn(r *Return, name string) error {
	dir, err := c.jobDir(r.JID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the return from %s: %w", r.NodeID, err)
	}
	return writeAtomic(filepath.Join(dir, "returns", returnShard(r.NodeID), name), append(raw, '\n'), 0o600)
}

// returnShards is how many subdirectories a job's returns are spread
// over.
//
// They used to share one directory, and every return is a create, a
// rename and two directory lookups there, each behind that directory's
// lock. On FreeBSD a lookup takes the vnode lock shared and a create or
// rename takes it exclusive, so 20,000 returns queued on one lock: a ZFS
// hub at 20,000 nodes had its 64 write slots all waiting in
// lockmgr_slock_hard with the disk idle (DIVERGENCE 5.659). 256 spreads
// 20,000 returns to about 80 a directory and a job to a handful of nodes
// still makes only a handful.
const returnShards = 256

// returnShard is the subdirectory of returns/ that a node's return goes
// in: two hex digits of FNV-1a of the node's identity. Stable across
// builds and platforms, because a hub reads back what an earlier run of
// it filed.
func returnShard(nodeID string) string {
	h := uint32(2166136261)
	for i := 0; i < len(nodeID); i++ {
		h ^= uint32(nodeID[i])
		h *= 16777619
	}
	return fmt.Sprintf("%02x", h%returnShards)
}

// isShard says whether name is one of returnShard's directory names.
func isShard(name string) bool {
	if len(name) != 2 {
		return false
	}
	for i := 0; i < 2; i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ensureShard makes the shard directory a return is about to be written
// into, once per job index, and syncs returns/ so that the shard's name
// survives a power loss before any return in it is acknowledged.
func (c *Cache) ensureShard(id ID, e *returnIndex, shard string) error {
	e.shardMu.Lock()
	defer e.shardMu.Unlock()
	if e.shards[shard] {
		return nil
	}
	dir, err := c.jobDir(id)
	if err != nil {
		return err
	}
	returns := filepath.Join(dir, "returns")
	// The job is marked before its first shard exists, so a sharded
	// return never exists for a record that does not say so.
	if !e.marked {
		if err := c.markSharded(id, returns); err != nil {
			return err
		}
		e.marked = true
	}
	if err := fileperm.MkdirAll(filepath.Join(returns, shard), 0o700); err != nil {
		return fmt.Errorf("creating a returns shard for %s: %w", id, err)
	}
	// Best effort, as atomicfile's own directory sync is: a directory
	// cannot be opened for reading on Windows.
	if d, err := os.Open(returns); err == nil {
		_ = d.Sync()
		d.Close()
	}
	if e.shards == nil {
		e.shards = map[string]bool{}
	}
	e.shards[shard] = true
	return nil
}

// shardMarker is a file written into returns/ when a job's returns
// start going into shards. It is there for a hub older than the shards,
// after a rollback: such a hub reads every *.json directly in returns/
// as a return and fails, naming the file, on one it cannot decode. So
// the marker's content is a JSON string, which no build decodes as a
// return, and its name says why. Without it a rolled-back hub reported
// the job as having no returns and every node as missing, and nothing
// said the returns were there.
//
// A build that knows the shards skips it (returnFiles).
const shardMarker = "returns-are-in-shards-read-by-a-newer-halite.json"

// shardMarkerBody is the marker's content, for whoever opens it.
const shardMarkerBody = `"This job's returns are in the two-character subdirectories ` +
	`of this directory, where a halite hub from DIVERGENCE 5.660 on files them. ` +
	`An older hub cannot read them: run a newer one."` + "\n"

// markSharded records, durably and before any shard exists, that a
// job's returns are sharded: ReturnsSharded in the record, which stamps
// it ShardedJobSchema so that an older build refuses to write it, and
// the marker an older build fails to read.
func (c *Cache) markSharded(id ID, returns string) error {
	if _, err := c.Update(id, func(j *Job) error {
		j.ReturnsSharded = true
		return nil
	}); err != nil {
		return fmt.Errorf("marking the returns of %s as sharded: %w", id, err)
	}
	if err := writeAtomic(filepath.Join(returns, shardMarker), []byte(shardMarkerBody), 0o600); err != nil {
		return fmt.Errorf("marking the returns of %s as sharded: %w", id, err)
	}
	return nil
}

// returnFiles lists a job's return files as paths relative to returns/,
// from both layouts: shard subdirectories, and files directly in
// returns/, which is where a hub before DIVERGENCE 5.660 put them and
// where a job it filed still has them.
func returnFiles(returns string) ([]string, error) {
	entries, err := os.ReadDir(returns)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ent := range entries {
		switch {
		case ent.IsDir() && isShard(ent.Name()):
			inner, err := os.ReadDir(filepath.Join(returns, ent.Name()))
			if err != nil {
				return nil, err
			}
			for _, f := range inner {
				if !f.IsDir() && filepath.Ext(f.Name()) == ".json" {
					out = append(out, filepath.Join(ent.Name(), f.Name()))
				}
			}
		case ent.Name() == shardMarker:
		case !ent.IsDir() && filepath.Ext(ent.Name()) == ".json":
			out = append(out, ent.Name())
		}
	}
	return out, nil
}

// index returns a job's return index, built and with its mutex held,
// and the release to call when done with it (after unlocking).
func (c *Cache) index(id ID) (*returnIndex, func(), error) {
	c.idxMu.Lock()
	if c.idx == nil {
		c.idx = map[ID]*returnIndex{}
	}
	e, ok := c.idx[id]
	if !ok {
		if len(c.idx) >= c.indexCap() {
			for other, idle := range c.idx {
				if idle.users == 0 {
					delete(c.idx, other)
				}
			}
		}
		e = &returnIndex{}
		c.idx[id] = e
	}
	e.users++
	c.idxMu.Unlock()

	release := func() {
		c.idxMu.Lock()
		defer c.idxMu.Unlock()
		e.users--
		if e.users > 0 || c.idx[id] != e {
			return
		}
		// A finished job's index is dropped: nothing more is owed, and
		// a late retry rebuilds it from the directory once. One that
		// never built (no such job) is dropped so it is not a record of
		// every jid somebody guessed.
		e.mu.Lock()
		drop := !e.built || e.remaining == 0
		e.mu.Unlock()
		if drop {
			delete(c.idx, id)
		}
	}

	e.mu.Lock()
	if !e.built {
		if err := c.build(id, e); err != nil {
			e.mu.Unlock()
			release()
			return nil, nil, err
		}
	}
	return e, release, nil
}

// build reads a job's record and lists its returns. Called with e.mu
// held, once per index.
func (c *Cache) build(id ID, e *returnIndex) error {
	j, err := c.Get(id)
	if err != nil {
		return err
	}
	dir, err := c.jobDir(id)
	if err != nil {
		return err
	}
	files, err := returnFiles(filepath.Join(dir, "returns"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading the returns for %s: %w", id, err)
	}
	e.answered = make(map[string]struct{}, len(files))
	e.pending = map[string]chan struct{}{}
	e.setRecord(j)
	e.marked = j.ReturnsSharded
	for _, f := range files {
		node, ok := strings.CutSuffix(filepath.Base(f), ".json")
		if !ok {
			continue
		}
		// The file is named for the node that filed it (returnFile), so
		// the name is the node. A name that is not a target is not a
		// return this index counts -- Missing would not count it either.
		if _, ok := e.targets[node]; ok {
			e.answered[node] = struct{}{}
		}
	}
	e.recount()
	e.built = true
	return nil
}

// setRecord takes what the index keeps from a job's record.
func (e *returnIndex) setRecord(j *Job) {
	e.fun, e.correlation = j.Fun, j.Correlation
	if e.targets != nil && slices.Equal(e.nodes, j.Nodes) {
		return
	}
	e.nodes = slices.Clone(j.Nodes)
	e.targets = make(map[string]struct{}, len(j.Nodes))
	for _, n := range j.Nodes {
		e.targets[n] = struct{}{}
	}
}

// recount is how many targets have not answered. A return still being
// written is not answered yet; File decrements for it when it is.
func (e *returnIndex) recount() {
	e.remaining = 0
	for n := range e.targets {
		if _, ok := e.answered[n]; !ok {
			e.remaining++
		}
	}
}

// refreshIndex keeps an index in step with a record that has just been
// written. Nothing in the hub changes a job's node set, function or
// correlation after dispatch; this is so that, if something ever does,
// the count follows the record rather than quietly disagreeing with it.
func (c *Cache) refreshIndex(j *Job) {
	c.idxMu.Lock()
	e := c.idx[j.JID]
	c.idxMu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.built {
		return
	}
	same := slices.Equal(e.nodes, j.Nodes)
	e.setRecord(j)
	if !same {
		e.recount()
	}
}

// forgetIndex drops a job's index, for a job whose records are gone.
func (c *Cache) forgetIndex(id ID) {
	c.idxMu.Lock()
	delete(c.idx, id)
	c.idxMu.Unlock()
}
