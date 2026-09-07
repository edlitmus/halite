package job

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SPEC 31's Upgrade row: "state and job cache format migration".
// upgrade:cache-format
//
// The row asks what happens to the records when the binary changes
// version, and the answer used to be that nobody had asked. A record
// round-trips through `Job`, and `encoding/json` drops every field the
// struct does not have — so a hub reading, changing and writing back a
// record written by a *newer* hub destroyed whatever the newer version
// had added, silently, with nothing in the record even saying what had
// written it.
//
// That is the rollback case. It is not exotic on a fleet built from
// source: going back a tag is one `git checkout` and one `make install`,
// and there is no reason to expect it to be lossy.

// A record from before the schema field existed reads and writes.
//
// This is the ordinary upgrade direction and the one that must not
// break: every record already on every hub was written without a schema,
// and a build that refused them would refuse its own history.
func TestARecordFromBeforeTheSchemaFieldIsAcceptedAndStamped(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "web1.example")

	// Strip the marker, as a record written by an older halite has it.
	path := recordPath(t, c, j.JID)
	strip(t, path, "schema")
	if got := rawField(t, path, "schema"); got != nil {
		t.Fatalf("the marker survived being stripped: %v", got)
	}

	updated, err := c.Update(j.JID, func(cur *Job) error {
		if cur.Schema != "" {
			t.Errorf("an old record read with schema %q, want empty", cur.Schema)
		}
		cur.State = Complete
		return nil
	})
	if err != nil {
		t.Fatalf("a record from before the schema field was refused: %v", err)
	}
	if updated.Schema != JobSchema {
		t.Errorf("it was not stamped on write: schema is %q", updated.Schema)
	}
	if got := rawField(t, path, "schema"); got != JobSchema {
		t.Errorf("the file says %v, want %q", got, JobSchema)
	}
}

// A record written by a newer halite is readable and is not written
// back.
//
// Readable, because `jobs list` on a rolled-back hub should keep
// working and an operator looking at a job needs to see it. Not
// writable, because writing it truncates it — and the truncation is
// exactly what an operator would not find out about.
func TestARecordFromANewerHaliteIsReadButNotWritten(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "web1.example")
	path := recordPath(t, c, j.JID)

	// What a newer halite left behind: a schema this build does not
	// know, and two fields it has never heard of.
	setFields(t, path, map[string]any{
		"schema":        "halite.job/2",
		"policy_digest": "sha256:deadbeef",
		"future_thing":  []any{"a", "b"},
	})

	// Reading works, and reports the foreign schema rather than hiding
	// it.
	got, err := c.Get(j.JID)
	if err != nil {
		t.Fatalf("a newer record could not be read: %v", err)
	}
	if got.Schema != "halite.job/2" {
		t.Errorf("the schema read as %q", got.Schema)
	}
	if got.Fun != "test.ping" {
		t.Errorf("the fields this build does know did not survive the read: %+v", got)
	}

	// Writing is refused, by both doors.
	if err := c.Put(got); !errors.Is(err, ErrForeignRecord) {
		t.Errorf("Put accepted a newer record: %v", err)
	}
	called := false
	if _, err := c.Update(j.JID, func(*Job) error {
		called = true
		return nil
	}); !errors.Is(err, ErrForeignRecord) {
		t.Errorf("Update accepted a newer record: %v", err)
	}
	if called {
		t.Error("the mutator ran before the refusal; a caller with a side effect in it " +
			"would have had it and been told the write failed")
	}

	// And the file is untouched, which is the whole point.
	for _, field := range []string{"policy_digest", "future_thing"} {
		if rawField(t, path, field) == nil {
			t.Errorf("%s was dropped anyway; the refusal did not protect the record", field)
		}
	}
	if s := rawField(t, path, "schema"); s != "halite.job/2" {
		t.Errorf("the schema was overwritten with %v", s)
	}
}

// The refusal says which record, which schema, and what to do.
//
// "this record was written by a newer halite" without the version is an
// operator reading release notes to work out which one.
func TestTheForeignRecordRefusalNamesEverythingNeeded(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now())
	setFields(t, recordPath(t, c, j.JID), map[string]any{"schema": "halite.job/2"})

	_, err := c.Update(j.JID, func(*Job) error { return nil })
	if err == nil {
		t.Fatal("a newer record was accepted")
	}
	var foreign *ForeignRecordError
	if !errors.As(err, &foreign) {
		t.Fatalf("the error carries no detail: %v", err)
	}
	if foreign.JID != j.JID || foreign.Schema != "halite.job/2" || foreign.Known != JobSchema {
		t.Errorf("the error is missing something: %+v", foreign)
	}
	for _, want := range []string{string(j.JID), "halite.job/2", JobSchema, "read"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not mention %q: %v", want, err)
		}
	}
}

// Without the marker, this is what happened. Kept as the reason the
// marker exists.
//
// It writes a record the way a newer halite would, round-trips it
// through this build's struct without the schema check, and counts the
// keys. If this ever stops losing them — because `Job` grew an
// unknown-field remainder, say — this test is where to say so.
func TestARoundTripDropsWhatTheStructDoesNotKnow(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "web1.example")
	path := recordPath(t, c, j.JID)
	setFields(t, path, map[string]any{
		"policy_digest": "sha256:deadbeef",
		"future_thing":  []any{"a", "b"},
	})
	before := len(rawMap(t, path))

	// The round trip itself, with no schema on the record so the check
	// does not fire: read into the struct, write it back.
	read, err := c.Get(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Put(read); err != nil {
		t.Fatal(err)
	}
	after := rawMap(t, path)

	if _, ok := after["policy_digest"]; ok {
		t.Skip("a round trip now preserves unknown fields. That is better than refusing " +
			"a foreign record, and ErrForeignRecord's reasoning needs rewriting.")
	}
	t.Logf("a round trip took %d keys to %d: %v dropped", before, len(after),
		before-len(after))
	if before <= len(after) {
		t.Errorf("this test establishes nothing: %d keys in, %d out", before, len(after))
	}
}

// --- helpers ---

func recordPath(t *testing.T, c *Cache, id ID) string {
	t.Helper()
	dir, err := c.jobDir(id)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "job.json")
}

func rawMap(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func rawField(t *testing.T, path, field string) any {
	t.Helper()
	return rawMap(t, path)[field]
}

func setFields(t *testing.T, path string, fields map[string]any) {
	t.Helper()
	m := rawMap(t, path)
	for k, v := range fields {
		m[k] = v
	}
	writeRaw(t, path, m)
}

func strip(t *testing.T, path string, fields ...string) {
	t.Helper()
	m := rawMap(t, path)
	for _, f := range fields {
		delete(m, f)
	}
	writeRaw(t, path, m)
}

func writeRaw(t *testing.T, path string, m map[string]any) {
	t.Helper()
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
