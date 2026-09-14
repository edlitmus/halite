//go:build linux

package grains

import (
	"os"
	"os/exec"
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
	g.Set("osmajorrelease", majorRelease(versionID))
	g.Set("osrelease_info", releaseInfo(versionID))
	g.Set("oscodename", firstNonEmpty(rel["VERSION_CODENAME"], rel["UBUNTU_CODENAME"]))
	family := osFamily(id, rel["ID_LIKE"])
	g.Set("os_family", family)
	g.Set("osarch", packageArch(family))
	g.Set("osfinger", osFinger(osName, firstNonEmpty(name, osName), versionID))

	// The lsb_ grains are what a tree written before os-release reads.
	g.Set("lsb_distrib_id", firstNonEmpty(name, osName))
	g.Set("lsb_distrib_release", versionID)
	g.Set("lsb_distrib_codename", firstNonEmpty(rel["VERSION_CODENAME"], rel["UBUNTU_CODENAME"]))
}

// packageArch is the `osarch` grain: the architecture a *package* is
// named for, which is not the architecture the CPU is called.
//
// A Debian machine calls the same processor `arm64` where `uname -m`
// calls it `aarch64`, and `amd64` where uname says `x86_64`. Salt asks
// the package manager for this and so does this: `pkg.installed` on a
// tree that pins an architecture is comparing against whatever dpkg
// prints, so anything else is a comparison that cannot match.
//
// This used to be `goarchToCPU()` -- the `cpuarch` value -- on every
// family, so `osarch` and `cpuarch` were the same grain twice and the
// Debian estate this is aimed at got `aarch64` where every one of its
// Salt trees had been reading `arm64`. A family whose tool is missing
// falls back to the CPU name, which is what Salt does for the families
// that have no package architecture of their own.
func packageArch(family string) string {
	switch family {
	case "Debian":
		if out, err := exec.Command("dpkg", "--print-architecture").Output(); err == nil {
			if arch := strings.TrimSpace(string(out)); arch != "" {
				return arch
			}
		}
	case "RedHat", "Suse":
		if out, err := exec.Command("rpm", "--eval", "%{_host_cpu}").Output(); err == nil {
			if arch := strings.TrimSpace(string(out)); arch != "" && arch != "%{_host_cpu}" {
				return arch
			}
		}
	}
	return goarchToCPU()
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

	g.Set("kernelparams", kernelParams(firstLineOf("/proc/cmdline")))
}

// kernelParams parses /proc/cmdline into the (name, value) pairs Salt
// reports, in the order the kernel was given them.
//
// A mapping is what this used to build, and a mapping cannot hold a
// command line. Parameters repeat: `console=tty1 console=ttyS0` is how
// a cloud image asks for both a virtual and a serial console, and
// hardening guides set `audit_backlog_limit` twice. Keyed by name, the
// second silently replaced the first -- this host boots with twelve
// parameters and reported ten -- so a state auditing boot parameters
// could not see the console it was looking for. A sequence keeps them
// all, and it is also the shape any tree carried over from Salt is
// written against: iterating pairs, not indexing a name.
//
// A parameter with no value gets an empty string, and one whose value
// contains an `=` keeps all of it. That second part is deliberately
// *not* what Salt does: Salt splits on every `=` and discards the value
// unless there are exactly two fields, so `root=UUID=...` is reported
// as `root` with no value at all. Reproducing that would mean losing
// the root filesystem's identity to copy a defect. DIVERGENCE records
// it.
func kernelParams(cmdline string) []any {
	var out []any
	for _, field := range splitCmdline(cmdline) {
		k, v, _ := strings.Cut(field, "=")
		out = append(out, []any{k, strings.Trim(v, `"`)})
	}
	return orEmpty(out)
}

