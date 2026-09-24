package builtin

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The `timezone` module on a real Mac.
//
// # Why this exists
//
// The darwin branch of `setZone` drives `systemsetup -settimezone`, and
// until this file nothing had ever run it: the only test that reached it
// mocked `systemsetup` and then moved /etc/localtime itself, so it
// demonstrated the fixture (plan.md §1.3). The reading half had a worse
// gap that a mock could not have found either: `list_zones` walked
// /usr/share/zoneinfo, which on macOS is a link, and returned nothing --
// so on every Mac `timezone.list_zones` failed and `timezone.system`
// quietly dropped its refusal of a zone the node does not have
// (DIVERGENCE 5.131).
//
// # What the two tests need
//
// The first only reads, needs nothing, and runs wherever the suite runs
// on a Mac. The second changes the machine's time zone and puts it back,
// so it wants root and `HALITE_SYSTEM_LIVE=1`:
//
//	sudo HALITE_SYSTEM_LIVE=1 go test -run TestLiveMacTimezone -v ./internal/builtin/
//
// The restore goes through `systemsetup` directly rather than through the
// module, so a module that cannot set a zone still cannot leave the
// machine in the wrong one.

// macZoneFromLink is the zone /etc/localtime points at, read without the
// module, so that the module's reader is checked against the file the C
// library reads rather than against itself.
func macZoneFromLink(t *testing.T) string {
	t.Helper()
	zone, err := macZoneLink()
	if err != nil {
		t.Fatal(err)
	}
	return zone
}

func macZoneLink() (string, error) {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return "", fmt.Errorf("/etc/localtime is not a link on this Mac: %v", err)
	}
	i := strings.LastIndex(target, "/zoneinfo/")
	if i < 0 {
		return "", fmt.Errorf("/etc/localtime points at %s, which is not under a zoneinfo tree", target)
	}
	return target[i+len("/zoneinfo/"):], nil
}

