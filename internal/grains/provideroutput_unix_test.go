//go:build !windows

package grains

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The 1 MiB a provider may emit was checked after the runner had read
// everything the provider wrote, so a provider that wrote without end
// was held in memory until it stopped or timed out, and the comment on
// the limit said it bounded memory (issue 321, section 5).
//
// This provider writes 64 MiB at once and then hangs. A bounded read
// gives up at the limit, kills it, and has allocated about what the limit
// is; the old one waited out the timeout and had allocated the lot.
func TestAProviderPastTheOutputLimitIsStoppedNotBuffered(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nhead -c 67108864 /dev/zero\nsleep 60\n"
	if err := os.WriteFile(filepath.Join(dir, "20-flood"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	_, warnings := Collect(Options{NodeID: "n", GrainsDir: dir, ProviderTimeout: 8 * time.Second})
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)

	if len(warnings) != 1 || !strings.Contains(warnings[0].Msg, "more than 1 MiB") {
		t.Fatalf("warnings = %v, want one saying the provider emitted more than 1 MiB", warnings)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took %v: the provider was waited out, not stopped at the limit", elapsed)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 24<<20 {
		t.Errorf("collection allocated %d MiB for a 1 MiB limit", allocated>>20)
	}
}
