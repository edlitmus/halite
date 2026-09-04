//go:build windows

package grains

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/edlitmus/halite/internal/value"
)

// The Windows grain sources are the registry and the Win32 API, which is
// what SPEC 14.1 names for this platform.
//
// This used to be a stub: `os` was the literal string "Windows",
// `osrelease` was empty, memory was zero, and every hardware field was
// an empty string, with a comment deferring the real sources to a later
// phase. A template asking `{% if grains['osrelease'] >= '10' %}` got an
// empty string, `osmajorrelease` could not be compared at all, and
// `halite-node grains item osrelease` answered with nothing — which is
// how the gap was found.
//
// Nothing here shells out. There is no wmic, no systeminfo and no
// PowerShell: those are three processes and a parse per grain refresh,
// and wmic is deprecated and absent from recent images. The registry
// keys read here are the ones Windows itself reports these facts from.

// currentVersionKey is where Windows records what it is.
const currentVersionKey = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`

// regString reads one string value, or empty when it is not there.
func regString(root registry.Key, path, name string) string {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	s, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return s
}

// regUint reads one integer value, or zero when it is not there.
func regUint(root registry.Key, path, name string) uint64 {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return 0
	}
	defer k.Close()
	n, _, err := k.GetIntegerValue(name)
	if err != nil {
		return 0
	}
	return n
}

func collectOS(g *value.Map) {
	// RtlGetVersion rather than GetVersionEx: the latter lies to a
	// process without a compatibility manifest, reporting 6.2 on every
	// Windows since 8, and halite ships no manifest.
	ver := windows.RtlGetVersion()
	build := ver.BuildNumber

	product := regString(registry.LOCAL_MACHINE, currentVersionKey, "ProductName")
	displayVersion := regString(registry.LOCAL_MACHINE, currentVersionKey, "DisplayVersion")
	if displayVersion == "" {
		// Before 20H2 the same fact was under this name.
		displayVersion = regString(registry.LOCAL_MACHINE, currentVersionKey, "ReleaseId")
	}

	// ProductName still says "Windows 10" on Windows 11: Microsoft never
	// updated the value, and every tool that reads it has to correct for
	// the build number instead. A grain that called Windows 11 "Windows
	// 10" would send a tree down the wrong branch of a version check.
	name := correctProductName(product, ver.MajorVersion, build)
	release := releaseFor(ver, build)

	g.Set("os", "Windows")
	g.Set("osfullname", name)
	g.Set("osrelease", release)
	g.Set("osmajorrelease", majorVersion(release))
	g.Set("osrelease_info", releaseInfo(release))
	// The marketing name of the half-yearly release — 24H2, 22H2 — which
	// is the version an operator patches against and the one an estate's
	// tree branches on.
	g.Set("oscodename", displayVersion)
	g.Set("os_family", "Windows")
	g.Set("osarch", goarchToCPU())
	g.Set("osfinger", name+"-"+majorVersion(release))

	// The lsb_ grains have no counterpart here. They are filled with the
	// nearest equivalent rather than left absent, because a tree written
	// for a mixed estate reads them without guarding for the platform.
	g.Set("lsb_distrib_id", name)
	g.Set("lsb_distrib_release", release)
	g.Set("lsb_distrib_codename", displayVersion)
}

// correctProductName fixes the two places the registry's own answer is
// wrong or unhelpful.
func correctProductName(product string, major uint32, build uint32) string {
	if product == "" {
		return "Windows"
	}
	// Windows 11 is build 22000 and later, and still calls itself
	// Windows 10 in ProductName.
	if major == 10 && build >= 22000 {
		return strings.Replace(product, "Windows 10", "Windows 11", 1)
	}
	return product
}

// releaseFor is the release a tree compares against.
//
// The build number, not the marketing name: `10.0.26100` orders and
// compares, and "24H2" does not. osrelease_info splits it, so a template
// can take the parts.
func releaseFor(ver *windows.OsVersionInfoEx, build uint32) string {
	return fmt.Sprintf("%d.%d.%d", ver.MajorVersion, ver.MinorVersion, build)
}

func collectKernel(g *value.Map) {
	ver := windows.RtlGetVersion()
	release := releaseFor(ver, ver.BuildNumber)

	g.Set("kernel", "Windows")
	g.Set("kernelrelease", release)
	// UBR is the update build revision, the fourth part of the version
	// an operator sees in winver and the one that moves with a patch.
	if ubr := regUint(registry.LOCAL_MACHINE, currentVersionKey, "UBR"); ubr > 0 {
		g.Set("kernelversion", fmt.Sprintf("%s.%d", release, ubr))
	} else {
		g.Set("kernelversion", release)
	}
	// No counterpart to a kernel command line here. Empty rather than
	// absent, so a template that reads it does not have to guard.
	g.Set("kernelparams", value.NewMap(0))
}

func collectCPU(g *value.Map) {
	g.Set("cpuarch", goarchToCPU())
	g.Set("num_cpus", int64(runtime.NumCPU()))

	const cpu0 = `HARDWARE\DESCRIPTION\System\CentralProcessor\0`
	model := regString(registry.LOCAL_MACHINE, cpu0, "ProcessorNameString")
	if model == "" {
		// What the environment reports is coarser — the family and
		// stepping rather than the marketing name — but it is never
		// absent, and a coarse answer beats an empty one.
		model = os.Getenv("PROCESSOR_IDENTIFIER")
	}
	g.Set("cpu_model", strings.TrimSpace(model))

	// There is no flag list in the registry, and CPUID would mean
	// hand-decoding feature bits per vendor. The list that can be
	// answered is the one Windows itself exposes through
	// IsProcessorFeaturePresent, which is what a tree branching on a
	// capability here would ask about.
	g.Set("cpu_flags", processorFeatures())
}

// processorFeature numbers from winnt.h, with the names a tree would
// recognise. Only the ones an estate plausibly branches on.
var processorFeatures_ = []struct {
	feature uint32
	name    string
}{
	{6, "sse"},   // PF_XMMI_INSTRUCTIONS_AVAILABLE
	{10, "sse2"}, // PF_XMMI64_INSTRUCTIONS_AVAILABLE
	{13, "sse3"}, // PF_SSE3_INSTRUCTIONS_AVAILABLE
	{36, "sse4_1"},
	{37, "sse4_2"},
	{39, "avx"},
	{40, "avx2"},
	{12, "nx"},  // PF_NX_ENABLED
	{17, "vmx"}, // PF_VIRT_FIRMWARE_ENABLED
	{25, "aes"}, // PF_ARM_V8_CRYPTO... on x86 this is unset, which is correct
}

func processorFeatures() []any {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessorFeaturePresent")
	out := []any{}
	for _, f := range processorFeatures_ {
		r, _, _ := proc.Call(uintptr(f.feature))
		if r != 0 {
			out = append(out, f.name)
		}
	}
	return out
}

func collectMemory(g *value.Map) {
	// GlobalMemoryStatusEx, which is the documented way to ask and needs
	// no privilege. This reported zero for both.
	var status memoryStatusEx
	status.length = uint32(unsafe.Sizeof(status))
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&status))); r == 0 {
		g.Set("mem_total", int64(0))
		g.Set("swap_total", int64(0))
		return
	}
	// MiB, to match what the other platforms report.
	g.Set("mem_total", int64(status.totalPhys>>20))
	// TotalPageFile counts physical memory as well as the page file,
	// because it is the commit limit rather than the file's size.
	// Reporting it whole would say a machine with no page file has as
	// much swap as it has memory.
	swap := int64(0)
	if status.totalPageFile > status.totalPhys {
		swap = int64((status.totalPageFile - status.totalPhys) >> 20)
	}
	g.Set("swap_total", swap)
}

// memoryStatusEx is MEMORYSTATUSEX from sysinfoapi.h.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

func collectSecurity(g *value.Map) {
	// The policy that makes Windows use only FIPS-validated algorithms.
	// A node with it set behaves differently, and an estate that has
	// both needs to be able to target the difference.
	g.Set("fips_mode", regUint(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Lsa\FipsAlgorithmPolicy`, "Enabled") == 1)
	collectFIPSBuild(g)

	// SELinux and AppArmor have no counterpart. Reported absent rather
	// than omitted, so a tree written for a mixed estate reads them
	// without guarding for the platform.
	g.Set("selinux", value.MapOf("enabled", false, "enforced", "Disabled"))
	g.Set("apparmor", false)

	g.Set("secure_boot", regUint(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\SecureBoot\State`, "UEFISecureBootEnabled") == 1)
	// The presence of a TPM's driver key, which is what Windows itself
	// keys off and needs no privilege to read.
	g.Set("tpm", hasKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\TPM`))
	// Kernel lockdown is a Linux concept.
	g.Set("lockdown", false)
}