// macZoneLinkWithin polls for the link to name want, and says how long
// it took. The first run on the `macos` leg found /etc/localtime missing
// straight after `systemsetup -settimezone` had exited 0, so whether the
// tool replaces the link synchronously is a measurement, not an
// assumption, and a state whose next run reads the link depends on it.
func macZoneLinkWithin(t *testing.T, want string, d time.Duration) (string, error) {
	t.Helper()
	start := time.Now()
	var zone string
	var err error
	for {
		zone, err = macZoneLink()
		if err == nil && zone == want {
			t.Logf("/etc/localtime named %s %v after the call returned", want, time.Since(start).Round(time.Millisecond))
			return zone, nil
		}
		if time.Since(start) > d {
			return zone, err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestLiveMacTimezoneReadsThisMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("this is the macOS branch, and this is %s", runtime.GOOS)
	}
	c := realCtx(t)

	have, err := currentZone(c)
	if err != nil {
		t.Fatalf("get_zone: %v", err)
	}
	if link := macZoneFromLink(t); have != link {
		t.Errorf("get_zone says %q and /etc/localtime points at %q", have, link)
	}

	zones, err := listZones(c)
	if err != nil {
		t.Fatalf("list_zones on a Mac with a tz database: %v", err)
	}
	// A real tz database has several hundred zones; a walk that stopped
	// at a link has none, and one that wandered into the alternate trees
	// has thousands with `posix/` in front.
	if len(zones) < 300 {
		t.Errorf("list_zones returned %d zones; a real tz database has several hundred", len(zones))
	}
	if !knownZone(zones, have) {
		t.Errorf("list_zones does not include %q, the zone this Mac is running in", have)
	}
	for _, z := range zones {
		if strings.HasPrefix(z, "posix/") || strings.HasPrefix(z, "right/") || strings.Contains(z, ".") {
			t.Errorf("list_zones returned %q, which is not a zone name", z)
			break
		}
	}
}

func TestLiveMacTimezoneSetsTheZoneAndPutsItBack(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to change this Mac's time zone")
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("this is the macOS branch, and this is %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		t.Skip("systemsetup -settimezone needs root; run it under sudo")
	}
	c := realCtx(t)
	if c.Which("systemsetup") == "" {
		t.Fatal("HALITE_SYSTEM_LIVE is set and there is no `systemsetup`; this is not a Mac")
	}

	// Captured as the link target and put back as the link target, not
	// through `systemsetup`: the first run on the `macos` leg found the
	// runner in UTC, and `systemsetup -settimezone UTC` refuses with
	// "UTC is not a valid timezone", so a restore through the tool left
	// the machine in the zone the test had moved it to.
	originalTarget, err := os.Readlink("/etc/localtime")
	if err != nil {
		t.Fatalf("/etc/localtime is not a link on this Mac: %v", err)
	}
	original := macZoneFromLink(t)
	t.Logf("this Mac is in %s (%s)", original, originalTarget)
	macLogTool(t, "systemsetup", "-gettimezone")
	if out, err := exec.Command("systemsetup", "-listtimezones").CombinedOutput(); err == nil {
		names := strings.Fields(string(out))
		has := func(z string) bool {
			for _, n := range names {
				if n == z {
					return true
				}
			}
			return false
		}
		t.Logf("systemsetup -listtimezones: %d names; UTC=%v GMT=%v US/Pacific=%v Pacific/Chatham=%v",
			len(names), has("UTC"), has("GMT"), has("US/Pacific"), has("Pacific/Chatham"))
	}
	t.Cleanup(func() {
		tmp := "/etc/localtime.halite-restore"
		_ = os.Remove(tmp)
		if err := os.Symlink(originalTarget, tmp); err != nil {
			t.Errorf("RESTORE FAILED: %v", err)
			return
		}
		if err := os.Rename(tmp, "/etc/localtime"); err != nil {
			t.Errorf("RESTORE FAILED: %v", err)
			return
		}
		if now := macZoneFromLink(t); now != original {
			t.Errorf("RESTORE FAILED: the Mac is in %s, not %s", now, original)
		}
	})

	// Two zones far from any runner's, so that neither is the one the
	// machine is already in.
	want := "Pacific/Chatham"
	if original == want {
		want = "Asia/Kathmandu"
	}

	r := New()
	res, err := r.States.Call(c, "timezone.system", value.MapOf("name", want))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("timezone.system %s failed: %s", want, res.Comment)
	}
	if res.Changes == nil || res.Changes.Len() == 0 {
		t.Errorf("timezone.system %s reported no change: %s", want, res.Comment)
	}

	// The fixture this replaces assumed `systemsetup` re-points the link
	// the reader reads. Checked here against the link itself, and against
	// what `systemsetup` reports, which are the two things that have to
	// agree for the next run to see the change.
	macLogTool(t, "ls", "-l", "/etc/localtime")
	macLogTool(t, "systemsetup", "-gettimezone")
	if link, err := macZoneLinkWithin(t, want, 5*time.Second); err != nil || link != want {
		macLogTool(t, "ls", "-l", "/etc/localtime")
		t.Fatalf("five seconds after setting %s, /etc/localtime names %q (%v)", want, link, err)
	}
	if got, err := currentZone(c); err != nil || got != want {
		t.Errorf("after setting %s, get_zone says %q (%v)", want, got, err)
	}
	if out, err := c.Run(hexec.Command{
		Argv: []string{"systemsetup", "-gettimezone"}, IgnoreExitCode: true,
	}); err == nil && !strings.Contains(out.Stdout, want) {
		t.Errorf("after setting %s, systemsetup -gettimezone says %q", want, strings.TrimSpace(out.Stdout))
	}

	res, err = r.States.Call(c, "timezone.system", value.MapOf("name", want))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the second run failed: %s", res.Comment)
	}
	if res.Changes != nil && res.Changes.Len() != 0 {
		t.Errorf("the second run reported a change, so the state does not converge: %v", res.Changes)
	}

	// A zone the node does not have. The state refuses it before the tool
	// runs, which is the refusal list_zones made unreachable on a Mac.
	res, err = r.States.Call(c, "timezone.system", value.MapOf("name", "Mars/Olympus_Mons"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result {
		t.Errorf("timezone.system accepted a zone this Mac does not have: %s", res.Comment)
	}

	// And set_zone, which has no such check in front of it, must report
	// the tool's refusal rather than succeed: `systemsetup` is the tool
	// whose exit status nothing here had ever seen.
	err = setZone(c, "Mars/Olympus_Mons")
	t.Logf("set_zone Mars/Olympus_Mons: %v", err)
	if err == nil {
		t.Errorf("set_zone of a zone this Mac does not have reported success")
	}
	if link := macZoneFromLink(t); link != want {
		t.Errorf("a refused zone moved /etc/localtime from %s to %s", want, link)
	}
}

// macLogTool runs a command outside the module and logs what it said and
// how it exited, which is what this test exists to find out.
func macLogTool(t *testing.T, argv ...string) {
	t.Helper()
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	t.Logf("%s: %q (%v)", strings.Join(argv, " "), strings.TrimSpace(string(out)), err)
}
