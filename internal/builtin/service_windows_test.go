package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/winsvc"
)

// A manual service is not enabled -- Enabled() says so, correctly -- but
// it is not disabled either, and service.disabled's whole promise is
// that the machine will not bring the service up on its own. Checking
// only Enabled() == false before decided there was nothing to do left a
// manual service manual forever.
func TestServiceDisabledMovesAManualServiceToDisabled(t *testing.T) {
	const name = "EventLog"

	before, err := winsvc.Query(name)
	if err != nil {
		t.Fatal(err)
	}
	restore := before.StartType
	if err := winsvc.SetStartType(name, "manual"); err != nil {
		if strings.Contains(err.Error(), "administrator") {
			t.Skip("changing a service's start type needs administrator rights, and this process does not have them")
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := winsvc.SetStartType(name, restore); err != nil {
			t.Errorf("could not restore %s's start type to %s: %v", name, restore, err)
		}
	})

	r := New()
	res, err := r.States.Call(realCtx(t), "service.disabled", value.MapOf("name", name))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Errorf("service.disabled on a manual service did not report success: %+v", res)
	}
	if res.Changes.Len() == 0 {
		t.Error("service.disabled on a manual service reported no change; a manual service is not enabled, but it is not disabled either")
	}

	after, err := winsvc.Query(name)
	if err != nil {
		t.Fatal(err)
	}
	if after.StartType != "disabled" {
		t.Errorf("start type after service.disabled = %q, want %q", after.StartType, "disabled")
	}
}
