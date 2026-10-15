package job

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SignedReplay is the node's record of every signed job it has accepted,
// kept on disk until the job's own expiry has passed: the replay cache
// SPEC 6.3 asks for, for the jobs whose replay a node can actually be made
// to suffer.
//
// # Why signed jobs have a cache of their own
//
// Guard is in memory, one per process, bounded by count, and keyed by jid
// and nonce. That is enough against a hub that redelivers a job by
// accident, and it is no protection at all against a hub that means it,
// for three reasons that were each reproduced (DIVERGENCE 5.290):
//
//   - It forgets on restart, and a signed job is good until its expiry,
//     which can be long after the node has restarted.
//   - It evicts the oldest entry when full, and the hub decides how many
//     jobs arrive. Four thousand junk `test.ping`s pushed a signed jid out,
//     and the same signed job was accepted again.
//   - The nonce is the hub's choice and the signature does not cover it,
//     so a replay with a fresh nonce is a fresh nonce.
//
// None of this matters for an unsigned job: a hub that wants to run one
// again can mint a new one, so there is nothing for a replay cache to
// defend. It matters only where the hub cannot mint the job, which is a
// signed one. So this is keyed by jid, which the signature covers and the
// hub cannot change without breaking it; it is not evicted by count,
// because only a signer can add to it and the hub cannot flood it; and it
// is on disk, written before the job is queued, because the window it has
// to cover is the job's expiry and not this process's lifetime.
//
// # Why not refuse anything signed before the node started
//
// The alternative design, with no state at all, is to refuse a signed job
// whose jid predates this process's start. It would refuse the jobs that
// matter most: a job queued on the hub for a node that is off is
// delivered when the node comes up, and its jid is older than the node's
// start by construction. So it is the record that is kept.
//
// # Bounded
//
// An entry is dropped once its job could no longer be admitted anyway --
// past its expiry, plus SignedReplayRetention for a clock that is
// corrected backwards -- so the set is what operators signed within one
// expiry window, and it grows with how often operators sign rather than
// with anything the hub sends. A signed job with no expiry at all is kept
// for ever: its signature is a permanent authorisation, so the protection
// against replaying it has to be permanent too. That is a set only an
// operator's signer can add to, and `halite-hub run` never signs without
// an expiry; a node that refuses such signatures outright keeps none.
type SignedReplay struct {
	path string

	mu     sync.Mutex
	seen   map[ID]time.Time // jid -> expiry; zero is "never"
	lines  int              // lines in the file, live or not
	loaded bool
	err    error

	// Now is the clock, for the tests.
	Now func() time.Time
	// compactAt overrides signedReplayCompactAt, for the tests: every
	// append is synced, and three thousand of them took forty seconds
	// on a Mac.
	compactAt int
}

// SignedReplayRetention is how long past its expiry a signed jid is kept.
//
// Guard refuses a job past its expiry, so in principle an expired entry
// can go at once. A day, because "in principle" assumes a clock that only
// moves forwards, and NTP stepping a node's clock back by minutes after a
// boot is exactly when a queued job arrives. The cost is a day's worth of
// lines.
const SignedReplayRetention = 24 * time.Hour

// signedReplayCompactAt is how many dead lines the file may carry before
// it is rewritten.
const signedReplayCompactAt = 1024

// NewSignedReplay names the file the record lives in. Nothing is read
// until the first signed job, so a node that never sees one never creates
// it.
func NewSignedReplay(path string) *SignedReplay {
	return &SignedReplay{path: path}
}

