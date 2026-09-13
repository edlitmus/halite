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

// BusyBox's ps is a third flavour, not a dialect of the other two.
//
// Alpine's ps is BusyBox's, whose whole usage is `ps [-o COLS] [-T]`.
// The Linux spelling this module sent it -- `-eww --no-headers -o ...`
// -- failed with `ps: unrecognized option: w`, taking nine live tests
// with it on a module that works everywhere else. The fixture below is
// what that ps really printed on Alpine 3.24.
func TestBusyboxPSIsParsedAgainstItsOwnColumns(t *testing.T) {
	layout := psLayout{Columns: psBusyboxColumns, Header: true}
	const real = "PID   PPID  USER     RSS  VSZ  STAT COMMAND\n" +
		"    1     0 root      884 1636 S    /sbin/init\n" +
		"    2     0 root        0    0 SW   [kthreadd]\n" +
		"  842     1 nobody   4096 123456 S  /usr/sbin/sshd -D\n"

	got := parsePSColumnsWith(layout, real)
	if len(got) != 3 {
		t.Fatalf("parsed %d processes, want 3: %+v", len(got), got)
	}

	if got[0].PID != 1 || got[0].PPID != 0 || got[0].User != "root" {
		t.Errorf("init row = %+v", got[0])
	}
	if got[0].RSS != 884 || got[0].VSZ != 1636 {
		t.Errorf("init memory = rss %d vsz %d, want 884 and 1636", got[0].RSS, got[0].VSZ)
	}
	// BusyBox spells it `stat`; the module's field is State either way.
	if got[0].State != "S" {
		t.Errorf("init state = %q, want S", got[0].State)
	}
	if got[0].Command != "/sbin/init" {
		t.Errorf("init command = %q", got[0].Command)
	}
	// The command runs to the end of the line, arguments included.
	if got[2].Command != "/usr/sbin/sshd -D" {
		t.Errorf("sshd command = %q, want the arguments kept", got[2].Command)
	}

	// **The percentages are absent, not zero.** BusyBox refuses `%cpu`
	// and `%mem` outright, and reporting 0 would be a claim that these
	// processes are idle.
	for _, p := range got {
		if p.PercentsKnown {
			t.Errorf("pid %d claims to know its percentages on a BusyBox listing", p.PID)
		}
		m := p.Map()
		for _, k := range []string{"cpu_percent", "mem_percent"} {
			if v, _ := m.GetString(k); v != nil {
				t.Errorf("pid %d reports %s = %v; want nil, because this ps cannot say", p.PID, k, v)
			}
		}
	}
}

// The procps listing still reports its percentages, and still parses.
func TestProcpsPercentagesSurviveTheBusyboxChange(t *testing.T) {
	layout := psLayout{Columns: psColumns, Percents: true}
	got := parsePSColumnsWith(layout,
		"  842     1 www   1.5  2.5 4096 123456 S    /usr/sbin/sshd -D\n")
	if len(got) != 1 {
		t.Fatalf("parsed %d, want 1", len(got))
	}
	p := got[0]
	if !p.PercentsKnown || p.CPU != 1.5 || p.Mem != 2.5 {
		t.Errorf("percentages = %v %v (known %v), want 1.5 and 2.5", p.CPU, p.Mem, p.PercentsKnown)
	}
	if v, _ := p.Map().GetString("cpu_percent"); v != 1.5 {
		t.Errorf("cpu_percent = %v, want 1.5", v)
	}
	if p.Command != "/usr/sbin/sshd -D" {
		t.Errorf("command = %q", p.Command)
	}
}

// The Linux argv is only sent to a ps that understands it.
func TestTheBusyboxArgvCarriesNoOptionItRefuses(t *testing.T) {
	busybox := psLayout{
		Argv:    []string{"ps", "-o", strings.Join(psBusyboxColumns, ",")},
		Columns: psBusyboxColumns,
	}
	joined := strings.Join(busybox.Argv, " ")
	for _, refused := range []string{"-eww", "--no-headers", "-axwwo", "%cpu", "%mem"} {
		if strings.Contains(joined, refused) {
			t.Errorf("%q carries %q, which BusyBox's ps refuses", joined, refused)
		}
	}
}
