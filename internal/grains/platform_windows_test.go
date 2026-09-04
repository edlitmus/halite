package grains

import (
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The Windows grains were a stub: `os` was the literal "Windows",
// `osrelease` was empty, memory was zero, and every hardware field was
// an empty string. A template asking
// `{% if grains['osrelease'] >= '10' %}` got an empty string,
// `osmajorrelease` could not be compared, and
// `halite-node grains item osrelease` answered with nothing.
//
// This runs against the machine it is on, because the point is that the
// facts are read rather than declared. It asserts shapes and
// relationships, not this machine's own values.
func TestTheWindowsGrainsAreReadFromTheMachine(t *testing.T) {
	g := collect(t, Options{})

	// The version a tree compares against. Three dotted numbers, so it
	// orders; the marketing name does not.
	release := str(t, g, "osrelease")
	parts := strings.Split(release, ".")
	if len(parts) != 3 {
		t.Fatalf("osrelease = %q, want <major>.<minor>.<build>", release)
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			t.Errorf("osrelease = %q, and %q is not a number", release, p)
		}
	}
	build, _ := strconv.Atoi(parts[2])
	if build < 1000 {
		t.Errorf("osrelease = %q; a build number is five digits on any supported Windows", release)
	}

	// osmajorrelease and osrelease_info are derived from it, and a
	// template uses one or the other to branch.
	if got := str(t, g, "osmajorrelease"); got != parts[0] {
		t.Errorf("osmajorrelease = %q, want %q", got, parts[0])
	}
	info, _ := g.Get("osrelease_info")
	if list, ok := info.([]any); !ok || len(list) != 3 {
		t.Errorf("osrelease_info = %v, want three numbers", info)
	}

	// ProductName says "Windows 10" on Windows 11 to this day, and a
	// grain that repeated it would send a tree down the wrong branch.
	full := str(t, g, "osfullname")
	if build >= 22000 && strings.Contains(full, "Windows 10") {
		t.Errorf("osfullname = %q on build %d, which is Windows 11", full, build)
	}
	if full == "" {
		t.Error("osfullname is empty")
	}

	// The facts that were zero or empty in the stub.
	if got := i64(t, g, "mem_total"); got < 256 {
		t.Errorf("mem_total = %d MiB; no supported Windows runs in that", got)
	}
	if got := i64(t, g, "num_cpus"); got < 1 {
		t.Errorf("num_cpus = %d", got)
	}
	if got := str(t, g, "cpu_model"); got == "" {
		t.Error("cpu_model is empty")
	}
	if got := str(t, g, "kernelversion"); !strings.HasPrefix(got, release) {
		t.Errorf("kernelversion = %q, want %q with the update revision after it", got, release)
	}
	// MachineGuid is what an inventory joins on, and every Windows has
	// one.
	if got := str(t, g, "uuid"); len(got) != 36 {
		t.Errorf("uuid = %q, want a GUID", got)
	}

	// Booleans, not empty strings: a tree tests them directly.
	for _, key := range []string{"fips_mode", "secure_boot", "tpm", "efi", "apparmor", "lockdown"} {
		if v, ok := g.Get(key); !ok {
			t.Errorf("%s is absent", key)
		} else if _, ok := v.(bool); !ok {
			t.Errorf("%s = %#v, want a boolean", key, v)
		}
	}

	// Present but empty rather than absent, so a template written for a
	// mixed estate reads them without guarding for the platform.
	for _, key := range []string{
		"oscodename", "manufacturer", "productname", "serialnumber",
		"biosversion", "biosreleasedate", "chassis", "virtual_subtype", "container",
	} {
		if _, ok := g.Get(key); !ok {
			t.Errorf("%s is absent; it should be present and empty", key)
		}
	}
	for _, key := range []string{"disks", "ssds", "cpu_flags"} {
		if v, _ := g.Get(key); v == nil {
			t.Errorf("%s is nil; it should be an empty list", key)
		}
	}
	if _, ok := mustBeMap(g, "selinux"); !ok {
		t.Error("selinux should be a mapping reporting that there is none")
	}
	if _, ok := mustBeMap(g, "systemd"); !ok {
		t.Error("systemd should be a mapping reporting that there is none")
	}

	if got := str(t, g, "init"); got != "windows" {
		t.Errorf("init = %q", got)
	}
	if got := str(t, g, "os_family"); got != "Windows" {
		t.Errorf("os_family = %q", got)
	}
}

// A hypervisor is recognised from the strings it writes into the BIOS
// tables, which are the same ones the Linux side reads out of DMI.
func TestAHypervisorIsRecognisedFromTheBIOSStrings(t *testing.T) {
	g := collect(t, Options{})
	virt := str(t, g, "virtual")
	if virt == "" {
		t.Fatal("virtual is empty; it should be `physical` or a hypervisor's name")
	}
	// Whatever this machine is, the subtype agrees with the verdict.
	subtype := str(t, g, "virtual_subtype")
	if virt == "physical" && subtype != "" {
		t.Errorf("virtual = physical but virtual_subtype = %q", subtype)
	}
	if virt != "physical" && subtype == "" {
		t.Errorf("virtual = %q but virtual_subtype is empty", virt)
	}
}

// A board that nobody filled the SMBIOS strings in on reports the
// vendor's placeholder. Repeating it as a fact is how an inventory ends
// up with four hundred machines whose chassis is "To be filled by
// O.E.M.".
func TestAnOEMPlaceholderIsNotAFact(t *testing.T) {
	for _, s := range []string{
		"To be filled by O.E.M.", "to be filled by o.e.m.", "Default string",
		"System Product Name", "  ", "", "Unknown",
	} {
		if !isOEMPlaceholder(s) {
			t.Errorf("%q should be recognised as a placeholder", s)
		}
	}
	for _, s := range []string{"Dell Inc.", "OptiPlex 7090", "Desktop"} {
		if isOEMPlaceholder(s) {
			t.Errorf("%q is a real value and should not be discarded", s)
		}
	}
}

// ProductName is corrected for the build, which is the only way to tell
// Windows 11 from Windows 10.
func TestProductNameIsCorrectedForTheBuild(t *testing.T) {
	cases := []struct {
		product string
		major   uint32
		build   uint32
		want    string
	}{
		{"Windows 10 Pro", 10, 19045, "Windows 10 Pro"},
		{"Windows 10 Pro", 10, 22000, "Windows 11 Pro"},
		{"Windows 10 Enterprise", 10, 26100, "Windows 11 Enterprise"},
		{"Windows Server 2022 Standard", 10, 20348, "Windows Server 2022 Standard"},
		{"", 10, 26100, "Windows"},
	}
	for _, c := range cases {
		if got := correctProductName(c.product, c.major, c.build); got != c.want {
			t.Errorf("correctProductName(%q, %d, %d) = %q, want %q",
				c.product, c.major, c.build, got, c.want)
		}
	}
}

func str(t *testing.T, g *value.Map, key string) string {
	t.Helper()
	v, ok := g.Get(key)
	if !ok {
		t.Fatalf("%s is absent", key)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s = %#v, want a string", key, v)
	}
	return s
}

func i64(t *testing.T, g *value.Map, key string) int64 {
	t.Helper()
	v, ok := g.Get(key)
	if !ok {
		t.Fatalf("%s is absent", key)
	}
	n, ok := v.(int64)
	if !ok {
		t.Fatalf("%s = %#v, want an integer", key, v)
	}
	return n
}

func mustBeMap(g *value.Map, key string) (*value.Map, bool) {
	v, ok := g.Get(key)
	if !ok {
		return nil, false
	}
	m, ok := v.(*value.Map)
	return m, ok
}
