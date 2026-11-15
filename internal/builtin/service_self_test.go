package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The lines these tests read are captures, not inventions. The FreeBSD
// ones came off beastie (FreeBSD 15.1-RELEASE-p6); the cgroup shapes came
// from the measurement in issue 728 on system76-pc and from cgroup v1's
// three-field line. DIVERGENCE 5.664.

func TestSystemdSelfUnitsReadsEveryUnitOnTheCgroupPath(t *testing.T) {
	cases := []struct {
		name    string
		cgroup  string
		want    []string
		isSelf  string
		notSelf string
	}{
		{
			name:    "cgroup v2, the measurement in issue 728",
			cgroup:  "0::/system.slice/halite-node.service\n",
			want:    []string{"halite-node.service"},
			isSelf:  "halite-node",
			notSelf: "halite-hub",
		},
		{
			name:    "a unit's own subgroup is still that unit",
			cgroup:  "0::/system.slice/halite-node.service/payload\n",
			want:    []string{"halite-node.service"},
			isSelf:  "halite-node.service",
			notSelf: "payload",
		},
		{
			name:   "cgroup v1 names the path after the controller list",
			cgroup: "4:pids:/system.slice/halite-node.service\n3:cpu,cpuacct:/system.slice/halite-node.service\n",
			want:   []string{"halite-node.service", "halite-node.service"},
			isSelf: "halite-node",
		},
		{
			name:   "a transient scope is a unit too",
			cgroup: "0::/user.slice/user-1000.slice/session-3.scope\n",
			want:   []string{"session-3.scope"},
		},
		{
			name:   "a unit nested under another is stopped by either",
			cgroup: "0::/user.slice/user-1000.slice/user@1000.service/app.slice/thing.service\n",
			want:   []string{"user@1000.service", "thing.service"},
			isSelf: "user@1000",
		},
		{
			name:   "no unit at all",
			cgroup: "0::/\n",
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cgroup")
			if err := os.WriteFile(path, []byte(tc.cgroup), 0o600); err != nil {
				t.Fatal(err)
			}
			restore := selfCgroupPath
			selfCgroupPath = path
			defer func() { selfCgroupPath = restore }()

			got, err := systemdSelfUnits()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("systemdSelfUnits() = %q, want %q", got, tc.want)
			}
			if tc.isSelf != "" {
				self, err := (systemdProvider{}).RunsThisProcess(nil, tc.isSelf)
				if err != nil || !self {
					t.Errorf("RunsThisProcess(%q) = %v, %v; want true, nil", tc.isSelf, self, err)
				}
			}
			if tc.notSelf != "" {
				self, err := (systemdProvider{}).RunsThisProcess(nil, tc.notSelf)
				if err != nil || self {
					t.Errorf("RunsThisProcess(%q) = %v, %v; want false, nil", tc.notSelf, self, err)
				}
			}
		})
	}
}

