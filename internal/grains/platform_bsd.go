//go:build !linux && !windows

package grains

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// The BSDs and macOS describe themselves through sysctl, which is a single
// well-defined interface rather than a directory tree. sysctl(8) is part of
// the base system on every one of them, so reading through it needs no
// installed package.

// sysctlString reads one sysctl as text.
func sysctlString(name string) string {
	out, err := exec.Command("sysctl", "-n", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sysctlInt reads one sysctl as a number.
func sysctlInt(name string) int64 {
	n, err := strconv.ParseInt(sysctlString(name), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func collectOS(g *value.Map) {
	switch runtime.GOOS {
	case "darwin":
		collectDarwinOS(g)
	default:
		collectBSDOS(g)
	}
}

// bsdNames spell each platform the way Salt does, because an SLS file's
// `os_family` map is keyed on that spelling and a tree written against
// Salt would silently fall through to its default otherwise.
var bsdNames = map[string]string{
	"freebsd":   "FreeBSD",
	"openbsd":   "OpenBSD",
	"netbsd":    "NetBSD",
	"dragonfly": "DragonFly",
	"solaris":   "Solaris",
	"illumos":   "Illumos",
}

func collectBSDOS(g *value.Map) {
	name, ok := bsdNames[runtime.GOOS]
	if !ok {
		name = runtime.GOOS
	}
	release := sysctlString("kern.osrelease")
	if release == "" {
		release = unameFlag("-r")
	}
	// FreeBSD spells a release as 15.1-RELEASE-p2; the numeric part is
	// what a tree compares against.
	numeric := release
	if i := strings.IndexByte(numeric, '-'); i >= 0 {
		numeric = numeric[:i]
	}

	g.Set("os", name)
	g.Set("osfullname", name)
	g.Set("osrelease", numeric)
	g.Set("osmajorrelease", majorVersion(numeric))
	g.Set("osrelease_info", releaseInfo(numeric))
	g.Set("oscodename", release)
	g.Set("os_family", name)
	g.Set("osarch", goarchToCPU())
	g.Set("osfinger", name+"-"+majorVersion(numeric))
	g.Set("lsb_distrib_id", name)
	g.Set("lsb_distrib_release", numeric)
	g.Set("lsb_distrib_codename", "")
}

func collectDarwinOS(g *value.Map) {
	release := swVers("-productVersion")
	g.Set("os", "MacOS")
	g.Set("osfullname", swVers("-productName"))
	g.Set("osrelease", release)
	g.Set("osmajorrelease", majorVersion(release))
	g.Set("osrelease_info", releaseInfo(release))
	g.Set("oscodename", swVers("-buildVersion"))
	g.Set("os_family", "MacOS")
	g.Set("osarch", goarchToCPU())
	g.Set("osfinger", "MacOS-"+majorVersion(release))
	g.Set("lsb_distrib_id", "MacOS")
	g.Set("lsb_distrib_release", release)
	g.Set("lsb_distrib_codename", "")
}

func swVers(flag string) string {
	out, err := exec.Command("sw_vers", flag).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func unameFlag(flag string) string {
	out, err := exec.Command("uname", flag).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func collectKernel(g *value.Map) {
	kernel := sysctlString("kern.ostype")
	if kernel == "" {
		kernel = unameFlag("-s")
	}
	g.Set("kernel", kernel)
	g.Set("kernelrelease", firstNonEmptyBSD(sysctlString("kern.osrelease"), unameFlag("-r")))
	g.Set("kernelversion", firstNonEmptyBSD(sysctlString("kern.version"), unameFlag("-v")))
	g.Set("kernelparams", value.NewMap(0))
}

func collectCPU(g *value.Map) {
	g.Set("cpuarch", goarchToCPU())

	n := sysctlInt("hw.ncpu")
	if n == 0 {
		n = int64(runtime.NumCPU())
	}
	g.Set("num_cpus", n)

	model := sysctlString("hw.model")
	if model == "" {
		model = sysctlString("machdep.cpu.brand_string")
	}
	g.Set("cpu_model", model)

	var flags []any
	for _, f := range strings.Fields(sysctlString("machdep.cpu.features")) {
		flags = append(flags, strings.ToLower(f))
	}
	g.Set("cpu_flags", orEmpty(flags))
}

func collectMemory(g *value.Map) {
	// Salt reports these in mebibytes.
	bytes := sysctlInt("hw.physmem")
	if bytes == 0 {
		bytes = sysctlInt("hw.memsize")
	}
	g.Set("mem_total", bytes/(1024*1024))

	swap := sysctlInt("vm.swap_total")
	g.Set("swap_total", swap/(1024*1024))
}

func collectSecurity(g *value.Map) {
	// A FIPS kernel state is a Linux concept; on these platforms the
	// grain reports false rather than being absent, so a template that
	// reads it does not have to guard for the platform.
	g.Set("fips_mode", false)
	g.Set("selinux", value.MapOf("enabled", false, "enforced", "Disabled"))
	g.Set("apparmor", false)
	g.Set("secure_boot", false)
	_, err := os.Stat("/dev/tpm0")
	g.Set("tpm", err == nil)
	g.Set("lockdown", false)
}

func collectVirtualization(g *value.Map) {
	virt := "physical"
	subtype := ""

	// FreeBSD reports a hypervisor directly, and a jail through a
	// separate sysctl.
	if v := sysctlString("kern.vm_guest"); v != "" && v != "none" {
		virt = v
	}
	if sysctlString("security.jail.jailed") == "1" {
		virt = "jail"
	}
	vendor := sysctlString("hw.hv_vendor")
	if vendor != "" {
		subtype = strings.TrimSpace(vendor)
	}
	if runtime.GOOS == "darwin" && sysctlString("kern.hv_vmm_present") == "1" {
		virt = "virtual"
	}

	g.Set("virtual", virt)
	g.Set("virtual_subtype", subtype)
	g.Set("container", "")
}

func collectHardware(g *value.Map) {
	g.Set("manufacturer", kenvOr("smbios.system.maker", sysctlString("hw.model")))
	g.Set("productname", kenvOr("smbios.system.product", ""))
	g.Set("serialnumber", kenvOr("smbios.system.serial", ""))
	g.Set("biosversion", kenvOr("smbios.bios.version", ""))
	g.Set("biosreleasedate", kenvOr("smbios.bios.reldate", ""))
	g.Set("uuid", strings.ToLower(kenvOr("smbios.system.uuid", "")))
	g.Set("chassis", kenvOr("smbios.chassis.type", ""))
	g.Set("efi", efiPresent())
	g.Set("disks", diskDevices())
	g.Set("ssds", []any{})
}

// kenvOr reads a FreeBSD kernel environment variable, which is where the
// SMBIOS fields land without needing dmidecode.
func kenvOr(name, def string) string {
	if runtime.GOOS != "freebsd" {
		return def
	}
	out, err := exec.Command("kenv", "-q", name).Output()
	if err != nil {
		return def
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return def
	}
	return s
}

func efiPresent() bool {
	if runtime.GOOS == "darwin" {
		return true
	}
	return sysctlString("machdep.bootmethod") == "UEFI"
}

func diskDevices() []any {
	var out []any
	for _, name := range strings.Fields(sysctlString("kern.disks")) {
		out = append(out, name)
	}
	return orEmpty(out)
}

func detectInit() string {
	if runtime.GOOS == "darwin" {
		return "launchd"
	}
	return "init"
}

func detectSystemd() *value.Map {
	return value.MapOf("version", "", "features", "")
}

func firstNonEmptyBSD(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