// splitCmdline splits /proc/cmdline into parameters.
//
// Whitespace separates them except inside double quotes, which is how
// the kernel itself parses the line and how a value containing a space
// -- `param="a, b"` -- reaches a single parameter. Splitting on
// whitespace alone tears such a value into two parameters, the second
// of which has no name.
func splitCmdline(cmdline string) []string {
	var fields []string
	var cur strings.Builder
	quoted := false
	flush := func() {
		if cur.Len() > 0 {
			fields = append(fields, cur.String())
			cur.Reset()
		}
	}
	for _, r := range cmdline {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case !quoted && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return fields
}

func collectCPU(g *value.Map) {
	g.Set("cpuarch", goarchToCPU())

	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		g.Set("num_cpus", int64(0))
		g.Set("cpu_model", "Unknown")
		g.Set("cpu_flags", []any{})
		return
	}

	count := 0
	// "Unknown" rather than "" is Salt's own default, and it is the
	// answer on every arm64 machine: /proc/cpuinfo there carries
	// `CPU implementer` and `CPU part` numbers and no model string at
	// all, so there is nothing to report and a tree reading the grain
	// should see that rather than an empty string it may treat as unset.
	model := "Unknown"
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
		case "model name", "Model", "cpu model", "Processor":
			if model == "Unknown" {
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
		// AWS's own hypervisor has a name, and a tree on an AWS estate
		// branches on it. Salt reports `Nitro` here; this reported
		// `kvm`, which is what Nitro is built on and not what anything
		// asks for.
		virt = "Nitro"
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
//
// `disks` and `ssds` partition the devices rather than nesting: a
// device is in `ssds` when the kernel calls it non-rotational and in
// `disks` when it calls it rotational, and in neither when it will not
// say. That is Salt's split, and the difference is not cosmetic -- this
// used to put every device in `disks` and repeat the solid-state ones
// in `ssds`, so on an all-NVMe host, which every current cloud instance
// is, `grains['disks']` listed seven devices where Salt lists none and
// a tree iterating it to find spinning disks found seven that are not.
//
// Devices whose /sys/block entry links into `devices/virtual` are
// skipped, which is how loop, ram and device-mapper nodes are excluded
// as a class rather than by a list of name prefixes that has to grow.
func blockDevices() (disks, ssds []any) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		name := e.Name()
		if target, err := os.Readlink("/sys/block/" + name); err == nil &&
			strings.Contains(target, "devices/virtual/") {
			continue
		}
		switch firstLineOf("/sys/block/" + name + "/queue/rotational") {
		case "0":
			ssds = append(ssds, name)
		case "1":
			disks = append(disks, name)
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

// detectSystemd reports the running init's version and build features,
// in the shape SPEC 14.1 and Salt both use.
//
// It asks `systemctl --version`, which is what Salt's own grain asks,
// because the value's whole purpose is that an existing tree branching
// on `grains['systemd']['version']` means the same thing here. The
// first line is `systemd 249 (249.11-0ubuntu3.22)` and the version is
// its second field -- the bare series, not the distribution's package
// string -- and the second line is the `+PAM +AUDIT ...` feature list
// verbatim.
//
// This is the one grain on Linux that runs a program, and it is worth
// saying why the file sources the rest of this package prefers do not
// answer it: nothing under /proc or /sys carries the version. What used
// to be read was /proc/1/comm, which carries the *process name*, so the
// grain reported the string "systemd" as its version on every Linux
// host and an empty feature list, and no test had ever looked at the
// value. systemd's D-Bus `Manager` exposes `Version` and `Features` and
// would avoid the fork, but it reports the manager's version where Salt
// reports the binary's, and matching Salt is what this grain is for.
func detectSystemd() *value.Map {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return value.MapOf("version", "", "features", "")
	}
	version, features := systemctlVersion()
	return value.MapOf("version", version, "features", features)
}

// systemctlVersion parses `systemctl --version`. A host where it cannot
// be run gets two empty strings rather than a guess: a template reading
// the grain gets nothing, which is the honest answer and the one the
// Windows collector already gives.
func systemctlVersion() (version, features string) {
	out, err := exec.Command("systemctl", "--version").Output()
	if err != nil {
		return "", ""
	}
	lines := strings.Split(string(out), "\n")
	if fields := strings.Fields(lines[0]); len(fields) >= 2 {
		version = fields[1]
	}
	if len(lines) >= 2 {
		features = strings.TrimSpace(lines[1])
	}
	return version, features
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
