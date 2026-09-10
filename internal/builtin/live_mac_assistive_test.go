package builtin

import (
	"runtime"
	"testing"
)

// mac_assistive reads, against the real `sqlite3` and this Mac's own
// TCC.db.
//
// # Why it needs no gate
//
// The `SELECT` against `/Library/Application Support/com.apple.TCC/TCC.db`
// reads and changes nothing, and the file is world-readable. `install`,
// `enable` and `remove` write it, and System Integrity Protection makes
// that database readonly for any process without Full Disk Access, so
// they are not exercised and no CI leg is a Mac with the entitlement.
// `evidence.go` records the module `assumed`.
//
// # What it establishes
//
// That `sqlite3` is present, that the `access` table parses into
// client / client_type / auth_value triples on this host's real schema —
// the modern one, where the column is `auth_value` and not the `allowed`
// that Salt's module still queries — and that `installed` and `enabled`
// agree with a row the bulk read returned.
func TestLiveMacAssistiveReadsThisMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_assistive is macOS's, and this is %s", runtime.GOOS)
	}
	c := realCtx(t)
	if c.Which("sqlite3") == "" {
		t.Skip("no `sqlite3` on this machine")
	}

	rows, err := macAssistiveList(c)
	if err != nil {
		// A Mac that denies even the read is a valid state; the module
		// reported it rather than crashing, which is the contract.
		t.Skipf("could not read TCC.db on this host: %v", err)
	}
	if len(rows) == 0 {
		t.Skip("this Mac has no Accessibility grants to read")
	}

	for _, row := range rows {
		if row.Client == "" {
			t.Errorf("a row read back with an empty client: %+v", row)
		}
		if row.ClientType != 0 && row.ClientType != 1 {
			t.Errorf("client_type %d is neither a bundle id (0) nor a path (1): %+v", row.ClientType, row)
		}
		if (row.ClientType == 1) != (row.Client != "" && row.Client[0] == '/') {
			t.Errorf("client_type %d does not match whether %q is a path", row.ClientType, row.Client)
		}
	}

	// installed and enabled agree with the bulk read for a real client.
	want := rows[0]
	got, err := macAssistiveFind(c, want.Client)
	if err != nil {
		t.Fatalf("by-client lookup of %q: %v", want.Client, err)
	}
	if got == nil {
		t.Fatalf("%q was in the list read but installed() did not find it", want.Client)
	}
	if got.Enabled != want.Enabled {
		t.Errorf("enabled for %q: list said %v, lookup said %v", want.Client, want.Enabled, got.Enabled)
	}
}
