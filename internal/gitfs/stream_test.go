//go:build unix

package gitfs

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// **An archive past the bound is refused as it arrives, not after it is
// all in memory** (issue 321, M-53). `git archive` was read whole into a
// buffer and only then checked, so a branch with a multi-gigabyte tree
// exhausted the hub's memory on every update. The `git` here writes a tar
// header for a 3 GiB file and then zeros without end; streaming refuses
// at the header and kills it, where buffering read until the timeout. The
// heap is sampled so a regression to buffering fails even on a machine
// with the memory to survive it.
func TestAnArchivePastTheBoundIsRefusedWithoutReadingIt(t *testing.T) {
	dir := t.TempDir()
	var hdr bytes.Buffer
	tw := tar.NewWriter(&hdr)
	if err := tw.WriteHeader(&tar.Header{Name: "big.sls", Mode: 0o644, Size: 3 << 30, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	header := filepath.Join(dir, "header.tar")
	if err := os.WriteFile(header, hdr.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "git")
	script := "#!/bin/sh\ncat " + header + "\nexec cat /dev/zero\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	o := &Options{Git: fake, Timeout: 20 * time.Second}
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	err := o.materialise(context.Background(), dir, filepath.Join(dir, "out"), Remote{URL: "x"}, Ref{Name: "main", Commit: strings.Repeat("a", 40)})
	elapsed := time.Since(start)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("a 3 GiB entry was not refused by size: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the refusal took %v; the archive was read rather than refused as it arrived", elapsed)
	}
	if grew := int64(after.TotalAlloc) - int64(before.TotalAlloc); grew > 64<<20 {
		t.Errorf("unpacking allocated %d MiB before refusing", grew>>20)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "out")); !os.IsNotExist(statErr) {
		t.Errorf("a refused archive left a tree: %v", statErr)
	}
}
