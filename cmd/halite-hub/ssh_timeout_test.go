package main

import (
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/cli"
)

// A --timeout that is not a positive duration is refused, as `run` refuses
// it, instead of being ignored. DIVERGENCE 5.539.
func TestSSHTimeoutFlagIsCheckedLikeRuns(t *testing.T) {
	for _, bad := range []string{"30", "-5s", "0s", "true", "soon"} {
		args := &cli.Args{Flags: map[string]string{"timeout": bad}}
		if _, err := sshTimeoutFlag(args); err == nil {
			t.Errorf("--timeout %q was accepted", bad)
		}
	}
	for flag, want := range map[string]time.Duration{"": 0, "90s": 90 * time.Second, "2m": 2 * time.Minute} {
		args := &cli.Args{Flags: map[string]string{}}
		if flag != "" {
			args.Flags["timeout"] = flag
		}
		if got, err := sshTimeoutFlag(args); err != nil || got != want {
			t.Errorf("--timeout %q = %v, %v; want %v", flag, got, err, want)
		}
	}
}
