package hub

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestOrchStoreReadsARecordOpenForReplacement(t *testing.T) {
	store, err := OpenOrchStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := &OrchRun{JID: "20261123T000000123456", State: OrchRunning}
	if err := store.Put(run); err != nil {
		t.Fatal(err)
	}
	path, err := store.path(run.JID)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}

	// Keep delete access open for the entire read. This models the access
	// Windows needs for replacement without depending on when a rename runs.
	handle, err := windows.CreateFile(name, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)

	got, err := store.Get(run.JID)
	if err != nil {
		t.Fatalf("read orchestration %s with delete access open: %v", run.JID, err)
	}
	if got.JID != run.JID || got.State != run.State {
		t.Fatalf("read orchestration %s in state %s, want %s in state %s", got.JID, got.State, run.JID, run.State)
	}
}
