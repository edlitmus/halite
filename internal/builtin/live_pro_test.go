package builtin

import (
	"os"
	"runtime"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `pro`, read against the real Ubuntu Pro client on the machine running
// the tests. No root and no gate: `pro status`, `pro api` and `pro
// --version` need neither, the way `modprobe.list`/`udev.list` do not.
//
// It asserts shape rather than this host's particular subscription --
// whether a runner is attached is a fact about that runner, not about
// the module -- so the same test passes on an attached development host
// and skips cleanly on a bare CI image with the client installed but
// never attached.
func TestLiveProReadsTheRealClient(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("pro is Ubuntu; this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	if c.Which("pro") == "" {
		t.Skip("this host has no pro (ubuntu-advantage-tools)")
	}
	r := New()

	version, err := r.Exec.Call(c, "pro.version", value.NewMap(0))
	if err != nil {
		t.Fatalf("pro.version: %v", err)
	}
	if v, _ := version.(string); v == "" {
		t.Errorf("pro.version = %#v, want a non-empty version string", version)
	}

	attached, err := r.Exec.Call(c, "pro.is_attached", value.NewMap(0))
	if err != nil {
		t.Fatalf("pro.is_attached: %v", err)
	}
	isAttached, ok := attached.(bool)
	if !ok {
		t.Fatalf("pro.is_attached = %#v, want a bool", attached)
	}

	raw, err := r.Exec.Call(c, "pro.status", value.NewMap(0))
	if err != nil {
		t.Fatalf("pro.status: %v", err)
	}
	status, ok := raw.(*value.Map)
	if !ok {
		t.Fatalf("pro.status = %T, want *value.Map", raw)
	}
	statusAttached, _ := status.Get("attached")
	if statusAttached != isAttached {
		t.Errorf("pro.status's attached = %#v disagrees with pro.is_attached = %v; "+
			"two ways of asking the same client gave different answers", statusAttached, isAttached)
	}

	services, _ := status.Get("services")
	list, ok := services.([]any)
	if !ok {
		t.Fatalf("pro.status's services = %#v, want a list", services)
	}
	if isAttached && len(list) == 0 {
		t.Error("an attached host reported no services at all")
	}
	for _, svc := range list {
		m, ok := svc.(*value.Map)
		if !ok {
			t.Fatalf("a service entry was %T, not *value.Map", svc)
		}
		if name, _ := m.GetString("name"); name == "" {
			t.Errorf("a service entry had no name: %#v", m)
		}
		if status, _ := m.GetString("status"); status == "" {
			t.Errorf("a service entry had no status: %#v", m)
		}
	}
}

// proServiceStatus reads one service's status out of a real pro.status
// call, for the mutating test below to know which direction to toggle
// and to confirm it actually took.
func proServiceStatus(t *testing.T, r *Registries, c *exec.Context, name string) string {
	t.Helper()
	raw, err := r.Exec.Call(c, "pro.status", value.NewMap(0))
	if err != nil {
		t.Fatalf("pro.status: %v", err)
	}
	status := raw.(*value.Map)
	services, _ := status.Get("services")
	for _, svc := range services.([]any) {
		m := svc.(*value.Map)
		if n, _ := m.GetString("name"); n == name {
			s, _ := m.GetString("status")
			return s.(string)
		}
	}
	t.Fatalf("this host's pro status names no %q service", name)
	return ""
}

// TestLiveProEnableAndDisable drives `pro.enable` and `pro.disable`
// against the real client, toggling `usg` (Ubuntu Security Guide) and
// back -- entitled on every Pro subscription, an audit/hardening tool
// rather than a security control, so leaving it in either state when
// the test fails costs nothing. Gated on root and HALITE_SYSTEM_LIVE:
// both functions change what this real host has installed and
// configured, which is not a thing to do by accident.
//
// State-agnostic on purpose: it reads which way `usg` already is and
// toggles away and back, rather than assuming a starting state, so the
// host is left exactly as it was found whichever way the test began.
func TestLiveProEnableAndDisable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("pro is Ubuntu; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this enable and disable a real Pro service")
	}
	if os.Geteuid() != 0 {
		t.Skip("pro enable/disable needs root")
	}
	c := &exec.Context{}
	if c.Which("pro") == "" {
		t.Skip("this host has no pro (ubuntu-advantage-tools)")
	}
	r := New()
	const service = "usg"

	if attached, err := r.Exec.Call(c, "pro.is_attached", value.NewMap(0)); err != nil || attached != true {
		t.Skipf("this host is not Pro-attached (is_attached=%v, err=%v)", attached, err)
	}

	before := proServiceStatus(t, r, c, service)
	enable := before != "enabled"
	toggle := func(fn string) {
		out, err := r.Exec.Call(c, fn, value.MapOf("services", []any{service}))
		if err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		doc, ok := out.(*value.Map)
		if !ok {
			t.Fatalf("%s returned %T, want *value.Map", fn, out)
		}
		if result, _ := doc.GetString("result"); result != "success" {
			t.Fatalf("%s: real client answered %#v", fn, doc)
		}
	}
	restore := func() {
		if enable {
			toggle("pro.disable")
		} else {
			toggle("pro.enable")
		}
		if got := proServiceStatus(t, r, c, service); got != before {
			t.Errorf("restoring %s left it %q, want back to %q", service, got, before)
		}
	}
	defer restore()

	if enable {
		toggle("pro.enable")
	} else {
		toggle("pro.disable")
	}
	want := "disabled"
	if enable {
		want = "enabled"
	}
	if got := proServiceStatus(t, r, c, service); got != want {
		t.Fatalf("after toggling, %s is %q, want %q", service, got, want)
	}
}