func hasKey(root registry.Key, path string) bool {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func collectVirtualization(g *value.Map) {
	virt, subtype := "physical", ""

	// The BIOS strings are what every hypervisor writes its name into,
	// and they are the same ones the Linux side reads out of DMI.
	const biosKey = `HARDWARE\DESCRIPTION\System\BIOS`
	haystack := strings.ToLower(strings.Join([]string{
		regString(registry.LOCAL_MACHINE, biosKey, "SystemManufacturer"),
		regString(registry.LOCAL_MACHINE, biosKey, "SystemProductName"),
		regString(registry.LOCAL_MACHINE, biosKey, "BIOSVendor"),
	}, " "))
	for _, m := range []struct{ needle, name string }{
		{"vmware", "VMware"},
		{"virtualbox", "VirtualBox"},
		{"innotek", "VirtualBox"},
		{"kvm", "kvm"},
		{"qemu", "qemu"},
		{"xen", "xen"},
		{"parallels", "Parallels"},
		{"amazon ec2", "kvm"},
		{"google", "kvm"},
		{"microsoft corporation virtual machine", "HyperV"},
		{"virtual machine", "HyperV"},
	} {
		if strings.Contains(haystack, m.needle) {
			virt, subtype = m.name, m.name
			break
		}
	}

	g.Set("virtual", virt)
	g.Set("virtual_subtype", subtype)
	// A Windows container sets this, and nothing else does.
	g.Set("container", os.Getenv("CONTAINER_SANDBOX_MOUNT_POINT"))
}

func collectHardware(g *value.Map) {
	const biosKey = `HARDWARE\DESCRIPTION\System\BIOS`
	g.Set("manufacturer", regString(registry.LOCAL_MACHINE, biosKey, "SystemManufacturer"))
	g.Set("productname", regString(registry.LOCAL_MACHINE, biosKey, "SystemProductName"))
	g.Set("serialnumber", regString(registry.LOCAL_MACHINE, biosKey, "SystemSerialNumber"))
	g.Set("biosversion", regString(registry.LOCAL_MACHINE, biosKey, "BIOSVersion"))
	g.Set("biosreleasedate", regString(registry.LOCAL_MACHINE, biosKey, "BIOSReleaseDate"))
	// The machine's own identifier, which is what an inventory joins on.
	// Not the SMBIOS UUID: reading that needs GetSystemFirmwareTable and
	// a parse of the whole table, and this one is stable across
	// reinstalls of the same image in the way an estate cares about.
	g.Set("uuid", regString(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Cryptography`, "MachineGuid"))
	g.Set("chassis", chassisType())
	// Firmware type, which decides whether a tree may manage an EFI
	// partition. A machine booted from UEFI has a system partition and a
	// BIOS one does not.
	g.Set("efi", firmwareIsUEFI())

	// The disk grains need the volume and partition APIs, which is more
	// than a fact lookup. Empty lists rather than absent keys, so a
	// template that iterates them does not have to guard.
	g.Set("disks", []any{})
	g.Set("ssds", []any{})
}

// chassisType maps the SMBIOS chassis code the registry carries onto the
// names the other platforms report.
//
// The code first, and the family name only as a fallback. SystemFamily
// is free text a board vendor fills in, and on a machine built from
// parts it is often the literal placeholder — "To be filled by O.E.M.",
// which is worse than "Desktop" for a tree deciding what to install.
func chassisType() string {
	const biosKey = `HARDWARE\DESCRIPTION\System\BIOS`
	switch regUint(registry.LOCAL_MACHINE, biosKey, "EnclosureType") {
	case 3, 4, 5, 6, 7, 15:
		return "Desktop"
	case 8, 9, 10, 14:
		return "Laptop"
	case 17, 23:
		return "Server"
	case 12, 13:
		return "Docking Station"
	}
	family := regString(registry.LOCAL_MACHINE, biosKey, "SystemFamily")
	if isOEMPlaceholder(family) {
		return ""
	}
	return family
}

// isOEMPlaceholder reports whether a SMBIOS string is one of the strings
// a board ships with when nobody filled the field in. Reporting one as
// though it were a fact is how an inventory ends up with four hundred
// machines whose chassis is "To be filled by O.E.M.".
func isOEMPlaceholder(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "to be filled by o.e.m.", "default string", "system manufacturer",
		"system product name", "not specified", "none", "unknown", "not applicable":
		return true
	}
	return false
}

// firmwareIsUEFI reports whether the machine booted from UEFI.
//
// GetFirmwareEnvironmentVariable with an empty name fails with
// ERROR_INVALID_FUNCTION on a BIOS machine and with a different error on
// a UEFI one. That is the documented way to ask, and it needs no
// privilege beyond what a service already has.
func firmwareIsUEFI() bool {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetFirmwareEnvironmentVariableW")
	name, err := windows.UTF16PtrFromString("")
	if err != nil {
		return false
	}
	guid, err := windows.UTF16PtrFromString("{00000000-0000-0000-0000-000000000000}")
	if err != nil {
		return false
	}
	var buf [8]byte
	_, _, errno := proc.Call(
		uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(guid)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return errno != windows.ERROR_INVALID_FUNCTION
}

// detectInit is the service manager. There is one on Windows and it has
// no versions to distinguish.
func detectInit() string { return "windows" }

// detectSystemd reports that there is none, in the shape the other
// platforms use, so a template reading `grains['systemd']['version']`
// gets an empty string rather than an error.
func detectSystemd() *value.Map {
	return value.MapOf("version", "", "features", "")
}
