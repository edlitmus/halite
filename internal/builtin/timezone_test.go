package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The tzutil listing alternates a localised display name with the
// identifier the tool takes back, and only the identifier is a name a
// state can name. Parsed here from the real output of `tzutil /l` on
// Windows 11, so that the shape is the one the tool actually emits and
// not the one this file imagines.
func TestWindowsZonesReturnsIdentifiersAndNotDisplayNames(t *testing.T) {
	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{
		Responses: map[string]exec.Result{
			"tzutil /l": {Stdout: "" +
				"(UTC-12:00) International Date Line West \r\n" +
				"Dateline Standard Time\r\n" +
				"\r\n" +
				"(UTC-11:00) Coordinated Universal Time-11 \r\n" +
				"UTC-11\r\n" +
				"\r\n" +
				"(UTC-08:00) Pacific Time (US & Canada) \r\n" +
				"Pacific Standard Time\r\n" +
				"\r\n"},
		},
	}

	got, err := windowsZones(c)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Dateline Standard Time", "UTC-11", "Pacific Standard Time"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The zone is recovered from wherever the platform in hand keeps it, and
// the symlink wins: it is what the C library reads, so a stale
// /etc/timezone beside it cannot make this report a zone the node is not
// actually running in.
func TestTheSymlinkOutranksTheFilesBesideIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix zone files do not exist on Windows")
	}
	dir := t.TempDir()
	restore := redirectZoneFiles(t, dir)
	defer restore()

	writeFile(t, etcTimezonePath, "Europe/Berlin\n")
	writeFile(t, zoneNamePath, "Asia/Tokyo\n")
	if err := os.Symlink(filepath.Join(dir, "zoneinfo", "America/Los_Angeles"), localtimePath); err != nil {
		t.Fatal(err)
	}

	got, err := currentZone(newCtx(false))
	if err != nil {
		t.Fatal(err)
	}
	if got != "America/Los_Angeles" {
		t.Errorf("got %q, want America/Los_Angeles", got)
	}

	// With the link gone, /var/db/zoneinfo is next, because that is the
	// only place FreeBSD records the name.
	if err := os.Remove(localtimePath); err != nil {
		t.Fatal(err)
	}
	if got, _ := currentZone(newCtx(false)); got != "Asia/Tokyo" {
		t.Errorf("without the link, got %q, want Asia/Tokyo", got)
	}
}

// A zone held as a copy stays a copy and a zone held as a link stays a
// link. Turning FreeBSD's copy into a symlink is a change to the node
// nobody asked for, and its own tzsetup would undo it on the next run.
func TestLinkZoneKeepsTheFormTheNodeAlreadyUsed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks to /etc/localtime are not how Windows holds the zone")
	}
	dir := t.TempDir()
	restore := redirectZoneFiles(t, dir)
	defer restore()
	writeFile(t, filepath.Join(zoneinfoDir, "America/Denver"), "TZif-denver")
	writeFile(t, filepath.Join(zoneinfoDir, "Asia/Tokyo"), "TZif-tokyo")

	// Held as a copy: it is rewritten as a copy, with the zone's bytes.
	writeFile(t, localtimePath, "TZif-tokyo")
	if err := linkZone("America/Denver"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(localtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("a zone held as a copy was replaced with a symlink")
	}
	if b, _ := os.ReadFile(localtimePath); string(b) != "TZif-denver" {
		t.Errorf("the copy holds %q, want the Denver zone data", b)
	}

	// Held as a link: it stays a link, pointing at the new zone.
	if err := os.Remove(localtimePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(zoneinfoDir, "Asia/Tokyo"), localtimePath); err != nil {
		t.Fatal(err)
	}
	if err := linkZone("America/Denver"); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(localtimePath)
	if err != nil {
		t.Fatalf("a zone held as a link was replaced with a copy: %v", err)
	}
	if zoneFromPath(target) != "America/Denver" {
		t.Errorf("the link points at %q, want the Denver zone", target)
	}
}

// The name caches beside the zone data are rewritten where the platform
// keeps one and not created where it does not: a /etc/timezone invented
// on FreeBSD is a file nothing on that node reads, and one that will be
// wrong the first time the zone is changed by any other means.
func TestLinkZoneRewritesOnlyTheNameCachesThatExist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix zone files do not exist on Windows")
	}
	dir := t.TempDir()
	restore := redirectZoneFiles(t, dir)
	defer restore()
	writeFile(t, filepath.Join(zoneinfoDir, "America/Denver"), "TZif-denver")
	writeFile(t, etcTimezonePath, "Asia/Tokyo\n")

	if err := linkZone("America/Denver"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(etcTimezonePath); strings.TrimSpace(string(b)) != "America/Denver" {
		t.Errorf("/etc/timezone holds %q, want America/Denver", b)
	}
	if _, err := os.Stat(zoneNamePath); err == nil {
		t.Error("a /var/db/zoneinfo was created on a node that had none")
	}
}

