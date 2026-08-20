package grains

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

func collect(t *testing.T, opts Options) *value.Map {
	t.Helper()
	if opts.NodeID == "" {
		opts.NodeID = "test.node"
	}
	g, warnings := Collect(opts)
	for _, w := range warnings {
		t.Logf("warning: %s", w)
	}
	return g
}

// TestCoreGrainsArePresent holds collection to the groups SPEC section
// 14.1 names. A grain that is absent rather than empty makes every
// template that reads it need a guard, so the contract is that the key
// exists on every platform.
func TestCoreGrainsArePresent(t *testing.T) {
	g := collect(t, Options{})
	required := []string{
		// Identity.
		"id", "host", "fqdn", "domain", "nodename", "localhost",
		// OS.
		"os", "os_family", "osfullname", "osrelease", "osrelease_info",
		"osmajorrelease", "oscodename", "osfinger", "osarch",
		"lsb_distrib_id", "lsb_distrib_release", "lsb_distrib_codename",
		// Kernel.
		"kernel", "kernelrelease", "kernelversion", "kernelparams",
		// CPU and memory.
		"cpuarch", "num_cpus", "cpu_model", "cpu_flags", "mem_total", "swap_total",
		// Virtualization.
		"virtual", "virtual_subtype", "container",
		// Network.
		"ipv4", "ipv6", "ip_interfaces", "ip4_interfaces", "ip6_interfaces",
		"hwaddr_interfaces", "dns",
		// Hardware.
		"manufacturer", "productname", "serialnumber", "biosversion",
		"biosreleasedate", "uuid", "chassis", "efi", "disks", "ssds",
		// Platform.
		"init", "systemd", "shell", "path", "systempath", "locale_info",
		"saltversion", "haliteversion", "haliteversioninfo",
		// Security.
		"selinux", "apparmor", "fips_mode", "secure_boot", "tpm", "lockdown",
	}
	for _, name := range required {
		if !g.Has(name) {
			t.Errorf("the %s grain is missing", name)
		}
	}
}

func TestIdentityGrains(t *testing.T) {
	g := collect(t, Options{NodeID: "web1.prod"})
	if got, _ := g.Get("id"); got != "web1.prod" {
		t.Errorf("id = %#v", got)
	}
	host, _ := g.Get("host")
	if host == "" {
		t.Error("host is empty")
	}
	// The short host must not carry a domain.
	if s, _ := host.(string); s != shortHost(s) {
		t.Errorf("host %q should be the short form", s)
	}
}

func TestNumericGrainsAreIntegers(t *testing.T) {
	// A grain that arrives as a float breaks a template that formats it,
	// and a 64-bit byte count that round-trips through float64 loses
	// precision. SPEC section 6.4.
	g := collect(t, Options{})
	for _, name := range []string{"num_cpus", "mem_total", "swap_total"} {
		v, _ := g.Get(name)
		if _, ok := v.(int64); !ok {
			t.Errorf("%s = %#v (%T), want an int64", name, v, v)
		}
	}
}

func TestNetworkGrainsAreStable(t *testing.T) {
	// Two collections on an unchanged host must produce identical
	// grains, or a grain-triggered reactor fires on nothing.
	first := collect(t, Options{})
	second := collect(t, Options{})
	for _, name := range []string{"ipv4", "ipv6", "ip_interfaces"} {
		a, _ := first.Get(name)
		b, _ := second.Get(name)
		if renderStable(a) != renderStable(b) {
			t.Errorf("%s differs between collections:\n%s\n%s", name, renderStable(a), renderStable(b))
		}
	}
}

func renderStable(v any) string {
	b, err := value.EncodeJSON(v, 0)
	if err != nil {
		return "<error>"
	}
	return string(b)
}

func TestStaticGrainsFileOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grains")
	if err := os.WriteFile(path, []byte("role: webserver\nos: OverriddenOS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := collect(t, Options{StaticFile: path})
	if got, _ := g.Get("role"); got != "webserver" {
		t.Errorf("role = %#v", got)
	}
	// Merged last, so it can override a collected fact.
	if got, _ := g.Get("os"); got != "OverriddenOS" {
		t.Errorf("os = %#v; the static file is merged last", got)
	}
}

func TestConfigGrainsAreMerged(t *testing.T) {
	g := collect(t, Options{Extra: value.MapOf("datacenter", "lhr1")})
	if got, _ := g.Get("datacenter"); got != "lhr1" {
		t.Errorf("datacenter = %#v", got)
	}
}

func TestGrainsDirectory(t *testing.T) {
	dir := t.TempDir()

	// A YAML fragment is parsed.
	if err := os.WriteFile(filepath.Join(dir, "10-static.yaml"), []byte("tier: frontend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An executable fragment is run and its JSON merged. This is the
	// low-ceremony path that most Salt _grains/ modules actually needed.
	script := filepath.Join(dir, "20-dynamic")
	body := "#!/bin/sh\nprintf '%s' '{\"rack\": \"a12\", \"num_disks\": 4}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	g := collect(t, Options{GrainsDir: dir})
	if got, _ := g.Get("tier"); got != "frontend" {
		t.Errorf("tier = %#v", got)
	}
	if got, _ := g.Get("rack"); got != "a12" {
		t.Errorf("rack = %#v", got)
	}
	// A JSON integer must arrive as an integer.
	if got, _ := g.Get("num_disks"); got != int64(4) {
		t.Errorf("num_disks = %#v (%T)", got, got)
	}
}

// TestBadProviderIsSkippedNotFatal is the failure Salt is known for: one
// bad grain script taking down grain collection entirely.
func TestBadProviderIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "10-good.yaml"), []byte("good: yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20-nonzero"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "30-badjson"), []byte("#!/bin/sh\necho 'not json'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "40-alsogood.yaml"), []byte("also_good: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g, warnings := Collect(Options{NodeID: "n", GrainsDir: dir})
	if len(warnings) != 2 {
		t.Errorf("warnings = %v, want one per bad provider", warnings)
	}
	// The good fragments on both sides of the bad ones still landed, and
	// the core grains survived.
	if got, _ := g.Get("good"); got != true {
		t.Errorf("good = %#v", got)
	}
	if got, _ := g.Get("also_good"); got != int64(1) {
		t.Errorf("also_good = %#v", got)
	}
	if !g.Has("os") {
		t.Error("a bad provider took the core grains down with it")
	}
}

func TestReleaseHelpers(t *testing.T) {
	if got := majorVersion("22.04"); got != "22" {
		t.Errorf("majorVersion = %q", got)
	}
	if got := majorVersion("9"); got != "9" {
		t.Errorf("majorVersion = %q", got)
	}
	info := releaseInfo("22.04.3")
	if len(info) != 3 || info[0] != int64(22) || info[1] != int64(4) {
		t.Errorf("releaseInfo = %v", info)
	}
	// A non-numeric component stops the list rather than producing a
	// mixed-type one a template cannot compare.
	if got := releaseInfo("15.1-RELEASE"); len(got) != 2 {
		t.Errorf("releaseInfo = %v", got)
	}
}

func TestKeyValueFileParsing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "os-release")
	body := "NAME=\"Ubuntu\"\nVERSION_ID=\"22.04\"\n# a comment\nID=ubuntu\nID_LIKE=debian\n\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m := readKeyValueFile(path)
	if m["NAME"] != "Ubuntu" || m["VERSION_ID"] != "22.04" || m["ID"] != "ubuntu" {
		t.Errorf("parsed = %v", m)
	}
	if len(m) != 4 {
		t.Errorf("parsed %d keys, want 4: %v", len(m), m)
	}
}
