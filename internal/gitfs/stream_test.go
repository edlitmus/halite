//go:build unix

package gitfs

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

// **What git writes after the archive's end is read too.** tar ends with
// two zero blocks, and the reader stops there, but `git archive` pads its
// output to a whole record after them. The first version of the streaming
// fix stopped reading at the end marker and waited for git, which could
// not finish writing into a pipe nobody read: on beastie (FreeBSD 15.1),
// with 7 KiB of a 10 KiB record unread, every gitfs update hung until its
// timeout. On Linux the pipe took it and nothing hung, so this git writes
// more than a pipe holds anywhere, and the test fails on every platform.
func TestWhatFollowsTheArchiveIsDrained(t *testing.T) {
	out, err, elapsed := materialiseWithTrailer(t, 256<<10)
	if err != nil {
		t.Fatalf("an archive followed by padding was not unpacked, after %v: %v", elapsed, err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("unpacking took %v: git was waited on rather than read", elapsed)
	}
	if got, err := os.ReadFile(filepath.Join(out, "top.sls")); err != nil || string(got) != "a: 1\n" {
		t.Errorf("top.sls is %q, %v", got, err)
	}
}

// And the drain is bounded: a git that goes on writing after the archive
// is killed, and the update fails rather than reading without end.
func TestWhatFollowsTheArchiveIsBounded(t *testing.T) {
	out, err, elapsed := materialiseWithTrailer(t, 4<<20)
	if err == nil || !strings.Contains(err.Error(), "past what it was asked for") {
		t.Fatalf("4 MiB after the archive was not refused: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the refusal took %v", elapsed)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("a refused archive left a tree: %v", statErr)
	}
}

// materialiseWithTrailer unpacks a one-file archive from a git that
// writes trailer zero bytes after it.
func materialiseWithTrailer(t *testing.T, trailer int) (out string, err error, elapsed time.Duration) {
	t.Helper()
	dir := t.TempDir()
	var arch bytes.Buffer
	tw := tar.NewWriter(&arch)
	body := []byte("a: 1\n")
	if err := tw.WriteHeader(&tar.Header{Name: "top.sls", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "archive.tar")
	if err := os.WriteFile(archive, arch.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "git")
	script := "#!/bin/sh\ncat " + archive + "\nexec head -c " + strconv.Itoa(trailer) + " /dev/zero\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	o := &Options{Git: fake, Timeout: 10 * time.Second}
	start := time.Now()
	out = filepath.Join(dir, "out")
	err = o.materialise(context.Background(), dir, out, Remote{URL: "x"}, Ref{Name: "main", Commit: strings.Repeat("a", 40)})
	return out, err, time.Since(start)
}