func TestFreebsdRCServicePIDsReadsTheStatusLineRCSubrPrints(t *testing.T) {
	cases := []struct {
		name    string
		stdout  string
		want    []int
		wantErr bool
	}{
		// Captured on beastie: `service sshd status`.
		{name: "one pid", stdout: "sshd is running as pid 10335.\n", want: []int{10335}},
		// Captured on beastie: `service nfsd status` on a host serving
		// NFS. This is the multi-pid line occurring in base, with no
		// contrivance -- `nfsd: master (nfsd)` at 10303 and its child // lexicon:allow
		// `nfsd: server (nfsd)` at 10304, both matched because
		// _find_processes compares the first word of the command against
		// `basename procname` plus a colon. It is also exactly the shape
		// the guard cares about, a parent and a child.
		{name: "two pids, nfsd in base", stdout: "nfsd is running as pid 10303 10304.\n", want: []int{10303, 10304}},
		// Captured on beastie from a throwaway rc.d script with no
		// pidfile whose procname matched three processes. Kept after nfsd
		// turned up, because nothing in the wild on this host printed
		// more than two and the parser takes an unbounded list.
		{name: "several pids", stdout: "haliteprobemulti is running as pid 3886 3887 3888.\n", want: []int{3886, 3887, 3888}},
		// rc.subr's other line, from the same script before they started.
		{name: "not running", stdout: "haliteprobemulti is not running.\n", want: nil},
		{name: "nothing at all", stdout: "", want: nil},
		// A line whose shape nobody anticipated must be an error, not an
		// empty list: an empty list reads as "nothing to stop", which
		// would turn a broken parser into an absent guard.
		{name: "a word where a pid belongs", stdout: "x is running as pid many.\n", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCtx(false)
			c.Runner = &exec.RecordingRunner{Default: exec.Result{Stdout: tc.stdout}}
			got, err := freebsdRCServicePIDs(c, "x")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("freebsdRCServicePIDs() = %v, nil; want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("freebsdRCServicePIDs() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("freebsdRCServicePIDs() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// capturedPSTable is `ps -ax -o pid= -o ppid=` as FreeBSD's ps prints it,
// trimmed to one chain: 266 is a halite-node started by daemon(8) 265,
// 4100 is a shell the agent ran, and 9999 is unrelated.
const capturedPSTable = `    0     0
    1     0
  265     1
  266   265
 4100   266
 9999     1
`

func TestProcessAncestryWalksToTheTopAndStopsThere(t *testing.T) {
	cases := []struct {
		pid  int
		want []int
		not  []int
	}{
		{pid: 4100, want: []int{4100, 266, 265, 1}, not: []int{9999, 0}},
		{pid: 266, want: []int{266, 265, 1}, not: []int{4100, 9999}},
		{pid: 9999, want: []int{9999, 1}, not: []int{265, 266, 4100}},
	}
	for _, tc := range cases {
		c := newCtx(false)
		c.Runner = &exec.RecordingRunner{Default: exec.Result{Stdout: capturedPSTable}}
		got, err := processAncestry(c, tc.pid)
		if err != nil {
			t.Fatal(err)
		}
		for _, pid := range tc.want {
			if !got[pid] {
				t.Errorf("ancestry of %d is missing %d: %v", tc.pid, pid, got)
			}
		}
		for _, pid := range tc.not {
			if got[pid] {
				t.Errorf("ancestry of %d wrongly holds %d: %v", tc.pid, pid, got)
			}
		}
	}
}

// freebsdSelfCtx is a context that looks like a FreeBSD host where
// `service <name> status` names servicePID and the process table is the
// captured one, with this process at pid.
//
// It skips where the rc provider is not the one the module would pick,
// which is Windows: the service control manager's `Available` is
// unconditionally true and it comes first, so no faked `Lookup` can put
// the rc provider in front of it -- and it is an API rather than a
// binary, so a request would go to the real one. The skip is a check and
// not a guess: the provider is *asked*, so a selection change on Linux
// or macOS turns these tests off loudly rather than quietly passing
// against the wrong provider.
func freebsdSelfCtx(t *testing.T, test bool, servicePID string, pid int) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	c := newCtx(test)
	rec := &exec.RecordingRunner{
		Responses: map[string]exec.Result{
			"service halite_node status":   {Stdout: "halite_node is running as pid " + servicePID + ".\n"},
			"ps -ax -o pid= -o ppid=":      {Stdout: capturedPSTable},
			"service halite_node rcvar":    {Stdout: "halite_node_enable=\"YES\"\n"},
			"service halite_node enabled":  {},
			"service halite_node restart":  {},
			"service halite_node reload":   {},
			"service halite_node onestart": {},
		},
	}
	c.Runner = rec
	c.Lookup = func(name string) string {
		if name == "service" || name == "sysrc" {
			return "/usr/sbin/" + name
		}
		return ""
	}
	p, err := pickServiceProvider(c)
	if err != nil {
		t.Fatalf("no service provider was available for a context that looks like FreeBSD: %v", err)
	}
	if p.Name() != "freebsd_service" {
		t.Skipf("the module picks the %s provider here, and this test is about the rc one", p.Name())
	}
	restore := selfPID
	selfPID = func() int { return pid }
	t.Cleanup(func() { selfPID = restore })
	return c, rec
}

// The refusal of issue 728: a `watch` on the agent's own service, which
// is how anyone would automate the handbook's restart step, must fail
// rather than stop the agent in the middle of the job.
func TestAWatchOnTheServiceRunningThisProcessIsRefused(t *testing.T) {
	for _, test := range []bool{false, true} {
		// pid 4100 is a descendant of the agent at 266, so it is caught
		// through its ancestry as well as directly.
		for _, pid := range []int{266, 4100} {
			c, rec := freebsdSelfCtx(t, test, "266", pid)
			res, err := New().States.CallWatch(c, "service.running", value.MapOf("name", "halite_node"))
			if err != nil {
				t.Fatal(err)
			}
			if res.Result == nil || *res.Result {
				t.Fatalf("test=%v pid=%d: result = %v, want false", test, pid, res.Result)
			}
			for _, want := range []string{
				"is the one running this process",
				// The consequence is the rc provider's, not systemd's:
				// the two differ and the message must not borrow the
				// worse one.
				"the service would stay down, because the restart runs as a child of this process",
				"daemon -f /bin/sh -c 'sleep 30; service halite_node restart'",
				"without a `watch`",
			} {
				if !strings.Contains(res.Comment, want) {
					t.Errorf("test=%v pid=%d: comment does not name %q:\n%s", test, pid, want, res.Comment)
				}
			}
			for _, ran := range rec.RanCommands() {
				if strings.HasSuffix(ran, "restart") || strings.HasSuffix(ran, "reload") {
					t.Errorf("test=%v pid=%d: it ran %q anyway", test, pid, ran)
				}
			}
		}
	}
}

// And the same service watched from a process that is not under it --
// `halite-node state apply --local`, or the agent restarting some other
// service -- goes ahead. A guard that refused this would be worse than
// none: the restart is safe and is the point of `watch`.
func TestAWatchOnAServiceThisProcessIsNotUnderStillRestarts(t *testing.T) {
	c, rec := freebsdSelfCtx(t, false, "266", 9999)
	res, err := New().States.CallWatch(c, "service.running", value.MapOf("name", "halite_node"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("result = %v (%s), want true", res.Result, res.Comment)
	}
	if !containsCommand(rec.RanCommands(), "service halite_node restart") {
		t.Fatalf("it did not restart: %q", rec.RanCommands())
	}
}

// The execution functions take the same guard, because `service.restart`
// called directly is the same code path -- which issue 728 listed as not
// verified.
func TestTheServiceFunctionsThatStopThisProcessAreRefused(t *testing.T) {
	refused := map[string]bool{
		"service.restart":      true,
		"service.reload":       true,
		"service.force_reload": true,
		// Not guarded, and deliberately: these do what they say. Stopping
		// the agent's own service stops it, which is what was asked for.
		"service.stop":    false,
		"service.start":   false,
		"service.enable":  false,
		"service.disable": false,
	}
	r := New()
	for function, wantRefusal := range refused {
		c, _ := freebsdSelfCtx(t, false, "266", 266)
		_, err := r.Exec.Call(c, function, value.MapOf("name", "halite_node"))
		if wantRefusal {
			if err == nil {
				t.Errorf("%s: no error, want the self-action refusal", function)
				continue
			}
			if !strings.Contains(err.Error(), "is the one running this process") {
				t.Errorf("%s: %v, want the self-action refusal", function, err)
			}
			continue
		}
		if err != nil && strings.Contains(err.Error(), "is the one running this process") {
			t.Errorf("%s: refused, and it should not be: %v", function, err)
		}
	}
}

// A provider that cannot answer the question leaves the guard silent
// rather than refusing on a guess, and a failed reading says so in the
// log instead of deciding either way.
func TestTheGuardIsSilentWhereItCannotKnow(t *testing.T) {
	c := newCtx(false)
	if refusal := serviceSelfActionRefusal(fakeBootProvider{}, c, "x", "restarting"); refusal != "" {
		t.Errorf("a provider with no RunsThisProcess refused: %s", refusal)
	}

	c = newCtx(false)
	c.Runner = &exec.RecordingRunner{Default: exec.Result{Stdout: "x is running as pid nonsense.\n"}}
	var logged []string
	c.Log = func(level, msg string) { logged = append(logged, level+": "+msg) }
	if refusal := serviceSelfActionRefusal(freebsdRCProvider{}, c, "x", "restarting"); refusal != "" {
		t.Errorf("an unreadable status refused: %s", refusal)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "guard did not run") {
		t.Errorf("the failed reading was not reported: %q", logged)
	}
}

func containsCommand(ran []string, want string) bool {
	for _, c := range ran {
		if c == want {
			return true
		}
	}
	return false
}

// Each provider says what its own init system does, and they must not be
// the same sentence: 5.663 called Linux "clean" because the service comes
// back there, and a refusal that claimed otherwise on systemd would be
// making the claim this guard exists to refuse. Both halves were
// measured -- DIVERGENCE 5.663.1.
func TestEachProviderStatesItsOwnConsequence(t *testing.T) {
	sd := systemdProvider{}.SelfActionConsequence()
	rc := freebsdRCProvider{}.SelfActionConsequence()
	if sd == rc {
		t.Fatalf("both providers give the same consequence: %q", sd)
	}
	if !strings.Contains(sd, "systemd starts the service again") {
		t.Errorf("the systemd consequence does not say the service comes back: %q", sd)
	}
	if strings.Contains(sd, "stay down") {
		t.Errorf("the systemd consequence claims the service stays down, and it does not: %q", sd)
	}
	if !strings.Contains(rc, "stay down") {
		t.Errorf("the rc consequence does not say the service stays down: %q", rc)
	}
	// The half that is true on both, and the half that makes it matter.
	for name, consequence := range map[string]string{"systemd": sd, "freebsd rc": rc} {
		if !strings.Contains(consequence, "never return") {
			t.Errorf("the %s consequence does not say the job never returns: %q", name, consequence)
		}
	}
}
