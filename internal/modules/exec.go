package modules

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ExecFunc is a read-only query: it reports facts and never changes the
// host. Execution modules are separate from state functions because they
// answer questions rather than converge anything — most of their value is
// fleet-wide (`halite run '*' call disk.usage`).
type ExecFunc func(c *Ctx, args map[string]any) (map[string]any, error)

// ExecRegistry maps "module.function" to its query implementation. Names
// never collide with the state Registry.
var ExecRegistry = map[string]ExecFunc{}

func registerExec(name string, f ExecFunc) { ExecRegistry[name] = f }

func init() {
	registerExec("disk.usage", diskUsage)
	registerExec("status.uptime", statusUptime)
	registerExec("status.loadavg", statusLoadavg)
	registerExec("network.interfaces", networkInterfaces)
}

// ExecNames lists the registered execution modules, sorted.
func ExecNames() []string {
	names := make([]string, 0, len(ExecRegistry))
	for name := range ExecRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// diskUsage reports space per mounted filesystem, keyed by mount point.
func diskUsage(c *Ctx, args map[string]any) (map[string]any, error) {
	mounts, err := activeMounts()
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for point, entry := range mounts {
		usage, err := statfsUsage(point)
		if err != nil {
			continue // pseudo-filesystems and dead NFS mounts are not worth failing over
		}
		usage["device"] = entry.Device
		usage["fstype"] = entry.FSType
		out[point] = usage
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no filesystem usage available on %s", runtime.GOOS)
	}
	return out, nil
}

// statusUptime reports how long the host has been up.
func statusUptime(c *Ctx, args map[string]any) (map[string]any, error) {
	seconds, err := uptimeSeconds()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"seconds": int64(seconds),
		"since":   time.Now().Add(-time.Duration(seconds) * time.Second).UTC().Format(time.RFC3339),
		"human":   humanDuration(time.Duration(seconds) * time.Second),
	}, nil
}

func uptimeSeconds() (float64, error) {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/uptime")
		if err != nil {
			return 0, fmt.Errorf("read /proc/uptime: %w", err)
		}
		first, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
		return strconv.ParseFloat(first, 64)
	}
	// BSD and macOS: kern.boottime prints "{ sec = 1723..., usec = ... }".
	out, errOut, rc, err := run("sysctl", "-n", "kern.boottime")
	if err != nil || rc != 0 {
		return 0, fmt.Errorf("kern.boottime: %s", cmdError(errOut, err))
	}
	_, rest, found := strings.Cut(out, "sec = ")
	if !found {
		return 0, fmt.Errorf("cannot parse kern.boottime output")
	}
	secText, _, _ := strings.Cut(rest, ",")
	boot, err := strconv.ParseInt(strings.TrimSpace(secText), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse kern.boottime output")
	}
	return time.Since(time.Unix(boot, 0)).Seconds(), nil
}

// statusLoadavg reports the 1, 5, and 15 minute load averages.
func statusLoadavg(c *Ctx, args map[string]any) (map[string]any, error) {
	fields, err := loadavgFields()
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for i, key := range []string{"1-min", "5-min", "15-min"} {
		value, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return nil, fmt.Errorf("cannot parse load average %q", fields[i])
		}
		out[key] = value
	}
	return out, nil
}

func loadavgFields() ([]string, error) {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return nil, fmt.Errorf("read /proc/loadavg: %w", err)
		}
		fields := strings.Fields(string(b))
		if len(fields) < 3 {
			return nil, fmt.Errorf("cannot parse /proc/loadavg")
		}
		return fields[:3], nil
	}
	// BSD and macOS: vm.loadavg prints "{ 0.31 0.28 0.24 }".
	out, errOut, rc, err := run("sysctl", "-n", "vm.loadavg")
	if err != nil || rc != 0 {
		return nil, fmt.Errorf("vm.loadavg: %s", cmdError(errOut, err))
	}
	fields := strings.Fields(strings.Trim(strings.TrimSpace(out), "{} "))
	if len(fields) < 3 {
		return nil, fmt.Errorf("cannot parse vm.loadavg output %q", strings.TrimSpace(out))
	}
	return fields[:3], nil
}

// networkInterfaces reports each interface with its addresses. It is pure
// stdlib, so it behaves the same on every platform.
func networkInterfaces(c *Ctx, args map[string]any) (map[string]any, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	out := map[string]any{}
	for _, iface := range interfaces {
		info := map[string]any{
			"index": iface.Index,
			"mtu":   iface.MTU,
			"up":    iface.Flags&net.FlagUp != 0,
			"flags": iface.Flags.String(),
		}
		if hw := iface.HardwareAddr.String(); hw != "" {
			info["mac"] = hw
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue // an interface that disappeared mid-walk is not an error
		}
		var list []any
		for _, addr := range addrs {
			list = append(list, addr.String())
		}
		if len(list) > 0 {
			info["addresses"] = list
		}
		out[iface.Name] = info
	}
	return out, nil
}

// humanBytes renders a byte count in binary units, as df -h does.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	value, exp := float64(n), 0
	for value >= unit && exp < 5 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", value, "KMGTPE"[exp-1])
}

// humanDuration renders a duration the way `uptime` does.
func humanDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
