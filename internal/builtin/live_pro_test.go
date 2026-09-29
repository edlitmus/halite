package builtin

import (
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
