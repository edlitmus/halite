package job

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/fileperm"
)

func signedJob(t *testing.T, at time.Time, ttl time.Duration) *Job {
	t.Helper()
	return &Job{JID: NewID(at), Fun: "state.apply", Nonce: mustNonce(t), Expires: at.Add(ttl), Signature: "sig"}
}

func isReplay(err error) bool {
	var r *Refusal
	return errors.As(err, &r) && r.Reason == ReasonReplayed
}

// A signed job is accepted once, by jid, whatever nonce it comes with.
func TestASignedJidIsAdmittedOnceWhateverItsNonce(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	s := NewSignedReplay(filepath.Join(t.TempDir(), "signed-jobs"))
	s.Now = func() time.Time { return now }
	j := signedJob(t, now, DefaultTTL)
	if err := s.Admit(j); err != nil {
		t.Fatalf("a fresh signed job was refused: %v", err)
	}
	again := *j
	again.Nonce = mustNonce(t)
	if err := s.Admit(&again); !isReplay(err) {
		t.Fatalf("the same signed jid with a fresh nonce gave %v, want a replay", err)
	}
	if !s.Seen(j.JID) {
		t.Error("Seen does not report a recorded jid")
	}
}

// The record survives the process, which is the point of it.
func TestASignedJidIsRememberedAcrossARestart(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "signed-jobs")
	first := NewSignedReplay(path)
	first.Now = func() time.Time { return now }
	j := signedJob(t, now, DefaultTTL)
	if err := first.Admit(j); err != nil {
		t.Fatal(err)
	}

	restarted := NewSignedReplay(path)
	restarted.Now = func() time.Time { return now.Add(time.Minute) }
	j.Nonce = mustNonce(t)
	if err := restarted.Admit(j); !isReplay(err) {
		t.Fatalf("after a restart the same signed job gave %v, want a replay", err)
	}
	// fileperm.Others rather than the mode bits: on Windows the mode is
	// the read-only attribute and says nothing about who can read it.
	if others, err := fileperm.Others(path); err != nil {
		t.Fatal(err)
	} else if len(others) > 0 {
		t.Errorf("the record is readable beyond its owner: %v", others)
	}
}

// Entries go once their job could no longer be admitted, so the record is
// bounded by what was signed in one expiry window; one with no expiry is
// kept, because its signature never stops being good.
func TestTheSignedRecordForgetsOnlyWhatHasExpired(t *testing.T) {
	start := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "signed-jobs")
	s := NewSignedReplay(path)
	s.Now = func() time.Time { return start }
	old := signedJob(t, start, DefaultTTL)
	forever := &Job{JID: NewID(start.Add(time.Second)), Fun: "state.apply", Nonce: mustNonce(t), Signature: "sig"}
	for _, j := range []*Job{old, forever} {
		if err := s.Admit(j); err != nil {
			t.Fatal(err)
		}
	}

	later := start.Add(DefaultTTL + SignedReplayRetention + time.Minute)
	reloaded := NewSignedReplay(path)
	reloaded.Now = func() time.Time { return later }
	if reloaded.Seen(old.JID) {
		t.Error("a jid long past its expiry is still held")
	}
	if !reloaded.Seen(forever.JID) {
		t.Error("a signed jid with no expiry was forgotten, so it could be replayed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), string(old.JID)) {
		t.Errorf("the expired entry is still in the file after a load:\n%s", data)
	}

	// Inside the retention, an expired entry is kept: a clock stepped
	// back must not reopen it.
	s2 := NewSignedReplay(filepath.Join(t.TempDir(), "signed-jobs"))
	s2.Now = func() time.Time { return start }
	if err := s2.Admit(old); err != nil {
		t.Fatal(err)
	}
	s2.Now = func() time.Time { return start.Add(DefaultTTL + time.Hour) }
	if !s2.Seen(old.JID) {
		t.Error("an entry an hour past its expiry was dropped, inside the retention")
	}
}

// A record that cannot be read refuses signed jobs rather than starting
// empty, which is what a restart used to do. A torn last line -- a write
// that never returned -- is the one thing tolerated.
func TestAnUnreadableSignedRecordRefusesRatherThanForgets(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	torn := filepath.Join(dir, "torn")
	good := formatSignedReplayLine(NewID(now), now.Add(DefaultTTL))
	if err := os.WriteFile(torn, []byte(good+"20261015T1200"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSignedReplay(torn)
	s.Now = func() time.Time { return now }
	if !s.Seen(NewID(now)) {
		t.Error("a complete line before a torn one was not read")
	}

	corrupt := filepath.Join(dir, "corrupt")
	if err := os.WriteFile(corrupt, []byte("garbage line\n"+good), 0o600); err != nil {
		t.Fatal(err)
	}
	s = NewSignedReplay(corrupt)
	s.Now = func() time.Time { return now }
	if err := s.Admit(signedJob(t, now.Add(time.Hour), DefaultTTL)); err == nil {
		t.Error("a signed job was accepted with a record this node could not read")
	}

	// A record that cannot be written refuses the job.
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s = NewSignedReplay(filepath.Join(blocked, "signed-jobs"))
	s.Now = func() time.Time { return now }
	if err := s.Admit(signedJob(t, now, DefaultTTL)); err == nil {
		t.Error("a signed job was accepted with nowhere to record it")
	}
}

// The file is compacted rather than growing for ever.
func TestTheSignedRecordIsCompacted(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "signed-jobs")
	s := NewSignedReplay(path)
	s.compactAt = 16
	at := now
	s.Now = func() time.Time { return at }
	// An hour apart and expiring in a minute, so that all but the last
	// day's are dead by the end.
	for i := 0; i < 100; i++ {
		at = now.Add(time.Duration(i) * time.Hour)
		if err := s.Admit(signedJob(t, at, time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Count(string(data), "\n")
	// Live: what expired within the retention, an hour apart.
	live := int(SignedReplayRetention/time.Hour) + 2
	if lines > live+s.compactAt+1 {
		t.Errorf("the record holds %d lines for about %d live entries", lines, live)
	}
	if s.Len() > live {
		t.Errorf("%d entries held, at most %d live", s.Len(), live)
	}
}

// Check reports without recording, so that a job refused later -- for its
// signature -- has not taken a place in the cache.
func TestGuardCheckDoesNotRecord(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	g := NewGuard(16)
	g.Now = func() time.Time { return now }
	j := &Job{JID: NewID(now), Fun: "test.ping", Nonce: mustNonce(t), Expires: now.Add(DefaultTTL)}
	for i := 0; i < 3; i++ {
		if err := g.Check(j); err != nil {
			t.Fatal(err)
		}
	}
	if g.Len() != 0 {
		t.Errorf("Check recorded %d keys", g.Len())
	}
	if err := g.Admit(j); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(j); !isReplay(err) {
		t.Errorf("Check of an admitted job gave %v, want a replay", err)
	}
}
