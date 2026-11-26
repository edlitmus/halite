package builtin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/winsvc"
)

// A manual service is not enabled -- Enabled() says so, correctly -- but
// it is not disabled either, and service.disabled's whole promise is
// that the machine will not bring the service up on its own. Checking
// only Enabled() == false before decided there was nothing to do left a
// manual service manual forever (5.628).
//
// # Why this is a live test, on a service of its own
//
// It used to be an ordinary test against the real EventLog: set it to
// manual, run service.disabled, put the old start type back in a cleanup.
// That skipped on a token that was not elevated and so looked harmless, but
// on an elevated one it reconfigured the Windows Event Log on every `go test
// ./...`, and a run killed or timed out between the change and the cleanup
// left the event log disabled at the next boot (issue 878). Changing when
// something the machine came with starts is exactly the change that outlives
// a test.
//
// So the service is one this test creates, with `sc create`, and deletes.
// Its binary is cmd.exe, the conformance suite's choice for the same reason:
// it exists everywhere, and the service never needs to run, because a start
// type is set and read without starting anything. It is created `demand`,
// which is what the manager calls manual, so the module under test is the
// only thing that writes its start type.
func TestLiveServiceDisabledMovesAManualServiceToDisabled(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this create and delete a throwaway Windows service")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("creating a service needs an elevated token, and this process does not have one " +
			"(run the shell as administrator)")
	}
	name := throwawayService(t, "demand")

	before, err := winsvc.Query(name)
	if err != nil {
		t.Fatal(err)
	}
	if before.StartType != "manual" {
		t.Fatalf("the setup produced a %q service, not a manual one, so this test cannot see what it is for",
			before.StartType)
	}

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

	// Read back with sc, the operator's tool, not only with the package
	// the module wrote through: a writer and its own reader agreeing is
	// the pair this repository has been caught by before.
	if got := scStartType(t, name); !strings.Contains(got, "DISABLED") {
		t.Errorf("sc qc says %q after service.disabled, want DISABLED", got)
	}
	after, err := winsvc.Query(name)
	if err != nil {
		t.Fatal(err)
	}
	if after.StartType != "disabled" {
		t.Errorf("start type after service.disabled = %q, want %q", after.StartType, "disabled")
	}
}

// scExe is sc.exe by its full path. A bare "sc" is whatever PATH finds
// first, and from Git Bash that is not always Windows' own (issue 877).
func scExe() string {
	return filepath.Join(os.Getenv("SystemRoot"), "System32", "sc.exe")
}

// throwawayService creates a service named halitesvctest plus random hex,
// with the given sc start= value, and deletes it when the test ends. The
// cleanup asks the manager afterwards and fails the test if the service is
// still there, so a leak is reported rather than assumed away. fleet.yml's
// windows leg counts halitesvctest* services left behind.
func throwawayService(t *testing.T, start string) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	name := "halitesvctest" + hex.EncodeToString(b)
	out, err := osexec.Command(scExe(), "create", name,
		"binPath=", `C:\Windows\System32\cmd.exe /c exit 0`,
		"start=", start,
		"DisplayName=", "halite test service "+name).CombinedOutput()
	if err != nil {
		t.Fatalf("sc create %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() {
		if out, err := osexec.Command(scExe(), "delete", name).CombinedOutput(); err != nil {
			t.Errorf("sc delete %s: %v: %s", name, err, strings.TrimSpace(string(out)))
		}
		// The service has no process, so nothing holds a handle open and
		// the deletion takes effect at once; 1060 is "the specified
		// service does not exist as an installed service".
		err := osexec.Command(scExe(), "query", name).Run()
		var exit *osexec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1060 {
			t.Errorf("%s is still installed after sc delete (sc query: %v)", name, err)
		}
	})
	return name
}

// scStartType is the START_TYPE line of `sc qc`, with its spacing collapsed.
func scStartType(t *testing.T, name string) string {
	t.Helper()
	out, err := osexec.Command(scExe(), "qc", name).CombinedOutput()
	if err != nil {
		t.Fatalf("sc qc %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "START_TYPE") {
			return strings.Join(strings.Fields(line), " ")
		}
	}
	t.Fatalf("sc qc %s printed no START_TYPE line: %s", name, out)
	return ""
}
