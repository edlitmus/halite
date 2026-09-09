package builtin

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `mac_defaults`, driven against the real `defaults(1)`.
//
// # What it touches
//
// A domain named `com.halite.selftest.<pid>`, in the invoking user's own
// preference store. Nothing else on the machine has that domain, the
// test writes and reads only there, and a cleanup removes it whether the
// body passed or not. No root, because a user domain does not need it —
// which is also the only path CI could ever run, since a Mac runner has
// no throwaway system domains.
//
// # Why it skips without the gate
//
// `HALITE_SYSTEM_LIVE=1`, the same switch the hostname and sysctl live
// tests use. It writes to a real preferences store, even if a private
// corner of one, and `go test ./...` on a Mac somebody is working on
// should not do that without being asked.
//
// # What it establishes
//
// That the plist reader agrees with what `defaults` actually exports for
// each scalar type and for a nested structure, that a second `write`
// with the same value is a no-op the state reports as converged, that a
// type change is seen as a change, and that `absent` removes a key.

func macDefaultsLive(t *testing.T) (*exec.Context, string) {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to run mac_defaults against the real `defaults`")
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_defaults is macOS's, and this is %s", runtime.GOOS)
	}
	c := realCtx(t)
	if c.Which("defaults") == "" {
		t.Fatal("HALITE_SYSTEM_LIVE is set and there is no `defaults`; this is not a Mac")
	}
	domain := fmt.Sprintf("com.halite.selftest.%d", os.Getpid())
	t.Cleanup(func() {
		_ = macDefaultsDelete(c, domain, "", "")
	})
	return c, domain
}

func TestLiveMacDefaultsReaderAgreesWithDefaults(t *testing.T) {
	c, domain := macDefaultsLive(t)

	type wrote struct {
		key, vtype  string
		value, want any
	}
	writes := []wrote{
		{"AString", "string", "hello world", "hello world"},
		{"AnInt", "int", int64(48), int64(48)},
		{"AFloat", "float", 64.5, 64.5},
		{"ABoolTrue", "bool", true, true},
		{"ABoolFalse", "bool", false, false},
		{"AnArray", "array", []any{"a", "b", "c"}, []any{"a", "b", "c"}},
		{"ADict", "dict", value.MapOf("locked", true, "n", int64(2)),
			value.MapOf("locked", true, "n", int64(2))},
	}

	for _, w := range writes {
		if err := macDefaultsWrite(c, domain, w.key, w.vtype, w.value, ""); err != nil {
			t.Fatalf("write %s: %v", w.key, err)
		}
	}

	dict, err := macDefaultsExport(c, domain, "")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, w := range writes {
		got, ok := dict.Get(w.key)
		if !ok {
			t.Errorf("%s: not in the exported domain", w.key)
			continue
		}
		if !macDefaultsEqual(got, w.want) {
			t.Errorf("%s: read back %#v, want %#v", w.key, got, w.want)
		}
	}
}

func TestLiveMacDefaultsStateConverges(t *testing.T) {
	c, domain := macDefaultsLive(t)

	args := value.MapOf("domain", domain, "key", "orientation", "value", "left", "vtype", "string")

	first, err := macDefaultsWriteState(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Succeeded() || !first.HasChanges() {
		t.Fatalf("first write did not report a change: %+v", first)
	}

	second, err := macDefaultsWriteState(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Succeeded() || second.HasChanges() {
		t.Fatalf("second write was not a no-op: %+v", second)
	}

	// A type change on the same key is a change.
	retyped := value.MapOf("domain", domain, "key", "orientation", "value", "1", "vtype", "int")
	switched, err := macDefaultsWriteState(c, retyped)
	if err != nil {
		t.Fatal(err)
	}
	if !switched.HasChanges() {
		t.Errorf("changing string->int on a key was seen as converged: %+v", switched)
	}

	// absent removes it, and is then converged.
	gone := value.MapOf("domain", domain, "key", "orientation")
	if r, err := macDefaultsAbsentState(c, gone); err != nil || !r.HasChanges() {
		t.Fatalf("absent did not remove the key: %+v (%v)", r, err)
	}
	if r, err := macDefaultsAbsentState(c, gone); err != nil || r.HasChanges() {
		t.Fatalf("absent on an already-gone key was not a no-op: %+v (%v)", r, err)
	}
}
