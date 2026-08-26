//go:build windows

package grains

import (
	"os"
	"runtime"

	"github.com/edlitmus/halite/internal/value"
)

// The Windows grain sources are the registry and the Win32 API, which
// arrive with the Windows module set of SPEC section 15.3 in a later
// phase. Until then the grains a template is most likely to read are
// filled from what the standard library can answer, and the rest are
// present but empty rather than absent, so a template that reads one does
// not have to guard for the platform.

func collectOS(g *value.Map) {
	g.Set("os", "Windows")
	g.Set("osfullname", "Windows")
	g.Set("osrelease", "")
	g.Set("osmajorrelease", "")
	g.Set("osrelease_info", []any{})
	g.Set("oscodename", "")
	g.Set("os_family", "Windows")
	g.Set("osarch", goarchToCPU())
	g.Set("osfinger", "Windows")
	g.Set("lsb_distrib_id", "Windows")
	g.Set("lsb_distrib_release", "")
	g.Set("lsb_distrib_codename", "")
}

func collectKernel(g *value.Map) {
	g.Set("kernel", "Windows")
	g.Set("kernelrelease", "")
	g.Set("kernelversion", "")
	g.Set("kernelparams", value.NewMap(0))
}

func collectCPU(g *value.Map) {
	g.Set("cpuarch", goarchToCPU())
	g.Set("num_cpus", int64(runtime.NumCPU()))
	g.Set("cpu_model", os.Getenv("PROCESSOR_IDENTIFIER"))
	g.Set("cpu_flags", []any{})
}

func collectMemory(g *value.Map) {
	g.Set("mem_total", int64(0))
	g.Set("swap_total", int64(0))
}

func collectSecurity(g *value.Map) {
	g.Set("fips_mode", false)
	collectFIPSBuild(g)
	g.Set("selinux", value.MapOf("enabled", false, "enforced", "Disabled"))
	g.Set("apparmor", false)
	g.Set("secure_boot", false)
	g.Set("tpm", false)
	g.Set("lockdown", false)
}

func collectVirtualization(g *value.Map) {
	g.Set("virtual", "physical")
	g.Set("virtual_subtype", "")
	g.Set("container", "")
}

func collectHardware(g *value.Map) {
	g.Set("manufacturer", "")
	g.Set("productname", "")
	g.Set("serialnumber", "")
	g.Set("biosversion", "")
	g.Set("biosreleasedate", "")
	g.Set("uuid", "")
	g.Set("chassis", "")
	g.Set("efi", false)
	g.Set("disks", []any{})
	g.Set("ssds", []any{})
}

func detectInit() string { return "windows" }

func detectSystemd() *value.Map {
	return value.MapOf("version", "", "features", "")
}
