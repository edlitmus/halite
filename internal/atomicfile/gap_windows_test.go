package atomicfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Issue 890, DIVERGENCE 5.751. On a loaded Windows machine, a reader's open
// can be told the file does not exist while it is being replaced, for up to
// 50 ms. The machine cannot be loaded on demand, so these tests make the gap
// with setRenameInfo: the destination is removed, the test is told, and only
// after holdGap does the real rename put the new file there. That is the gap
// as the loaded machine showed it, and one of this process's own replaces
// explains it.
const holdGap = 300 * time.Millisecond

// hideDuringReplace makes every replace of path leave it missing for
// holdGap first. The returned channel receives once per replace, when the
// file is gone.
func hideDuringReplace(t *testing.T, path string) <-chan struct{} {
	t.Helper()
	gone := make(chan struct{}, 16)
	saved := setRenameInfo
	setRenameInfo = func(h windows.Handle, class uint32, buf *byte, size uint32) error {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		gone <- struct{}{}
		time.Sleep(holdGap)
		return saved(h, class, buf, size)
	}
	t.Cleanup(func() { setRenameInfo = saved })
	return gone
}

// writeInBackground replaces path with data and returns where its error
// will arrive.
func writeInBackground(path, data string) <-chan error {
	done := make(chan error, 1)
	go func() { done <- Write(path, []byte(data), 0o600) }()
	return done
}

func TestAReadInTheGapOfThisProcesssReplaceGetsTheNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.json")
	if err := Write(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := hideDuringReplace(t, path)
	written := writeInBackground(path, "new")
	<-gone

	got, err := Read(path)

	if err != nil {
		t.Fatalf("a read while this process was replacing the file failed: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("read %q, want the replacement %q", got, "new")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

func TestAnOpenInTheGapOfThisProcesssReplaceGetsTheNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "object")
	if err := Write(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := hideDuringReplace(t, path)
	written := writeInBackground(path, "new")
	<-gone

	f, err := Open(path)

	if err != nil {
		t.Fatalf("an open while this process was replacing the file failed: %v", err)
	}
	got, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("read %q, want the replacement %q", got, "new")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

// The other half of the bargain: a file that is simply not there is not
// waited for, even while this process is in the gap of a replace of a
// different file. An unknown jid is the hub's commonest not-found, and it
// must not pay for somebody else's write.
func TestAFileThatIsNotThereIsReportedAtOnceDuringAnotherFilesReplace(t *testing.T) {
	dir := t.TempDir()
	busy := filepath.Join(dir, "busy.json")
	if err := Write(busy, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := hideDuringReplace(t, busy)
	written := writeInBackground(busy, "new")
	<-gone

	started := time.Now()
	_, err := Read(filepath.Join(dir, "absent.json"))
	took := time.Since(started)

	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("reading an absent file: %v, want not-found", err)
	}
	if took >= holdGap/2 {
		t.Errorf("an absent file took %v to report, as though it waited for another file's replace", took)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

// The reader that most needs the record is the one whose open fell in the
// gap of a replace that finished before it asked. The first version of this
// deleted a path's entry when its replace ended, and told exactly that
// reader there had been no replace.
func TestAReplaceThatFinishedDuringTheReadStillExplainsItsNotFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.json")
	readBegan := markRead()

	end := beginReplace(path)
	end()

	if !replaceCanExplain(path, readBegan) {
		t.Error("a replace that began and ended during the read was not counted")
	}
	if replaceCanExplain(path, markRead()) {
		t.Error("a replace that ended before the read began was counted")
	}
	if replaceCanExplain(filepath.Join(filepath.Dir(path), "other.json"), readBegan) {
		t.Error("a replace of another path was counted")
	}
}

// NTFS names are case-insensitive and a path may arrive relative, so the
// writer's and the reader's spellings of one file must meet.
func TestTwoSpellingsOfOnePathAreOneReplace(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	readBegan := markRead()

	end := beginReplace(filepath.Join(dir, "Job.JSON"))
	defer end()

	if !replaceCanExplain("job.json", readBegan) {
		t.Error("a relative, differently cased spelling did not match the replace in flight")
	}
}
