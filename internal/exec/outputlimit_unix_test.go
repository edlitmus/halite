//go:build !windows

package exec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A command past its MaxOutput is stopped, not read to the end: this one
// would write for a minute, and the run returns in moments with what was
// read up to the limit and an error that errors.Is can name.
func TestACommandPastItsOutputLimitIsStopped(t *testing.T) {
	start := time.Now()
	res, err := (&OSRunner{}).Run(context.Background(), Command{
		Argv:      []string{"sh", "-c", "head -c 4194304 /dev/zero; sleep 60"},
		MaxOutput: 1 << 20,
	})
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("err = %v, want ErrOutputLimit", err)
	}
	if len(res.Stdout) != 1<<20 {
		t.Errorf("kept %d bytes of stdout, want exactly the %d limit", len(res.Stdout), 1<<20)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v: the command was waited out", time.Since(start))
	}
}

// stderr is bounded as well as stdout; a command cannot go around the
// limit by writing to the other stream.
func TestStderrIsBoundedToo(t *testing.T) {
	res, err := (&OSRunner{}).Run(context.Background(), Command{
		Argv:      []string{"sh", "-c", "head -c 4194304 /dev/zero >&2; sleep 60"},
		MaxOutput: 1 << 20,
	})
	if !errors.Is(err, ErrOutputLimit) || len(res.Stderr) > 1<<20 {
		t.Fatalf("err = %v, kept %d bytes of stderr", err, len(res.Stderr))
	}
}

// Without a limit nothing changes: the same volume comes back whole.
// This is the property that keeps the limit from reaching `cmd.run`.
func TestNoLimitMeansNoLimit(t *testing.T) {
	res, err := (&OSRunner{}).Run(context.Background(), Command{
		Argv: []string{"sh", "-c", "head -c 4194304 /dev/zero"},
	})
	if err != nil || len(res.Stdout) != 4<<20 {
		t.Fatalf("err = %v, stdout %d bytes, want 4 MiB whole", err, len(res.Stdout))
	}
}

// Output exactly at the limit is allowed; the limit is a ceiling.
func TestOutputAtTheLimitIsKept(t *testing.T) {
	res, err := (&OSRunner{}).Run(context.Background(), Command{
		Argv:      []string{"sh", "-c", "head -c 1048576 /dev/zero"},
		MaxOutput: 1 << 20,
	})
	if err != nil || len(res.Stdout) != 1<<20 || strings.Contains(res.Stderr, "limit") {
		t.Fatalf("err = %v, stdout %d bytes", err, len(res.Stdout))
	}
}
