//go:build linux

package grains

import (
	"os"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// The Linux sources are /proc, /sys, /etc/os-release, and /sys/class/dmi/
// id. No dmidecode, lscpu, or ip binary is required, so grain collection
// works on a minimal image. SPEC section 14.1.

func collectOS(g *value.Map) {
	rel := readKeyValueFile("/etc/os-release")

	name := rel["NAME"]
	id := rel["ID"]
	versionID := rel["VERSION_ID"]

	osName := prettyOSName(id, name)
	g.Set("os", osName)
	g.Set("osfullname", firstNonEmpty(name, osName))
	g.Set("osrelease", versionID)
	g.Set("osmajorrelease", majorVersion(versionID))
	g.Set("osrelease_info", releaseInfo(versionID))
	g.Set("oscodename", firstNonEmpty(rel["VERSION_CODENAME"], rel["UBUNTU_CODENAME"]))
	g.Set("os_family", osFamily(id, rel["ID_LIKE"]))
	g.Set("osarch", goarchToCPU())
	g.Set("osfinger", osName+"-"+majorVersion(versionID))

	// The lsb_ grains are what a tree written before os-release reads.
	g.Set("lsb_distrib_id", firstNonEmpty(name, osName))
	g.Set("lsb_distrib_release", versionID)
	g.Set("lsb_distrib_codename", firstNonEmpty(rel["VERSION_CODENAME"], rel["UBUNTU_CODENAME"]))
}

func prettyOSName(id, name string) string {
	switch id {
	case "ubuntu":
		return "Ubuntu"
	case "debian":
		return "Debian"
	case "rhel":
		return "RedHat"
	case "centos":
		return "CentOS"
	case "rocky":
		return "Rocky"
	case "almalinux":
		return "AlmaLinux"
	case "fedora":
		return "Fedora"
	case "amzn":
		return "Amazon"
	case "sles", "opensuse-leap", "opensuse-tumbleweed":
		return "SUSE"
	case "alpine":
		return "Alpine"
	case "arch":
		return "Arch"
	}
	if name != "" {
		return name
	}
	return "Linux"
}

// osFamily groups a distribution the way an SLS file's `filter_by` map
// expects, which is what makes a per-platform state tree work.
func osFamily(id, idLike string) string {
	candidates := append([]string{id}, strings.Fields(idLike)...)
	for _, c := range candidates {
		switch c {
		case "debian", "ubuntu":
			return "Debian"
		case "rhel", "fedora", "centos", "rocky", "almalinux", "amzn":
			return "RedHat"
		case "suse", "sles", "opensuse":
			return "Suse"
		case "arch":
			return "Arch"
		case "alpine":
			return "Alpine"
		case "gentoo":
			return "Gentoo"
		}
	}
	return "Linux"
}

func collectKernel(g *value.Map) {
	g.Set("kernel", "Linux")
	release := firstLineOf("/proc/sys/kernel/osrelease")
	g.Set("kernelrelease", release)
	g.Set("kernelversion", firstLineOf("/proc/sys/kernel/version"))

	params := value.NewMap(8)
	for _, field := range strings.Fields(firstLineOf("/proc/cmdline")) {
		k, v, _ := strings.Cut(field, "=")
		params.Set(k, v)
	}
	g.Set("kernelparams", params)
}

func collectCPU(g *value.Map) {
	g.Set("cpuarch", goarchToCPU())

	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		g.Set("num_cpus", int64(0))
		g.Set("cpu_model", "")
		g.Set("cpu_flags", []any{})
		return
	}

	count := 0
	model := ""
	var flags []any
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "processor":
			count++
		case "model name", "Model", "cpu model":
			if model == "" {
				model = v
			}
		case "flags", "Features":
			if len(flags) == 0 {
				for _, f := range strings.Fields(v) {
					flags = append(flags, f)
				}
			}
		}
	}
	if count == 0 {
		count = 1
	}
	g.Set("num_cpus", int64(count))
	g.Set("cpu_model", model)
	g.Set("cpu_flags", orEmpty(flags))
}

func collectMemory(g *value.Map) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// Salt reports these in mebibytes.
		switch k {
		case "MemTotal":
			g.Set("mem_total", kb/1024)
		case "SwapTotal":
			g.Set("swap_total", kb/1024)
		}
	}
}