// The tz database is a tree of zone files with several plain-text tables
// and two alternate copies of the whole thing beside them, none of which
// is a name set_zone would take.
func TestZoneinfoNamesReturnsZonesAndNotTheTablesBesideThem(t *testing.T) {
	dir := t.TempDir()
	old := zoneinfoDir
	zoneinfoDir = filepath.Join(dir, "zoneinfo")
	defer func() { zoneinfoDir = old }()

	for _, p := range []string{
		"America/Los_Angeles", "America/Argentina/Ushuaia", "UTC",
		"zone.tab", "zone1970.tab", "iso3166.tab", "tzdata.zi", "leapseconds",
		"posixrules", "localtime", "Factory",
		"posix/America/Los_Angeles", "right/America/Los_Angeles",
	} {
		writeFile(t, filepath.Join(zoneinfoDir, filepath.FromSlash(p)), "TZif")
	}

	got, err := zoneinfoNames()
	if err != nil {
		t.Fatal(err)
	}
	want := "America/Argentina/Ushuaia|America/Los_Angeles|UTC"
	if strings.Join(got, "|") != want {
		t.Errorf("got %v, want %s", got, want)
	}
}

// The state is a state: it reports no change when the node already has
// the zone, changes it when it does not, and reports no change again on
// the run after that.
func TestTimezoneSystemConvergesInOneChange(t *testing.T) {
	r := New()
	c, want, settle := zoneFixture(t)

	res, err := r.States.Call(c, "timezone.system", value.MapOf("name", want))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("setting the zone failed: %s", res.Comment)
	}
	if res.Changes == nil || res.Changes.Len() == 0 {
		t.Error("setting the zone reported no change")
	}
	settle()

	res, err = r.States.Call(c, "timezone.system", value.MapOf("name", want))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the second run failed: %s", res.Comment)
	}
	if res.Changes != nil && res.Changes.Len() != 0 {
		t.Errorf("the second run reported a change: %v", res.Changes)
	}
}

// A zone the node does not have is refused rather than handed to the
// tool, and refused in test mode too, where the tool would never run to
// say so itself.
func TestTimezoneSystemRefusesAZoneTheNodeDoesNotHave(t *testing.T) {
	r := New()
	c, _, _ := zoneFixture(t)
	c.Test = true

	res, err := r.States.Call(c, "timezone.system", value.MapOf("name", "Mars/Olympus_Mons"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result {
		t.Fatalf("an unknown zone was accepted: %s", res.Comment)
	}
	if !strings.Contains(res.Comment, "list_zones") {
		t.Errorf("the refusal does not say where the names are: %s", res.Comment)
	}
}

// zoneFixture builds a node whose zone can be read and set without
// touching the machine the test runs on: a scripted tzutil on Windows,
// and a redirected tz database everywhere else. It returns a zone the
// node does not currently have, and a function to run between the change
// and the check that it stuck.
func zoneFixture(t *testing.T) (c *exec.Context, want string, settle func()) {
	t.Helper()
	c = newCtx(false)

	if runtime.GOOS == "windows" {
		zone := "Pacific Standard Time"
		listing := "(UTC-08:00) Pacific Time (US & Canada) \r\n" + zone + "\r\n\r\n"
		runner := &exec.RecordingRunner{
			Responses: map[string]exec.Result{
				"tzutil /g": {Stdout: "UTC"},
				"tzutil /l": {Stdout: listing},
			},
		}
		c.Runner = runner
		return c, zone, func() {
			runner.Responses["tzutil /g"] = exec.Result{Stdout: zone}
		}
	}

	dir := t.TempDir()
	restore := redirectZoneFiles(t, dir)
	t.Cleanup(restore)
	writeFile(t, filepath.Join(zoneinfoDir, "America/Denver"), "TZif-denver")
	writeFile(t, filepath.Join(zoneinfoDir, "UTC"), "TZif-utc")
	if err := os.Symlink(filepath.Join(zoneinfoDir, "UTC"), localtimePath); err != nil {
		t.Fatal(err)
	}
	// So that setZone writes the files rather than reaching for systemd,
	// which is what a container without one gets.
	c.Lookup = func(string) string { return "" }
	return c, "America/Denver", func() {}
}

// redirectZoneFiles points every path the module reads into a temporary
// tree, and returns the function that puts them back.
func redirectZoneFiles(t *testing.T, dir string) func() {
	t.Helper()
	oldZoneinfo, oldLocaltime := zoneinfoDir, localtimePath
	oldEtc, oldName := etcTimezonePath, zoneNamePath
	zoneinfoDir = filepath.Join(dir, "zoneinfo")
	localtimePath = filepath.Join(dir, "etc", "localtime")
	etcTimezonePath = filepath.Join(dir, "etc", "timezone")
	zoneNamePath = filepath.Join(dir, "var", "db", "zoneinfo")
	for _, d := range []string{zoneinfoDir, filepath.Dir(localtimePath), filepath.Dir(zoneNamePath)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		zoneinfoDir, localtimePath = oldZoneinfo, oldLocaltime
		etcTimezonePath, zoneNamePath = oldEtc, oldName
	}
}
