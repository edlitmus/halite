package nodeevidence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

func open(t *testing.T, dir string, opts Options) *Log {
	t.Helper()
	if opts.NodeID == "" {
		opts.NodeID = "node-1"
	}
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func appendJobs(t *testing.T, l *Log, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		detail := map[string]string{"jid": fmt.Sprintf("2026092300000%02d", i), "fun": "test.ping"}
		if err := l.Append(KindJobAccepted, detail); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

func verify(t *testing.T, dir string) *Result {
	t.Helper()
	res, err := Verify(dir)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return res
}

func TestChainVerifiesWhatItWrote(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	appendJobs(t, l, 5)

	res := verify(t, dir)
	if !res.OK() {
		t.Fatalf("a chain this package wrote does not verify: %v", res.Breaks)
	}
	if res.Records != 5 {
		t.Errorf("read %d records, wrote 5", res.Records)
	}
	if res.First.Seq != 1 || res.Last.Seq != 5 {
		t.Errorf("the chain runs from %d to %d, expected 1 to 5", res.First.Seq, res.Last.Seq)
	}
	if res.Lost != 0 {
		t.Errorf("the chain declares %d lost records and nothing failed", res.Lost)
	}
	seq, head := l.Head()
	if seq != 5 || head != res.Last.Hash {
		t.Errorf("Head reports %d/%s and the file ends at %d/%s", seq, head, res.Last.Seq, res.Last.Hash)
	}
}

func TestTheRecordIsWrittenBeforeTheCallReturns(t *testing.T) {
	// The whole value of the record is that it is on the disk before the
	// job runs, so a node that is killed by the job it accepted has
	// recorded accepting it. Read from a second descriptor rather than
	// through the Log, because the Log's own state would agree with
	// itself whether or not anything reached the file.
	dir := t.TempDir()
	l := open(t, dir, Options{})
	if err := l.Append(KindJobAccepted, map[string]string{"jid": "1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, currentName))
	if err != nil {
		t.Fatalf("reading the segment: %v", err)
	}
	if !strings.Contains(string(data), KindJobAccepted) {
		t.Fatalf("the record is not in the file after Append returned: %q", data)
	}
}

func TestAlteringARecordFailsVerification(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	appendJobs(t, l, 3)
	l.Close()

	// The change an attacker would want: the function that ran, edited
	// in place, leaving every other field alone.
	path := filepath.Join(dir, currentName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "test.ping", "cmd.run!!", 1)
	if edited == string(data) {
		t.Fatal("the test did not change anything, so it is checking nothing")
	}
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	res := verify(t, dir)
	if res.OK() {
		t.Fatal("an edited record verified")
	}
	if !strings.Contains(res.Breaks[0].Reason, "altered") {
		t.Errorf("the break does not say the record was altered: %s", res.Breaks[0])
	}
	if res.Breaks[0].Seq != 1 {
		t.Errorf("the break names record %d, expected 1", res.Breaks[0].Seq)
	}
}

func TestRemovingARecordFromTheMiddleFailsVerification(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	appendJobs(t, l, 4)
	l.Close()

	path := filepath.Join(dir, currentName)
	lines := readLines(t, path)
	kept := append(append([]string{}, lines[:2]...), lines[3:]...)
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := verify(t, dir)
	if res.OK() {
		t.Fatal("a chain with a record cut out of it verified")
	}
	// Both checks should fire: the link and the numbering.
	var reasons string
	for _, b := range res.Breaks {
		reasons += b.Reason + "\n"
	}
	if !strings.Contains(reasons, "removed, inserted or reordered") {
		t.Errorf("no break reports a missing record: %s", reasons)
	}
	if !strings.Contains(reasons, "numbered 4 and follows record 2") {
		t.Errorf("no break reports the numbering: %s", reasons)
	}
}

func TestRemovingTheTailIsNotDetectableFromTheFileAlone(t *testing.T) {
	// This is a limitation rather than a defect, and it is the reason
	// Head exists. A hash chain makes editing and removal from the middle
	// detectable; truncating the end leaves every remaining record
	// perfectly valid, and the only thing that contradicts it is a head
	// hash recorded somewhere the node cannot reach. A test states it so
	// that nobody reads the package comment as a stronger claim.
	dir := t.TempDir()
	l := open(t, dir, Options{})
	appendJobs(t, l, 4)
	_, head := l.Head()
	l.Close()

	path := filepath.Join(dir, currentName)
	lines := readLines(t, path)
	if err := os.WriteFile(path, []byte(strings.Join(lines[:2], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := verify(t, dir)
	// The point of the test: verification is happy, because every record
	// that remains is intact and correctly linked.
	if !res.OK() {
		t.Fatalf("expected a truncated chain to verify, which is what this test is about: %v",
			res.Breaks)
	}
	if res.Records != 2 {
		t.Fatalf("the test truncated to %d records, expected 2", res.Records)
	}
	// And what does catch it: the head an operator kept somewhere this
	// node cannot reach no longer matches the end of the file. That
	// comparison is the whole value of Head, and nothing in this build
	// does it for you.
	if res.Last.Hash == head {
		t.Error("the head hash still matches, so this test truncated nothing")
	}
}

// Removing the *start* of a chain is reported, once, in words an operator
// who archived it can recognise.
//
// It is a break: the records before this one are not here, and saying so
// is the honest answer. It is one break rather than two, because a chain
// that begins at record 412 following a hash that is not present is one
// fact, and docs/operations.md tells operators to ship sealed segments
// off -- so this is a message somebody will meet on purpose.
func TestAChainMissingItsStartSaysSoOnce(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	appendJobs(t, l, 4)
	l.Close()

	path := filepath.Join(dir, currentName)
	lines := readLines(t, path)
	if err := os.WriteFile(path, []byte(strings.Join(lines[2:], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := verify(t, dir)
	if len(res.Breaks) != 1 {
		t.Fatalf("expected one break for a chain missing its start, got %d: %v",
			len(res.Breaks), res.Breaks)
	}
	if !strings.Contains(res.Breaks[0].Reason, "archived") {
		t.Errorf("the break does not offer the ordinary explanation: %s", res.Breaks[0])
	}
	if res.Breaks[0].Seq != 3 {
		t.Errorf("the break names record %d, expected the first one present", res.Breaks[0].Seq)
	}
}

func TestSealingASegmentKeepsTheChain(t *testing.T) {
	dir := t.TempDir()
	// Small enough that every record seals, which exercises the boundary
	// rather than hoping to reach it.
	l := open(t, dir, Options{MaxBytes: 1})
	appendJobs(t, l, 4)
	l.Close()

	segments, err := Segments(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 4 {
		t.Fatalf("expected four segments, got %d: %v", len(segments), segments)
	}
	// Chain order must be file order, or verification reads the records
	// out of sequence.
	res := verify(t, dir)
	if !res.OK() {
		t.Fatalf("a chain across segments does not verify: %v", res.Breaks)
	}
	if res.Records != 4 {
		t.Errorf("read %d records across segments, wrote 4", res.Records)
	}
	if res.First.Seq != 1 || res.Last.Seq != 4 {
		t.Errorf("the chain runs %d to %d", res.First.Seq, res.Last.Seq)
	}
}

func TestSealedSegmentsSortIntoChainOrderPastTen(t *testing.T) {
	// A segment named for its first sequence number sorts lexically, so
	// record 10 must not sort before record 9. Zero-padding is what makes
	// that true and a test that stopped at four records would not have
	// noticed it missing.
	dir := t.TempDir()
	l := open(t, dir, Options{MaxBytes: 1})
	appendJobs(t, l, 12)
	l.Close()

	res := verify(t, dir)
	if !res.OK() {
		t.Fatalf("twelve segments do not verify in file order: %v", res.Breaks)
	}
	if res.Last.Seq != 12 {
		t.Errorf("the chain ends at %d, expected 12", res.Last.Seq)
	}
}

func TestReopeningContinuesTheChain(t *testing.T) {
	dir := t.TempDir()
	first := open(t, dir, Options{})
	appendJobs(t, first, 2)
	_, head := first.Head()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := open(t, dir, Options{})
	seq, reopened := second.Head()
	if seq != 2 || reopened != head {
		t.Fatalf("a reopened log resumed at %d/%s, expected 2/%s", seq, reopened, head)
	}
	appendJobs(t, second, 2)
	second.Close()

	res := verify(t, dir)
	if !res.OK() {
		t.Fatalf("a chain written across two processes does not verify: %v", res.Breaks)
	}
	if res.Records != 4 {
		t.Errorf("read %d records, wrote 4", res.Records)
	}
}

func TestReopeningAfterASealContinuesTheChain(t *testing.T) {
	// The crash window: the segment was renamed and the process died
	// before the new one was created, so the directory holds sealed
	// segments and no current file.
	dir := t.TempDir()
	l := open(t, dir, Options{MaxBytes: 1})
	appendJobs(t, l, 3)
	l.Close()
	// The record that was in the current segment goes with it, which is
	// what a process killed between the rename and the create loses. What
	// must survive is the chain up to the last sealed segment.
	if err := os.Remove(filepath.Join(dir, currentName)); err != nil {
		t.Fatal(err)
	}
	sealed := verify(t, dir)
	if !sealed.OK() || sealed.Last.Seq != 2 {
		t.Fatalf("the sealed segments should hold records 1 and 2: %d, %v", sealed.Last.Seq, sealed.Breaks)
	}

	reopened := open(t, dir, Options{})
	seq, resumed := reopened.Head()
	if seq != 2 || resumed != sealed.Last.Hash {
		t.Fatalf("resumed at %d/%s, expected 2/%s", seq, resumed, sealed.Last.Hash)
	}
	appendJobs(t, reopened, 1)
	reopened.Close()
	if res := verify(t, dir); !res.OK() {
		t.Fatalf("the chain does not verify across the missing current segment: %v", res.Breaks)
	}
}

func TestAnIncompleteRecordIsDroppedAndCounted(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	appendJobs(t, l, 2)
	l.Close()

	// A process killed mid-append leaves bytes with no newline.
	path := filepath.Join(dir, currentName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schema":"halite.evidence/1","seq":3,"kind":"job.acce`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Verification before the repair must report it: an investigator
	// looking at the file as found should be told the file was cut short.
	if res := verify(t, dir); res.OK() {
		t.Fatal("a segment ending in a partial record verified")
	} else if !strings.Contains(res.Breaks[0].Reason, "incomplete record") {
		t.Errorf("the break does not name the partial record: %s", res.Breaks[0])
	}

	reopened := open(t, dir, Options{})
	appendJobs(t, reopened, 1)
	reopened.Close()

	res := verify(t, dir)
	if !res.OK() {
		t.Fatalf("the chain does not verify after the partial tail was repaired: %v", res.Breaks)
	}
	if res.Lost != 1 {
		t.Errorf("the chain declares %d lost records, expected 1", res.Lost)
	}
	if res.Last.Seq != 3 {
		t.Errorf("the chain ends at %d, expected 3", res.Last.Seq)
	}
}

func TestARecordFromANewerBuildIsNotABreak(t *testing.T) {
	// A rollback must not look like tampering. A record whose schema this
	// build does not know cannot have its contents checked -- the fields
	// it does not have would be dropped before the hash was recomputed --
	// so its links are checked and its contents are reported as
	// unverified.
	dir := t.TempDir()
	l := open(t, dir, Options{})
	appendJobs(t, l, 1)
	_, head := l.Head()
	l.Close()

	newer := Record{
		Schema: "halite.evidence/2",
		Seq:    2,
		TS:     time.Now().UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
		Kind:   KindJobAccepted,
		Prev:   head,
		Hash:   "sha256:00",
	}
	line, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, currentName), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	f.Close()

	res := verify(t, dir)
	if !res.OK() {
		t.Fatalf("a record from a newer build was reported as a break: %v", res.Breaks)
	}
	if len(res.UnknownSchemas) != 1 || res.UnknownSchemas[0] != "halite.evidence/2" {
		t.Errorf("the unverified schema was not reported: %v", res.UnknownSchemas)
	}
}

func TestOpeningRefusesToExtendANewerChain(t *testing.T) {
	// Readable, not writable: appending would produce records the build
	// that wrote the earlier ones could not verify, which turns a
	// rollback into a chain that reads as broken.
	dir := t.TempDir()
	newer := Record{Schema: "halite.evidence/2", Seq: 1, Kind: KindStart, Hash: "sha256:00"}
	line, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, currentName), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{NodeID: "node-1"}); err == nil {
		t.Fatal("Open extended a chain written by a newer build")
	} else if !strings.Contains(err.Error(), "halite.evidence/2") {
		t.Errorf("the refusal does not name the schema it found: %v", err)
	}
	if res := verify(t, dir); !res.OK() {
		t.Errorf("the chain should still read: %v", res.Breaks)
	}
}

func TestAnUnusableDirectoryIsRefusedAtOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a directory with no write bit, so this proves nothing as root")
	}
	dir := filepath.Join(t.TempDir(), "evidence")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dir, Options{NodeID: "node-1"})
	if err == nil {
		t.Fatal("Open accepted a directory it cannot write to")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the refusal does not name the directory: %v", err)
	}
}

func TestDigestIgnoresKeyOrderAndNotSequenceOrder(t *testing.T) {
	one := value.NewMap(2)
	one.Set("hub", "https://hub:4505")
	one.Set("file_roots", []any{"/srv/a", "/srv/b"})
	other := value.NewMap(2)
	other.Set("file_roots", []any{"/srv/a", "/srv/b"})
	other.Set("hub", "https://hub:4505")
	if Digest(one) != Digest(other) {
		t.Error("two configurations differing only in the order the keys were written digest differently")
	}

	reordered := value.NewMap(2)
	reordered.Set("hub", "https://hub:4505")
	reordered.Set("file_roots", []any{"/srv/b", "/srv/a"})
	if Digest(one) == Digest(reordered) {
		t.Error("a reordered file_roots is a different search path and digests the same")
	}
}

func TestDigestSeesAChangeInASecret(t *testing.T) {
	// The reason the digest is taken over the whole configuration rather
	// than a redacted copy: a change confined to a secret-bearing key
	// must still be a change.
	before := value.NewMap(1)
	before.Set("token", "one")
	after := value.NewMap(1)
	after.Set("token", "two")
	if Digest(before) == Digest(after) {
		t.Error("a changed token digests the same")
	}
}

func TestDigestDistinguishesTypesAndBoundaries(t *testing.T) {
	str := value.NewMap(1)
	str.Set("port", "4505")
	num := value.NewMap(1)
	num.Set("port", int64(4505))
	if Digest(str) == Digest(num) {
		t.Error("the string \"4505\" and the number 4505 digest the same")
	}

	// Length prefixes stop two adjacent values being re-cut into one.
	split := value.NewMap(2)
	split.Set("a", "xy")
	split.Set("b", "z")
	joined := value.NewMap(2)
	joined.Set("a", "x")
	joined.Set("b", "yz")
	if Digest(split) == Digest(joined) {
		t.Error("two configurations digest the same because their values can be re-cut")
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}
