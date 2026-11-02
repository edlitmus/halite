package hub

import (
	"os"
	"path/filepath"
	"testing"
)

// An orchestration record the hub cannot read is listed as one it cannot read.
//
// `OrchStore.List` said in a comment that an unreadable record must not be
// silently dropped and then dropped it: a truncated file, left by a full disk
// or a crash mid-write on a filesystem that does not make the rename atomic,
// was absent from `orch list`, and an operator looking for the run was told
// there was none. DIVERGENCE 5.526.
func TestAnUnreadableOrchRecordIsListedAsUnreadable(t *testing.T) {
	l := orchLab(t, map[string]string{"x.sls": "a:\n  salt.runner:\n    - name: manage.status\n"})
	store := l.server.orchStore()
	good := l.server.clock().Next()
	if err := store.Put(&OrchRun{JID: good, Principal: "cert:CN=ed", State: OrchComplete}); err != nil {
		t.Fatal(err)
	}
	bad := l.server.clock().Next()
	if err := os.WriteFile(filepath.Join(store.dir, string(bad)+".json"), []byte(`{"jid": "trunc`), 0o600); err != nil {
		t.Fatal(err)
	}

	runs, unreadable, err := store.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].JID != good {
		t.Errorf("the readable run was not listed: %v", runs)
	}
	if len(unreadable) != 1 || unreadable[0].JID != bad {
		t.Fatalf("the truncated record was not reported: %v", unreadable)
	}

	listed := mapOf(t, returned(t, call(t, l.operator(t, "ed"), "state.orch_list")))
	entry, ok := listed.Get(string(bad))
	if !ok {
		t.Fatalf("`orch list` has no entry for the record it cannot read: %v", listed.StringKeys())
	}
	if state, _ := mapOf(t, entry).Get("state"); state != "unreadable" {
		t.Errorf("the unreadable record is listed in state %v", state)
	}
	if _, ok := listed.Get(string(good)); !ok {
		t.Error("an unreadable record hid the readable one")
	}
}

// An unreadable record counts against the limit, because it is one of the most
// recent records; and a caller scoped to its own runs is not told that a record
// it cannot be shown to own exists.
func TestAnUnreadableOrchRecordIsCountedAndIsNotShownToAScopedCaller(t *testing.T) {
	l := orchLab(t, map[string]string{"x.sls": "a:\n  salt.runner:\n    - name: manage.status\n"})
	store := l.server.orchStore()
	for i := 0; i < 3; i++ {
		if err := store.Put(&OrchRun{JID: l.server.clock().Next(), State: OrchComplete}); err != nil {
			t.Fatal(err)
		}
	}
	bad := l.server.clock().Next()
	if err := os.WriteFile(filepath.Join(store.dir, string(bad)+".json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}

	runs, unreadable, err := store.List(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs)+len(unreadable) != 2 || len(unreadable) != 1 || unreadable[0].JID != bad {
		t.Errorf("limit 2 returned %d runs and %d unreadable", len(runs), len(unreadable))
	}

	scoped := &RunnerContext{Server: l.server, Principal: "cert:CN=alice", Scope: l.server.Policy.RunnerScope("cert:CN=nobody", "state.orch_list", false)}
	_, bad2, err := scoped.visibleRuns(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad2) != 0 {
		t.Errorf("a scoped caller was told of %d records it cannot be shown to own", len(bad2))
	}
}
