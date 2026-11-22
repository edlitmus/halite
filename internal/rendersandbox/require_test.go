package rendersandbox

import (
	"errors"
	"os"
	"os/user"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/render"
)

func requiringSandbox(t *testing.T, account string, require ...string) *Sandbox {
	t.Helper()
	parsed, err := ParseRequirements(require)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Exe: os.Args[0], User: account, Require: parsed, Timeout: 30 * time.Second, Stderr: os.Stderr})
	t.Cleanup(func() { s.Close() })
	return s
}

func renderOne(s *Sandbox) error {
	_, err := s.Render([]byte("a: 1\n"), render.Options{File: "top.sls"})
	return err
}

// A misspelt requirement is refused, not dropped: dropped, it would be a
// control that reports itself as required and is not.
func TestARequirementTheSandboxDoesNotHaveIsRefused(t *testing.T) {
	if _, err := ParseRequirements([]string{"identity", "filesystem"}); err == nil ||
		!strings.Contains(err.Error(), `"filesystem"`) {
		t.Fatalf("got %v, want the unknown name refused by name", err)
	}
	got, err := ParseRequirements([]string{"network", "identity", "network"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"identity", "network"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The child measures itself: the account it reports is the one it is
// running as, read in the child, not the one the parent asked for.
func TestTheChildReportsTheAccountItRunsAs(t *testing.T) {
	s := requiringSandbox(t, "")
	o, err := s.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if o.UID != os.Getuid() || o.GID != os.Getgid() {
		t.Errorf("the child reported uid %d gid %d; it runs as this process's %d %d", o.UID, o.GID, os.Getuid(), os.Getgid())
	}
}

// Required and not in force: the render is refused, and says what was
// seen. A node not running as root cannot drop to another account, so
// requiring one there refuses every render rather than rendering as the
// node's own account under a setting that says otherwise.
func TestARequiredIdentityThatIsNotInForceRefusesTheRender(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can drop to the account, so the requirement would be met; this is the unprivileged case")
	}
	t.Run("an account this process cannot drop to", func(t *testing.T) {
		err := renderOne(requiringSandbox(t, "nobody", RequireIdentity))
		if !errors.Is(err, ErrUnmet) {
			t.Fatalf("got %v, want the render refused as unmet", err)
		}
		want := []string{"rather than as nobody", "cannot drop privilege"}
		if runtime.GOOS == "windows" {
			want = []string{windowsHasNoAccount}
		}
		if !containsAll(err.Error(), want) {
			t.Errorf("the refusal does not say what was seen and why: %v", err)
		}
	})
	t.Run("no account at all", func(t *testing.T) {
		err := renderOne(requiringSandbox(t, "", RequireIdentity))
		if !errors.Is(err, ErrUnmet) || !strings.Contains(err.Error(), "no render_sandbox_user") {
			t.Fatalf("got %v, want a refusal naming the missing account", err)
		}
	})
}

// Met without root: the account required is the one this process is
// already, so the child, measured, runs as it and the render goes ahead.
// Without this the tests above would pass for a gate that refused
// everything.
//
// Windows has no account the child can be dropped to (a restricted token
// is the answer there, and is not built), so the requirement cannot be
// met even by naming the account the node already is; there the test
// holds the opposite, that it is refused for that reason rather than met
// by coincidence.
func TestARequiredIdentityThatIsInForceRenders(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skipf("this machine cannot name the account the test runs as: %v", err)
	}
	err = renderOne(requiringSandbox(t, me.Username, RequireIdentity))
	if runtime.GOOS == "windows" {
		if !errors.Is(err, ErrUnmet) || !strings.Contains(err.Error(), windowsHasNoAccount) {
			t.Fatalf("got %v, want the render refused because this platform has no account to drop to", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("the child runs as %s, which is what was required, and the render was refused: %v", me.Username, err)
	}
}

// A required network denial is checked against the interfaces the child
// sees, so on a machine where the child shares the host's network the
// render is refused and the refusal names an interface that is up.
func TestARequiredNetworkDenialThatIsNotInForceRefusesTheRender(t *testing.T) {
	s := requiringSandbox(t, "", RequireNetwork)
	o, err := s.Observe()
	if err != nil {
		t.Fatal(err)
	}
	if len(o.UpInterfaces) == 0 && o.NetworkError == "" {
		t.Skip("the child sees no interface up, so the requirement is met here; the live test covers that case")
	}
	err = renderOne(s)
	if !errors.Is(err, ErrUnmet) {
		t.Fatalf("got %v, want the render refused as unmet", err)
	}
	if o.NetworkError == "" && !strings.Contains(err.Error(), o.UpInterfaces[0]) {
		t.Errorf("the refusal does not name the interface the child saw up (%v): %v", o.UpInterfaces, err)
	}
}

// windowsHasNoAccount is the reason confine_other.go gives.
const windowsHasNoAccount = "this platform has no account the render child can be dropped to"

func containsAll(s string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
