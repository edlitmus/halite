package pillarstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const canary = "s3kret-canary"

func store(t *testing.T, node, key string) *Store {
	t.Helper()
	return &Store{Dir: filepath.Join(t.TempDir(), "p"), NodeID: node,
		KeyDER: func() ([]byte, error) { return []byte(key), nil }}
}

func record() Record {
	named := []string{canary}
	return Record{Pillar: json.RawMessage(`{"token":"` + canary + `"}`), Secrets: &named,
		SavedAt: time.Now().UTC().Truncate(time.Second)}
}

// The list of secrets has to survive as well as the pillar: without it a
// cached run seeds the redactor differently from a live one.
func TestARecordComesBackWithItsSecrets(t *testing.T) {
	s := store(t, "n1", "key")
	if err := s.Save("base", false, record()); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("base", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got.Pillar, []byte(canary)) || got.Secrets == nil || (*got.Secrets)[0] != canary {
		t.Errorf("record did not survive: %+v", got)
	}
}

// The whole reason this package exists rather than a JSON file.
func TestNoSecretReachesTheDisk(t *testing.T) {
	s := store(t, "n1", "key")
	if err := s.Save("base", false, record()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries %v, err %v", entries, err)
	}
	path := filepath.Join(s.Dir, entries[0].Name())
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(canary)) {
		t.Error("the pillar is on the disk in plaintext")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode %o", info.Mode().Perm())
		}
	}
}

// A file opens only for the key, node and environment it was sealed for. A
// failure must be an error: an empty pillar would render states against
// nothing and report success.
func TestAFileOpensOnlyWhereItWasSealed(t *testing.T) {
	s := store(t, "n1", "key")
	if err := s.Save("base", false, record()); err != nil {
		t.Fatal(err)
	}
	other := func(node, key string) *Store {
		return &Store{Dir: s.Dir, NodeID: node, KeyDER: func() ([]byte, error) { return []byte(key), nil }}
	}
	if _, err := other("n1", "rotated").Load("base", false); err == nil {
		t.Error("opened under another key")
	}
	if _, err := other("n2", "key").Load("base", false); err == nil {
		t.Error("opened for another node")
	}
	// Renaming one environment's file over another's must not hand a run
	// the wrong environment's secrets.
	if err := os.Rename(s.path("base", false), s.path("prod", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("prod", false); err == nil || errors.Is(err, ErrNone) {
		t.Errorf("a renamed file gave %v, want a failure to open", err)
	}
}

func TestAMissingOrClearedStoreIsErrNone(t *testing.T) {
	s := store(t, "n1", "key")
	if _, err := s.Load("base", false); !errors.Is(err, ErrNone) {
		t.Errorf("empty store: %v", err)
	}
	if err := s.Save("base", false, record()); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("base", false); !errors.Is(err, ErrNone) {
		t.Errorf("cleared store: %v", err)
	}
}
