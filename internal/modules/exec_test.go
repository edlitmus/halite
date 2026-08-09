package modules

import (
	"runtime"
	"testing"
	"time"
)

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0K"},
		{1536, "1.5K"},
		{1024 * 1024, "1.0M"},
		{3 * 1024 * 1024 * 1024, "3.0G"},
		{2 * 1024 * 1024 * 1024 * 1024, "2.0T"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "0m"},
		{90 * time.Second, "1m"},
		{2 * time.Hour, "2h 0m"},
		{25*time.Hour + 30*time.Minute, "1d 1h 30m"},
	}
	for _, c := range cases {
		if got := humanDuration(c.in); got != c.want {
			t.Errorf("humanDuration(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExecModulesDoNotCollideWithStates(t *testing.T) {
	for name := range ExecRegistry {
		if _, clash := Registry[name]; clash {
			t.Errorf("%q is registered as both a state and an execution module", name)
		}
	}
}

func TestNetworkInterfacesReportsLoopback(t *testing.T) {
	data, err := networkInterfaces(&Ctx{}, nil)
	if err != nil {
		t.Fatalf("network.interfaces: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("no interfaces reported")
	}
	// Every host has a loopback with an address; finding one proves the
	// address walk works, without depending on this machine's hardware.
	found := false
	for _, value := range data {
		info, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("interface entry is %T, want a map", value)
		}
		if addrs, ok := info["addresses"].([]any); ok && len(addrs) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("no interface reported an address")
	}
}

func TestStatusUptimeIsPositive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uptime is unix-only")
	}
	data, err := statusUptime(&Ctx{}, nil)
	if err != nil {
		t.Fatalf("status.uptime: %v", err)
	}
	seconds, ok := data["seconds"].(int64)
	if !ok || seconds <= 0 {
		t.Errorf("seconds = %v, want a positive count", data["seconds"])
	}
}

func TestStatusLoadavgReportsThreeWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("load average is unix-only")
	}
	data, err := statusLoadavg(&Ctx{}, nil)
	if err != nil {
		t.Fatalf("status.loadavg: %v", err)
	}
	for _, key := range []string{"1-min", "5-min", "15-min"} {
		value, ok := data[key].(float64)
		if !ok || value < 0 {
			t.Errorf("%s = %v, want a non-negative number", key, data[key])
		}
	}
}

func TestDiskUsageCoversTheRootFilesystem(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("disk usage is not implemented on Windows")
	}
	data, err := diskUsage(&Ctx{}, nil)
	if err != nil {
		t.Fatalf("disk.usage: %v", err)
	}
	root, ok := data["/"].(map[string]any)
	if !ok {
		t.Fatalf("no entry for /: %v", data)
	}
	total, ok := root["total_bytes"].(uint64)
	if !ok || total == 0 {
		t.Errorf("total_bytes = %v, want a non-zero size", root["total_bytes"])
	}
	if _, ok := root["capacity"].(string); !ok {
		t.Errorf("capacity missing from %v", root)
	}
}
