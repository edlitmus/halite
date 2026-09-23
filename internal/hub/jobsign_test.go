package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
)

// The identifier and the expiry of a signed job are the caller's, and
// the hub checks what it can about them without a key.
//
// It cannot check the signature: it holds no signer key, which is the
// point of SPEC 25.6. What it can check is that the identifier is well
// formed, that it has not seen it before, and that the expiry has not
// already passed — the three things that stop a caller replaying a
// signed submission at the hub rather than at a node.
func TestASignedSubmissionBringsItsOwnIdentifier(t *testing.T) {
	cache, err := job.OpenCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Jobs: cache}
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)
	expires := now.Add(15 * time.Minute)

	jid, got, err := srv.identify(Submission{
		Signature: "a-signature",
		JID:       "20260923T080000000001",
		Expires:   expires,
	}, now, time.Minute)
	if err != nil {
		t.Fatalf("a signed submission was refused: %v", err)
	}
	if jid != "20260923T080000000001" {
		t.Errorf("the hub assigned %q instead of taking the signed identifier", jid)
	}
	if !got.Equal(expires) {
		t.Errorf("the hub set the expiry to %s instead of the signed %s", got, expires)
	}
}

func TestAnUnsignedSubmissionGetsTheHubsIdentifier(t *testing.T) {
	srv := &Server{}
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)

	jid, expires, err := srv.identify(Submission{}, now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !jid.Valid() {
		t.Errorf("the hub produced %q, which is not a job identifier", jid)
	}
	if !expires.Equal(now.Add(15 * time.Minute)) {
		t.Errorf("the expiry is %s, expected now plus the time to live", expires)
	}
}

// An unsigned submission may not choose either, or a caller could pick
// its own identifier and collide with somebody else's job.
func TestAnUnsignedSubmissionMayNotChooseItsIdentifier(t *testing.T) {
	srv := &Server{}
	now := time.Now()
	for _, sub := range []Submission{
		{JID: "20260923T080000000001"},
		{Expires: now.Add(time.Hour)},
	} {
		if _, _, err := srv.identify(sub, now, time.Minute); err == nil {
			t.Errorf("%+v was accepted without a signature", sub)
		}
	}
}

func TestASignedSubmissionIsCheckedBeforeItIsRelayed(t *testing.T) {
	cache, err := job.OpenCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)

	cases := []struct {
		what string
		sub  Submission
		want string
	}{
		{
			what: "an identifier that is not one",
			sub:  Submission{Signature: "s", JID: "yesterday", Expires: now.Add(time.Hour)},
			want: "is not one",
		},
		{
			what: "no expiry",
			sub:  Submission{Signature: "s", JID: "20260923T080000000002"},
			want: "absolute expiry",
		},
		{
			what: "an expiry that has passed",
			sub:  Submission{Signature: "s", JID: "20260923T080000000003", Expires: now.Add(-time.Minute)},
			want: "expired at",
		},
	}
	for _, c := range cases {
		srv := &Server{Jobs: cache}
		_, _, err := srv.identify(c.sub, now, time.Minute)
		if err == nil {
			t.Errorf("%s was accepted", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: the message is %q, expected it to mention %q", c.what, err, c.want)
		}
	}
}

// The same signed submission twice is one job.
//
// The node's replay guard would refuse the second delivery anyway; this
// stops it reaching the node at all, and stops a second record of the
// same identifier overwriting the first in the job cache.
func TestASignedSubmissionCannotBeReplayedAtTheHub(t *testing.T) {
	cache, err := job.OpenCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Jobs: cache}
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)
	sub := Submission{
		Signature: "a-signature",
		JID:       "20260923T080000000004",
		Expires:   now.Add(time.Hour),
	}

	if _, _, err := srv.identify(sub, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	// Recorded as Dispatch would, which is what the second attempt has
	// to see.
	if err := cache.Put(&job.Job{JID: sub.JID, Fun: "test.ping", Expires: sub.Expires}); err != nil {
		t.Fatal(err)
	}
	_, _, err = srv.identify(sub, now, time.Minute)
	if err == nil {
		t.Fatal("the same signed submission was accepted twice")
	}
	if !strings.Contains(err.Error(), "already has a job") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}