func (s *SignedReplay) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Admit records a signed job and reports whether it may run.
//
// The record is on disk, synced, before this returns nil, so a job that
// is then queued and run cannot be lost from it by a crash. A record that
// cannot be written refuses the job: the property this exists for is
// that a signed job runs once, and "probably once" is not it.
func (s *SignedReplay) Admit(j *Job) error {
	if j == nil || j.JID == "" {
		return &Refusal{Reason: ReasonMalforme, Detail: "the job carries no identifier"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return err
	}
	if _, ok := s.seen[j.JID]; ok {
		return &Refusal{
			Reason: ReasonReplayed,
			Detail: fmt.Sprintf("this node has already accepted the signed job %s", j.JID),
		}
	}
	if err := s.append(j.JID, j.Expires); err != nil {
		return fmt.Errorf("recording the signed job %s so that it cannot be replayed: %w; "+
			"refused, because a signed job this node cannot remember is one it could run twice", j.JID, err)
	}
	s.seen[j.JID] = j.Expires
	s.prune()
	return nil
}

// Seen reports whether a signed jid is already recorded, without
// recording it -- for a refusal before the signature has been checked.
func (s *SignedReplay) Seen(id ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.load() != nil {
		return false
	}
	_, ok := s.seen[id]
	return ok
}

// Len is how many jids are held, for a test.
func (s *SignedReplay) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

// load reads the file once. Called with the lock held.
//
// A file that cannot be read is remembered as an error and every signed
// job is refused with it, rather than starting from empty: an empty
// record after a restart is exactly the hole this closes. A line that
// does not parse is the same -- a torn last line from a crash mid-write
// is the one exception, because the write it belongs to never returned
// and so admitted nothing.
func (s *SignedReplay) load() error {
	if s.loaded {
		return s.err
	}
	s.loaded = true
	s.seen = map[ID]time.Time{}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		s.err = fmt.Errorf("reading this node's record of signed jobs: %w", err)
		return s.err
	}
	text := string(data)
	complete := strings.HasSuffix(text, "\n") || text == ""
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		jid, expires, err := parseSignedReplayLine(line)
		if err != nil {
			if i == len(lines)-1 && !complete {
				continue
			}
			s.err = fmt.Errorf("this node's record of signed jobs, %s, line %d: %w", s.path, i+1, err)
			return s.err
		}
		s.seen[jid] = expires
		s.lines++
	}
	s.prune()
	// Rewritten on load, which is where most of the dead lines are:
	// everything that expired while the node was down.
	if s.lines > len(s.seen) || !complete {
		if err := s.rewrite(); err != nil {
			s.err = err
			return s.err
		}
	}
	return nil
}

// The line format: `<jid> <expiry, RFC 3339 nanoseconds UTC, or never>`.
func formatSignedReplayLine(jid ID, expires time.Time) string {
	at := "never"
	if !expires.IsZero() {
		at = expires.UTC().Format(time.RFC3339Nano)
	}
	return string(jid) + " " + at + "\n"
}

func parseSignedReplayLine(line string) (ID, time.Time, error) {
	jid, at, ok := strings.Cut(line, " ")
	if !ok || !ID(jid).Valid() {
		return "", time.Time{}, fmt.Errorf("%q is not `<jid> <expiry>`", line)
	}
	if at == "never" {
		return ID(jid), time.Time{}, nil
	}
	expires, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%q: the expiry is not a timestamp: %w", line, err)
	}
	return ID(jid), expires, nil
}

// prune drops what can no longer be admitted. Called with the lock held.
func (s *SignedReplay) prune() {
	cutoff := s.now().Add(-SignedReplayRetention)
	for jid, expires := range s.seen {
		if !expires.IsZero() && expires.Before(cutoff) {
			delete(s.seen, jid)
		}
	}
}

// append writes one line and syncs it. Called with the lock held.
func (s *SignedReplay) append(jid ID, expires time.Time) error {
	limit := signedReplayCompactAt
	if s.compactAt > 0 {
		limit = s.compactAt
	}
	if s.lines-len(s.seen) > limit {
		if err := s.rewrite(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(formatSignedReplayLine(jid, expires)); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.lines++
	return nil
}

// rewrite replaces the file with the live entries: written beside it,
// synced, and renamed over it, so that a crash leaves the old file or the
// new one and never half of either. Called with the lock held.
func (s *SignedReplay) rewrite() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".signed-jobs-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	var b strings.Builder
	for jid, expires := range s.seen {
		b.WriteString(formatSignedReplayLine(jid, expires))
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.lines = len(s.seen)
	return nil
}
