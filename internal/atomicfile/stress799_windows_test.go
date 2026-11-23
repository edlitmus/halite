package atomicfile

// TEMPORARY, issue 799: a measurement, never merged. It runs the replace
// against a concurrent read many times and logs which call failed and
// with what error, so a fix can be compared with the code it replaces on
// the same runner.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	stressRounds = 20
	stressOps    = 200
)

// readSharedOnce is Read's open without Read's retry: what a reader that
// shares everything meets on a single attempt.
func readSharedOnce(path string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	return io.ReadAll(f)
}

// writeOnce is Write with the given rename and no gate.
func writeOnce(path string, data []byte, rename func(string, string) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	tmp.Close()
	if err := rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

type stressCase struct {
	name  string
	write func(path string, data []byte) error
	read  func(path string) ([]byte, error)
}

type tally struct {
	mu       sync.Mutex
	writes   int
	reads    int
	writeErr map[string]int
	readErr  map[string]int
	torn     int
	slowest  time.Duration
}

// shape strips the temporary names out of an error so that equal errors
// count together.
func shape(err error, dir string) string {
	s := strings.ReplaceAll(err.Error(), dir, "<dir>")
	if i := strings.Index(s, "<dir>\\.job.json."); i >= 0 {
		j := i + len("<dir>\\.job.json.")
		k := j
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		s = s[:j] + "N" + s[k:]
	}
	return s
}

func runStressCase(t *testing.T, c stressCase) *tally {
	tl := &tally{writeErr: map[string]int{}, readErr: map[string]int{}}
	old := bytes.Repeat([]byte("o"), 4096)
	newer := bytes.Repeat([]byte("n"), 4096)
	for round := 0; round < stressRounds; round++ {
		dir := t.TempDir()
		path := filepath.Join(dir, "job.json")
		if err := os.WriteFile(path, old, 0o600); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < stressOps; i++ {
				body := old
				if i%2 == 0 {
					body = newer
				}
				start := time.Now()
				err := c.write(path, body)
				took := time.Since(start)
				tl.mu.Lock()
				tl.writes++
				if took > tl.slowest {
					tl.slowest = took
				}
				if err != nil {
					tl.writeErr[shape(err, dir)]++
				}
				tl.mu.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < stressOps; i++ {
				b, err := c.read(path)
				tl.mu.Lock()
				tl.reads++
				if err != nil {
					tl.readErr[shape(err, dir)]++
				} else if !bytes.Equal(b, old) && !bytes.Equal(b, newer) {
					tl.torn++
				}
				tl.mu.Unlock()
			}
		}()
		wg.Wait()
	}
	return tl
}

func report(t *testing.T, name string, tl *tally) {
	failedWrites, failedReads := 0, 0
	var lines []string
	for e, n := range tl.writeErr {
		failedWrites += n
		lines = append(lines, fmt.Sprintf("    write %5d  %s", n, e))
	}
	for e, n := range tl.readErr {
		failedReads += n
		lines = append(lines, fmt.Sprintf("    read  %5d  %s", n, e))
	}
	sort.Strings(lines)
	t.Logf("STRESS799 %-40s writes %d failed %d | reads %d failed %d | torn %d | slowest write %v",
		name, tl.writes, failedWrites, tl.reads, failedReads, tl.torn, tl.slowest)
	for _, l := range lines {
		t.Logf("STRESS799 %s", l)
	}
}

func TestStress799ReplaceAgainstRead(t *testing.T) {
	if os.Getenv("HALITE_ATOMICFILE_STRESS") == "" {
		t.Skip("HALITE_ATOMICFILE_STRESS is not set; this measurement runs only on the issue 799 draft")
	}
	cases := []stressCase{
		// The primitive under the package, with no retry on either side:
		// how often the race is really met.
		{"raw MoveFileEx vs raw shared read", func(p string, d []byte) error { return writeOnce(p, d, os.Rename) }, readSharedOnce},
		// The package's Rename, no retry on the reader.
		{"Rename vs raw shared read", func(p string, d []byte) error { return writeOnce(p, d, Rename) }, readSharedOnce},
		// What callers use: Write and Read, both with their retries.
		{"Write vs Read", func(p string, d []byte) error { return Write(p, d, 0o600) }, Read},
		// A reader that does not share delete, as os.ReadFile opens.
		{"Write vs os.ReadFile", func(p string, d []byte) error { return Write(p, d, 0o600) }, os.ReadFile},
	}
	for _, c := range cases {
		report(t, c.name, runStressCase(t, c))
	}
	if fat := os.Getenv("HALITE_ATOMICFILE_FAT_DIR"); fat != "" {
		stressOnFAT(t, fat)
	}
}

// stressOnFAT replaces a file on a FAT volume, which has no POSIX rename
// semantics, so that the fallback is exercised by the filesystem rather
// than by a stub.
func stressOnFAT(t *testing.T, fat string) {
	dir, err := os.MkdirTemp(fat, "stress799-")
	if err != nil {
		t.Fatalf("the FAT directory %s is not usable: %v", fat, err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "job.json")
	if err := Write(path, []byte("old"), 0o600); err != nil {
		t.Errorf("STRESS799 FAT first write: %v", err)
		return
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Errorf("STRESS799 FAT replace: %v", err)
		return
	}
	got, err := Read(path)
	t.Logf("STRESS799 FAT replace on %s: read back %q, %v", fat, got, err)
	probeFATPrimitive(t, dir)
}
