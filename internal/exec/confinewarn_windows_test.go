package exec

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// Issue 608 item 2. When the job object could not be made, or the child
// could not be put in it, the command ran unconfined and nothing said so.
// Neither failure happens on a healthy machine on demand, so the calls
// are made to fail; what is under test is what happens next.
func TestAnUnconfinedCommandIsSaidOnce(t *testing.T) {
	for _, c := range []struct {
		what    string
		breakIt func() func()
	}{
		{"the job object cannot be created", func() func() {
			was := createJobObject
			createJobObject = func(*windows.SecurityAttributes, *uint16) (windows.Handle, error) {
				return 0, windows.ERROR_NOT_ENOUGH_QUOTA
			}
			return func() { createJobObject = was }
		}},
		{"the child cannot be assigned to it", func() func() {
			was := assignProcessToJobObject
			assignProcessToJobObject = func(windows.Handle, windows.Handle) error {
				return windows.ERROR_ACCESS_DENIED
			}
			return func() { assignProcessToJobObject = was }
		}},
	} {
		t.Run(c.what, func(t *testing.T) {
			defer c.breakIt()()
			confinementWarned.Store(false)
			t.Cleanup(func() { confinementWarned.Store(false) })

			var said []string
			r := &OSRunner{Warn: func(msg string) { said = append(said, msg) }}
			for i := range 2 {
				res, err := r.Run(context.Background(), helperCommand("echo", "ran"))
				if err != nil {
					t.Fatalf("run %d: an unconfined command should still run: %v", i, err)
				}
				if strings.TrimSpace(res.Stdout) != "ran" {
					t.Fatalf("run %d: stdout = %q", i, res.Stdout)
				}
			}
			if len(said) != 1 {
				t.Fatalf("the runner said %d things about confinement over two commands, want 1: %q", len(said), said)
			}
			if !strings.Contains(said[0], "without process confinement") {
				t.Errorf("the warning does not say confinement is off: %q", said[0])
			}
		})
	}
}

// And a command that was confined says nothing.
func TestAConfinedCommandSaysNothing(t *testing.T) {
	confinementWarned.Store(false)
	t.Cleanup(func() { confinementWarned.Store(false) })
	var said []string
	r := &OSRunner{Warn: func(msg string) { said = append(said, msg) }}
	if _, err := r.Run(context.Background(), helperCommand("echo", "ran")); err != nil {
		t.Fatal(err)
	}
	if len(said) != 0 {
		t.Errorf("a confined command produced a warning: %q", said)
	}
}