func collectSecurity(g *value.Map) {
	// fips_mode reports the kernel's own state. Whether this *build* is a
	// FIPS artifact is a separate fact, and the distinction matters in an
	// estate that has both. SPEC section 14.1.
	g.Set("fips_mode", firstLineOf("/proc/sys/crypto/fips_enabled") == "1")
	collectFIPSBuild(g)

	selinux := value.MapOf("enabled", false, "enforced", "Disabled")
	if _, err := os.Stat("/sys/fs/selinux"); err == nil {
		selinux.Set("enabled", true)
		switch firstLineOf("/sys/fs/selinux/enforce") {
		case "1":
			selinux.Set("enforced", "Enforcing")
		case "0":
			selinux.Set("enforced", "Permissive")
		}
	}
	g.Set("selinux", selinux)

	_, err := os.Stat("/sys/kernel/security/apparmor")
	g.Set("apparmor", err == nil)

	g.Set("secure_boot", secureBootEnabled())
	_, tpmErr := os.Stat("/dev/tpm0")
	g.Set("tpm", tpmErr == nil)
	g.Set("lockdown", strings.Contains(firstLineOf("/sys/kernel/security/lockdown"), "[integrity]") ||
		strings.Contains(firstLineOf("/sys/kernel/security/lockdown"), "[confidentiality]"))
}

func secureBootEnabled() bool {
	entries, err := os.ReadDir("/sys/firmware/efi/efivars")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "SecureBoot-") {
			continue
		}
		b, err := os.ReadFile("/sys/firmware/efi/efivars/" + e.Name())
		if err != nil || len(b) < 5 {
			return false
		}
		// The first four bytes are EFI variable attributes; the fifth is
		// the value.
		return b[4] == 1
	}
	return false
}

func collectVirtualization(g *value.Map) {
	virt := "physical"
	subtype := ""

	// The DMI product name identifies most hypervisors without any tool.
	product := firstLineOf("/sys/class/dmi/id/product_name")
	vendor := firstLineOf("/sys/class/dmi/id/sys_vendor")
	switch {
	case strings.Contains(product, "KVM"), strings.Contains(vendor, "QEMU"):
		virt = "kvm"
	case strings.Contains(product, "VMware"), strings.Contains(vendor, "VMware"):
		virt = "VMware"
	case strings.Contains(product, "VirtualBox"):
		virt = "VirtualBox"
	case strings.Contains(vendor, "Xen"):
		virt = "xen"
	case strings.Contains(vendor, "Microsoft"), strings.Contains(product, "Virtual Machine"):
		virt = "HyperV"
	case strings.Contains(vendor, "Amazon"):
		virt = "kvm"
		subtype = "Amazon EC2"
	}

	container := ""
	if _, err := os.Stat("/.dockerenv"); err == nil {
		container = "docker"
		virt = "container"
	}
	if v := os.Getenv("container"); v != "" {
		container = v
		virt = "container"
	}
	if cgroup := firstLineOf("/proc/1/cgroup"); strings.Contains(cgroup, "docker") {
		container = "docker"
		virt = "container"
	}

	g.Set("virtual", virt)
	g.Set("virtual_subtype", subtype)
	g.Set("container", container)
}

func collectHardware(g *value.Map) {
	g.Set("manufacturer", firstLineOf("/sys/class/dmi/id/sys_vendor"))
	g.Set("productname", firstLineOf("/sys/class/dmi/id/product_name"))
	g.Set("serialnumber", firstLineOf("/sys/class/dmi/id/product_serial"))
	g.Set("biosversion", firstLineOf("/sys/class/dmi/id/bios_version"))
	g.Set("biosreleasedate", firstLineOf("/sys/class/dmi/id/bios_date"))
	g.Set("uuid", strings.ToLower(firstLineOf("/sys/class/dmi/id/product_uuid")))
	g.Set("chassis", firstLineOf("/sys/class/dmi/id/chassis_type"))
	_, efiErr := os.Stat("/sys/firmware/efi")
	g.Set("efi", efiErr == nil)

	disks, ssds := blockDevices()
	g.Set("disks", orEmpty(disks))
	g.Set("ssds", orEmpty(ssds))
}

// blockDevices reads /sys/block, which is how the kernel already describes
// the disks; no lsblk is needed.
func blockDevices() (disks, ssds []any) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}
		disks = append(disks, name)
		if firstLineOf("/sys/block/"+name+"/queue/rotational") == "0" {
			ssds = append(ssds, name)
		}
	}
	return disks, ssds
}

func detectInit() string {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return "systemd"
	}
	comm := firstLineOf("/proc/1/comm")
	if comm != "" {
		return comm
	}
	return "unknown"
}

func detectSystemd() *value.Map {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return value.MapOf("version", "", "features", "")
	}
	return value.MapOf("version", firstLineOf("/proc/1/comm"), "features", "")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
