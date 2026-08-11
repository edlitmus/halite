// Package grains collects static facts about the host, mirroring the most
// commonly used Salt grains: os, os_family, osrelease, kernel,
// kernelrelease, arch (cpuarch), num_cpus, mem_total, host, username.
//
// A site's own facts — role, datacentre, tier — come from a static grains
// file (see CustomPath), which is merged over the detected ones. Without
// it, grain targeting could only select on what the host reports about
// itself.
package grains

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
)

// Collect gathers grains for the current host: the facts halite detects,
// with the static custom grains file merged over them.
func Collect() map[string]any {
	return CollectFrom(CustomPath())
}

// CollectFrom is Collect against a named custom grains file. An empty path
// or a missing file leaves only the detected facts; a file that cannot be
// parsed is reported on stderr and otherwise ignored, because a broken
// grains file must not stop a host from converging.
func CollectFrom(path string) map[string]any {
	g := detect()
	if path == "" {
		return g
	}
	custom, err := LoadCustom(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		return g
	}
	// Custom grains win: a site that sets os_family by hand means it.
	for k, v := range custom {
		g[k] = v
	}
	return g
}

// detect gathers the facts halite reads from the host itself.
func detect() map[string]any {
	g := map[string]any{
		"kernel":   kernelName(),
		"arch":     runtime.GOARCH,
		"num_cpus": runtime.NumCPU(),
		"halite":   true,
	}
	if h, err := os.Hostname(); err == nil {
		g["host"] = h
		// Salt's `id` grain. Masterless it is just the hostname; under a
		// control plane the master replaces it with the enrolled identity,
		// which is the name operators target by.
		g["id"] = h
	}
	if u, err := user.Current(); err == nil {
		g["username"] = u.Username
	}

	switch runtime.GOOS {
	case "freebsd":
		g["os"] = "FreeBSD"
		g["os_family"] = "FreeBSD"
		g["osrelease"] = unameR()
		g["kernelrelease"] = unameR()
		g["mem_total"] = memFromSysctl("hw.physmem")
	case "darwin":
		g["os"] = "MacOS"
		g["os_family"] = "MacOS"
		g["osrelease"] = swVers()
		g["kernelrelease"] = unameR()
		g["mem_total"] = memFromSysctl("hw.memsize")
	case "windows":
		g["os"] = "Windows"
		g["os_family"] = "Windows"
		g["kernelrelease"] = ""
	case "openbsd", "netbsd", "dragonfly":
		name := strings.ToUpper(runtime.GOOS[:1]) + runtime.GOOS[1:]
		g["os"] = name
		g["os_family"] = name
		g["osrelease"] = unameR()
		g["kernelrelease"] = unameR()
	default: // linux
		id, versionID, idLike, pretty := osRelease()
		g["os"] = linuxOSName(id)
		g["os_family"] = linuxFamily(id, idLike)
		g["osrelease"] = versionID
		g["osfullname"] = pretty
		g["kernelrelease"] = unameR()
		g["mem_total"] = memFromMeminfo()
	}
	return g
}

func kernelName() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "Darwin"
	case "freebsd":
		return "FreeBSD"
	case "linux":
		return "Linux"
	}
	return runtime.GOOS
}

func capture(name string, arg ...string) string {
	out, err := exec.Command(name, arg...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func unameR() string { return capture("uname", "-r") }

func swVers() string { return capture("sw_vers", "-productVersion") }

func memFromSysctl(key string) int64 {
	v := capture("sysctl", "-n", key)
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n / (1024 * 1024) // MB
	}
	return 0
}

func memFromMeminfo() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "MemTotal:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				if kb, err := strconv.ParseInt(f[1], 10, 64); err == nil {
					return kb / 1024 // MB
				}
			}
		}
	}
	return 0
}

func osRelease() (id, versionID, idLike, pretty string) {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			versionID = v
		case "ID_LIKE":
			idLike = v
		case "PRETTY_NAME":
			pretty = v
		}
	}
	return
}

func linuxOSName(id string) string {
	switch id {
	case "ubuntu":
		return "Ubuntu"
	case "debian":
		return "Debian"
	case "centos":
		return "CentOS"
	case "rhel":
		return "RedHat"
	case "fedora":
		return "Fedora"
	case "rocky":
		return "Rocky"
	case "almalinux":
		return "AlmaLinux"
	case "opensuse-leap", "opensuse-tumbleweed", "sles":
		return "SUSE"
	case "arch":
		return "Arch"
	case "alpine":
		return "Alpine"
	case "":
		return "Linux"
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

func linuxFamily(id, idLike string) string {
	all := id + " " + idLike
	switch {
	case strings.Contains(all, "debian") || strings.Contains(all, "ubuntu"):
		return "Debian"
	case strings.Contains(all, "rhel") || strings.Contains(all, "fedora") ||
		strings.Contains(all, "centos"):
		return "RedHat"
	case strings.Contains(all, "suse"):
		return "Suse"
	case strings.Contains(all, "arch"):
		return "Arch"
	case strings.Contains(all, "alpine"):
		return "Alpine"
	}
	return "Linux"
}
