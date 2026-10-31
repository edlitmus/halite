package hub

import (
	"sort"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/job"
)

// QueuedTTL is SPEC 9.5's default lifetime for a queued job.
//
// Longer than the ordinary fifteen minutes, because the point of
// queueing is to wait for a machine that is off; shorter than "until
// someone notices", because the hazard the section is explicit about is
// a node that comes back after two weeks and applies a two-week-old
// instruction.
const QueuedTTL = time.Hour

// deliverQueued gives a node the jobs it missed while it was away.
//
// Called when a node's stream opens. The spool is not a separate
// structure: a queued job records who it matched before delivery, so
// the nodes it has not reached are exactly `Remaining`, and that is
// already on disk for the batch machinery. Which jobs to look at comes from
// queueIndex, because looking among the most recent jobs missed a queued
// one as soon as enough others had run since (DIVERGENCE 5.500).
func (s *Server) deliverQueued(nodeID string) {
	if s.Jobs == nil {
		return
	}
	if err := s.queued.seed(s.Jobs); err != nil {
		s.warn("could not look for queued jobs", "node_id", nodeID, "error", err.Error())
		return
	}
	now := s.now()
	for _, id := range s.queued.candidates(nodeID) {
		j, err := s.Jobs.Get(id)
		if err != nil || j.Offline != job.Queue || !j.IsQueuedFor(nodeID) {
			// Pruned, killed, or already delivered: the hint was stale.
			s.queued.drop(nodeID, id)
			continue
		}
		if j.Expired(now) {
			// SPEC 9.5: a queued job that expires produces an event
			// and an audit record rather than silence. A node that
			// comes back to find nothing happened, and no reason why,
			// is the failure mode this exists to avoid.
			s.warn("a queued job expired before its node returned",
				"jid", string(j.JID), "node_id", nodeID,
				"expired", j.Expires.UTC().Format(time.RFC3339))
			s.emit("halite/job/"+string(j.JID)+"/expired", nodeID, map[string]any{
				"jid": string(j.JID), "fun": j.Fun,
				"expires": j.Expires.UTC().Format(time.RFC3339),
				"reason":  "the node did not connect before the job expired",
			})
			if _, err := s.Jobs.Update(j.JID, func(cur *job.Job) error {
				cur.Dequeue(nodeID)
				return nil
			}); err != nil {
				s.warn("could not clear an expired queue entry",
					"jid", string(j.JID), "node_id", nodeID, "error", err.Error())
				continue
			}
			s.queued.drop(nodeID, j.JID)
			continue
		}
		if s.fleet().Send(nodeID, messageFor(j)) {
			// Through Update, not Put. The node can run the job and
			// send its return before this line, and the goroutine
			// recording that return is editing the same record. A
			// whole-record Put here writes back the copy `List` gave
			// us, which still has this node's return missing and, when
			// the two land the other way round, puts this very spool
			// entry back -- so the node is sent the job again the next
			// time it connects.
			if _, err := s.Jobs.Update(j.JID, func(cur *job.Job) error {
				cur.Dequeue(nodeID)
				return nil
			}); err != nil {
				s.warn("could not record a queued delivery",
					"jid", string(j.JID), "node_id", nodeID, "error", err.Error())
			} else {
				s.queued.drop(nodeID, j.JID)
			}
			s.info("delivered a queued job", "jid", string(j.JID), "node_id", nodeID, "fun", j.Fun)
			s.emit("halite/job/"+string(j.JID)+"/queued/"+nodeID, nodeID, map[string]any{
				"jid": string(j.JID), "fun": j.Fun,
			})
		}
	}
}

// queueIndex finds the jobs spooled for a node.
//
// deliverQueued used to take the node's queued jobs from the 200 most recent
// in the cache, on the comment's word that two hundred "covers a busy hub's hour
// many times over". A hub that runs a job every ten seconds runs 360 in an
// hour, and a job queued for an hour was then out of the window and never
// delivered, expired or reported. The window cannot be widened enough without
// walking the whole cache each time any node connects, which after a hub restart
// is every node at once.
//
// This is a hint and not the record: the job's own `Queued` list stays the
// truth, every candidate is read and checked before it is sent, and an entry
// that turns out stale (pruned, killed, already delivered) is dropped. It is
// filled once from the cache, on the first node to connect, and then by each
// job that is queued.
type queueIndex struct {
	mu     sync.Mutex
	seeded bool
	by     map[string]map[job.ID]struct{}
}

// seed fills the index from the cache, once. A failure is retried on the next
// connection rather than remembered.
func (q *queueIndex) seed(jobs *job.Cache) error {
	q.mu.Lock()
	done := q.seeded
	q.mu.Unlock()
	if done {
		return nil
	}
	all, err := jobs.List(0)
	if err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range all {
		if j.Offline == job.Queue {
			q.addLocked(j)
		}
	}
	q.seeded = true
	return nil
}

// add records the nodes a job is spooled for.
func (q *queueIndex) add(j *job.Job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.addLocked(j)
}

func (q *queueIndex) addLocked(j *job.Job) {
	if q.by == nil {
		q.by = map[string]map[job.ID]struct{}{}
	}
	for _, node := range j.Queued {
		if q.by[node] == nil {
			q.by[node] = map[job.ID]struct{}{}
		}
		q.by[node][j.JID] = struct{}{}
	}
}

// drop forgets one node's entry for a job.
func (q *queueIndex) drop(node string, id job.ID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.by[node], id)
}

// candidates are the jobs that may be spooled for a node, oldest first, which
// is the order they were asked for in.
func (q *queueIndex) candidates(node string) []job.ID {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]job.ID, 0, len(q.by[node]))
	for id := range q.by[node] {
		out = append(out, id)
	}
	sort.Slice(out, func(i, k int) bool { return out[i] < out[k] })
	return out
}
