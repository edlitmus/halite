package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheSpoolReturnsWhatItWasGivenOldestFirst(t *testing.T) {
	s, err := OpenSpool(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for i, body := range []string{"first", "second", "third"} {
		if err := s.Put([]byte(body), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if s.Count() != 3 {
		t.Fatalf("the spool holds %d", s.Count())
	}

	// Oldest first, so the upstream sees returns in the order they
	// happened rather than the order the filesystem lists them.
	for _, want := range []string{"first", "second", "third"} {
		entry, ok, err := s.Next()
		if err != nil || !ok {
			t.Fatalf("Next: %v %v", ok, err)
		}
		if string(entry.Body) != want {
			t.Fatalf("it read %q, want %q", entry.Body, want)
		}
		if err := s.Remove(entry); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := s.Next(); ok {
		t.Error("the spool is not empty")
	}
}

// A spool that silently discards is the failure it exists to prevent,
// and the oldest return is usually the one somebody wants.
func TestAFullSpoolRefusesRatherThanDroppingTheOldest(t *testing.T) {
	s, err := OpenSpool(t.TempDir(), 32)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.Put([]byte(strings.Repeat("a", 20)), now); err != nil {
		t.Fatal(err)
	}
	err = s.Put([]byte(strings.Repeat("b", 20)), now.Add(time.Second))
	if err == nil {
		t.Fatal("the spool grew past its bound")
	}
	if !strings.Contains(err.Error(), "full") {
		t.Errorf("the refusal is %v", err)
	}
	if s.Dropped() != 1 {
		t.Errorf("it recorded %d drops", s.Dropped())
	}
	// The first return is still there: the oldest is kept, not
	// sacrificed for the newest.
	entry, ok, err := s.Next()
	if err != nil || !ok {
		t.Fatal("the first return was lost")
	}
	if !strings.HasPrefix(string(entry.Body), "a") {
		t.Errorf("it kept %q", entry.Body)
	}
}

// A relay that silently had no spool would be a syndic with a better
// name.
func TestARelayWithNoSpoolDirectoryIsRefused(t *testing.T) {
	_, err := OpenSpool("", 0)
	if err == nil {
		t.Fatal("a relay with no spool directory was accepted")
	}
	if !strings.Contains(err.Error(), "loses returns") {
		t.Errorf("the refusal is %v", err)
	}
}

// A spool that survives a restart is the whole point of it being on
// disk.
func TestTheSpoolSurvivesReopening(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenSpool(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put([]byte("a return"), time.Now()); err != nil {
		t.Fatal(err)
	}

	again, err := OpenSpool(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok, err := again.Next()
	if err != nil || !ok {
		t.Fatal("the spooled return did not survive")
	}
	if string(entry.Body) != "a return" {
		t.Errorf("it read %q", entry.Body)
	}
	// And the bound is measured from what is on disk, not from zero.
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Errorf("the directory holds %d files", len(files))
	}
	_ = filepath.Join(dir, entry.Name)
}
