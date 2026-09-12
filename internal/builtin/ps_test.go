package builtin

import (
	"strings"
	"testing"
)

// The two rules in this module that are not the machine's business.
//
// Everything else about `ps` is checked against the real process table
// in live_ps_test.go, which is the right way round: the parser's job is
// to agree with what the tool printed. These two are the parts where a
// tool's output is not the question -- what to do with a command line
// that holds spaces, and which signals to accept -- so they are checked
// here, where the input can be chosen.

// A command line holds spaces and nothing else on the row does. Getting
// that wrong truncates every command at its first argument, which is
// exactly the field `pgrep --full` matches on.
func TestTheCommandIsEverythingAfterTheFixedColumns(t *testing.T) {
	// Real shapes: a plain command, one with arguments, one with a
	// quoted argument holding spaces, a kernel thread in brackets, and
	// a header line of the kind a BSD ps prints.
	out := parsePSColumns(strings.Join([]string{
		"  PID  PPID USER %CPU %MEM  RSS    VSZ STAT COMMAND",
		"    1     0 root  0.0  0.1 1234  56789 Ss   /sbin/init",
		"  842     1 www   1.5  2.5 4096 123456 S    /usr/bin/java -Xmx2g -jar /opt/app.jar --profile=prod",
		"  907   842 www   0.0  0.1  512  65536 S    [kworker/0:1]",
		" 1024     1 ed   12.0  0.5 2048  99999 R+   /bin/sh -c read line # marker",
	}, "\n"))

	if len(out) != 4 {
		t.Fatalf("parsed %d rows, want 4 and no header", len(out))
	}

	for _, want := range []struct {
		pid     int64
		name    string
		command string
	}{
		{1, "init", "/sbin/init"},
		{842, "java", "/usr/bin/java -Xmx2g -jar /opt/app.jar --profile=prod"},
		{907, "kworker/0:1", "[kworker/0:1]"},
		{1024, "sh", "/bin/sh -c read line # marker"},
	} {
		var got *psProcess
		for i := range out {
			if out[i].PID == want.pid {
				got = &out[i]
				break
			}
		}
		if got == nil {
			t.Errorf("no row for pid %d", want.pid)
			continue
		}
		if got.Command != want.command {
			t.Errorf("pid %d command is %q, want %q", want.pid, got.Command, want.command)
		}
		if got.Name() != want.name {
			t.Errorf("pid %d name is %q, want %q", want.pid, got.Name(), want.name)
		}
	}

	// The numeric columns are numbers rather than text, because a
	// threshold is what a beacon will compare them against.
	first := out[0]
	if first.PPID != 0 || first.RSS != 1234 || first.VSZ != 56789 || first.Mem != 0.1 {
		t.Errorf("the first row parsed as %+v", first)
	}
	if out[3].CPU != 12.0 {
		t.Errorf("a processor share of 12.0 parsed as %v", out[3].CPU)
	}
	if first.State != "Ss" {
		t.Errorf("the state parsed as %q", first.State)
	}
}

// A row that is too short is dropped rather than read into the wrong
// fields. Truncated output is what a killed `ps` leaves behind, and
// half a row assigned to the wrong columns is worse than no row.
func TestAShortRowIsDropped(t *testing.T) {
	out := parsePSColumns("    1     0 root  0.0\n  842     1 www   1.5  2.5 4096 123456 S    /usr/sbin/sshd\n")
	if len(out) != 1 {
		t.Fatalf("parsed %d rows, want only the complete one", len(out))
	}
	if out[0].PID != 842 {
		t.Errorf("kept pid %d", out[0].PID)
	}
}

func TestASignalIsNormalisedOrRefused(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"", "TERM"},
		{"TERM", "TERM"},
		{"term", "TERM"},
		{"SIGKILL", "KILL"},
		{"sigkill", "KILL"},
		{"9", "9"},
		{"0", "0"},
	} {
		got, err := psSignal(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q became %q, want %q", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{
		// Real on a BSD and absent on Linux. A tree naming it would
		// work on one platform and fail on the other at the moment it
		// was wanted.
		"INFO",
		"SIGINFO",
		// Not a signal at all.
		"STOPIT",
		"-9",
		"99",
	} {
		if got, err := psSignal(bad); err == nil {
			t.Errorf("%q was accepted as %q", bad, got)
		}
	}
}
