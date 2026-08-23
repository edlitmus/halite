package hub

import (
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
// already on disk for the batch machinery.
func (s *Server) deliverQueued(nodeID string) {
	if s.Jobs == nil {
		return
	}
	// Recent jobs only. A queued job lives an hour by default and two
	// hundred covers a busy hub's hour many times over.
	jobs, err := s.Jobs.List(200)
	if err != nil {
		s.warn("could not look for queued jobs", "node_id", nodeID, "error", err.Error())
		return
	}
	now := s.now()
	for _, j := range jobs {
		if j.Offline != job.Queue || !j.IsQueuedFor(nodeID) {
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
			j.Dequeue(nodeID)
			if err := s.Jobs.Put(j); err != nil {
				s.warn("could not clear an expired queue entry",
					"jid", string(j.JID), "node_id", nodeID, "error", err.Error())
			}
			continue
		}
		if s.fleet().Send(nodeID, messageFor(j)) {
			j.Dequeue(nodeID)
			if err := s.Jobs.Put(j); err != nil {
				s.warn("could not record a queued delivery",
					"jid", string(j.JID), "node_id", nodeID, "error", err.Error())
			}
			s.info("delivered a queued job", "jid", string(j.JID), "node_id", nodeID, "fun", j.Fun)
			s.emit("halite/job/"+string(j.JID)+"/queued/"+nodeID, nodeID, map[string]any{
				"jid": string(j.JID), "fun": j.Fun,
			})
		}
	}
}
