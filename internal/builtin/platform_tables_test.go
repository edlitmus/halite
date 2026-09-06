package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
)

// These check the per-platform tables from whatever host is running,
// which is the whole point of them being tables.
//
// The audit that produced this file came out of two defects found within
// a week of each other, both the same shape and neither in a module: the
// macOS timezone fixture forced the file-writing path on a platform that
// drives `systemsetup`, and the hostname fixture forced it on FreeBSD,
// which reads rc.conf through `sysrc`. In both cases a test existed, ran,
// and passed, while exercising a branch the platform does not take.
//
// The general problem is that a decision made from `runtime.GOOS` can
// only be checked on the platform it decides for, so every branch but
// one is unreachable from any given host — and this project has four
// platforms and, until this week, automation for one. internal/config
// solved it years ago by passing the platform as an argument;
// `sysctlConfFor` now does the same. Where a decision is already a table
// keyed by platform, as `hashLocations` is, the fix is only to assert it.
//
// What this file cannot do is check behaviour: whether `sysrc` is the
// right way to read a FreeBSD hostname is a question for a FreeBSD
// runner, which now exists. It checks the part that is data.

// Every platform's sysctl file is the one that platform's convention
// says, checked from anywhere.
func TestTheSysctlFileIsRightForEveryPlatform(t *testing.T) {
	for goos, want := range map[string]string{
		// Linux has a drop-in directory, and using it keeps an estate's
		// setting out of the file the distribution also edits.
		"linux": "/etc/sysctl.d/99-halite.conf",
		// The BSDs and macOS have one file.
		"freebsd":   "/etc/sysctl.conf",
		"openbsd":   "/etc/sysctl.conf",
		"netbsd":    "/etc/sysctl.conf",
		"dragonfly": "/etc/sysctl.conf",
		"darwin":    "/etc/sysctl.conf",
	} {
		if got := sysctlConfFor(goos); got != want {
			t.Errorf("sysctlConfFor(%q) = %q, want %q", goos, got, want)
		}
	}
	// And the running platform's value is what the package actually
	// uses, so the table is not a second opinion nobody reads.
	if SysctlConfPath != sysctlConfFor(runtime.GOOS) {
		t.Errorf("SysctlConfPath is %q and the table says %q",
			SysctlConfPath, sysctlConfFor(runtime.GOOS))
	}
}

// The password hash file and its field are right for each platform this
// build knows.
//
// The field index is the part worth pinning. Both files are colon
// separated and the hash is the second field in both — but they are not
// the same format, and a wrong index would read a UID or a date and
// compare it against a hash. That comparison never matches, so the state
// would set the password on every run, on a platform nobody develops on,
// and report a change every time rather than failing.
func TestThePasswordHashLocationsAreRight(t *testing.T) {
	for goos, want := range map[string]passwordFile{
		// name:hash:uid:gid:class:change:expire:gecos:home:shell
		"freebsd": {"/etc/master.passwd", 1}, // lexicon:allow — the filename FreeBSD uses
		// name:hash:lastchange:min:max:warn:inactive:expire
		"linux": {"/etc/shadow", 1},
	} {
		got, ok := hashLocations[goos]
		if !ok {
			t.Errorf("this build does not know where %s keeps password hashes", goos)
			continue
		}
		if got != want {
			t.Errorf("hashLocations[%q] = %+v, want %+v", goos, got, want)
		}
	}

	// darwin and windows are handled before the table is consulted, so
	// they must *not* be in it: an entry there would be a file path this
	// build would read on a platform that keeps no such file.
	for _, goos := range []string{"darwin", "windows"} {
		if loc, ok := hashLocations[goos]; ok {
			t.Errorf("%s has a table entry (%s) and is handled separately; one of the two is wrong",
				goos, loc.path)
		}
	}
}

// A symlink target is turned into a zone name on every platform's
// spelling of where the zone files live.
//
// macOS is the case worth having: /etc/localtime there points into
// /var/db/timezone/zoneinfo rather than /usr/share/zoneinfo, so a reader
// that stripped a fixed prefix would return nothing and the state would
// never converge. Taking the last `/zoneinfo/` is what makes all of
// these work, and this is where that is asserted rather than inferred.
func TestAZoneNameIsReadFromEveryPlatformsLayout(t *testing.T) {
	for _, tc := range []struct {
		what   string
		target string
		want   string
	}{
		{"linux", "/usr/share/zoneinfo/America/Denver", "America/Denver"},
		{"macOS", "/var/db/timezone/zoneinfo/America/Denver", "America/Denver"},
		{"a relative target, which is what ln -s often writes",
			"../usr/share/zoneinfo/Europe/London", "Europe/London"},
		{"a zone with no region", "/usr/share/zoneinfo/UTC", "UTC"},
		{"a three-part zone", "/usr/share/zoneinfo/America/Argentina/Salta", "America/Argentina/Salta"},
	} {
		if got := zoneFromPath(tc.target); got != tc.want {
			t.Errorf("%s: zoneFromPath(%q) = %q, want %q", tc.what, tc.target, got, tc.want)
		}
	}
	// A target that is not under a zoneinfo directory at all has no zone
	// name in it, and saying so is what makes the reader fall through to
	// the other files rather than returning nonsense.
	for _, target := range []string{"/etc/localtime", "", "/var/db/zoneinfo"} {
		if got := zoneFromPath(target); got != "" {
			t.Errorf("zoneFromPath(%q) = %q, want an empty string", target, got)
		}
	}
}

// Every platform-restricted module names platforms the signature package
// will accept.
//
// A typo here — "darwins", "win" — produces a module that refuses
// everywhere and a refusal naming a platform that does not exist. It
// cannot be caught by running on any one platform, because on all but
// one the refusal is what you would expect to see anyway.
func TestEveryDeclaredPlatformIsARealOne(t *testing.T) {
	known := map[string]bool{
		"aix": true, "android": true, "darwin": true, "dragonfly": true,
		"freebsd": true, "hurd": true, "illumos": true, "ios": true,
		"js": true, "linux": true, "nacl": true, "netbsd": true,
		"openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
		"windows": true, "zos": true,
	}
	r := New()
	checked := 0
	for _, sigs := range []*signature.Registry{r.Exec.Signatures(), r.States.Signatures()} {
		for _, name := range sigs.Names() {
			sig, ok := sigs.Lookup(name)
			if !ok {
				continue
			}
			for _, p := range sig.Platforms {
				checked++
				if !known[p] {
					t.Errorf("%s is declared for %q, which is not a platform Go has", name, p)
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no platform restriction was checked; this audit has stopped checking anything")
	}
	t.Logf("checked %d platform declarations", checked)
}

// The refusal an operator sees names the platform they are on and the
// ones the module runs on, rather than only one of the two.
func TestAPlatformRefusalNamesBothSides(t *testing.T) {
	r := New()
	// A module restricted away from this host, whichever host that is.
	var name string
	for _, candidate := range r.Exec.Names() {
		sig, ok := r.Exec.Signatures().Lookup(candidate)
		if !ok || len(sig.Platforms) == 0 {
			continue
		}
		if sig.CheckPlatform() != nil {
			name = candidate
			break
		}
	}
	if name == "" {
		t.Skip("every restricted module runs on this platform; there is no refusal to read here")
	}
	sig, _ := r.Exec.Signatures().Lookup(name)
	err := sig.CheckPlatform()
	if err == nil {
		t.Fatalf("%s was expected to refuse here", name)
	}
	if !strings.Contains(err.Error(), runtime.GOOS) {
		t.Errorf("%s's refusal does not name this node's platform: %v", name, err)
	}
	for _, p := range sig.Platforms {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("%s's refusal does not name %s, which it does run on: %v", name, p, err)
		}
	}
}

// The account tool is chosen from what is installed, and the choice can
// be checked from any host.
//
// `pickAccountTool` prefers this platform's own tool and falls back to
// whichever one is present, which is what a minimal container needs. The
// fallback is the interesting half and the half nobody was ever on the
// right machine to see: a Linux image with `pw` but no `useradd`, or the
// reverse, takes a branch that the platform preference normally hides.
//
// Injectable through Lookup, so all four cases run everywhere.
func TestTheAccountToolIsChosenFromWhatIsInstalled(t *testing.T) {
	withTools := func(present ...string) *exec.Context {
		c := newCtx(false)
		have := map[string]bool{}
		for _, p := range present {
			have[p] = true
		}
		c.Lookup = func(name string) string {
			if have[name] {
				return "/usr/sbin/" + name
			}
			return ""
		}
		return c
	}

	// Whatever this host is, a node carrying only one of the two tools
	// gets that one.
	for _, tc := range []struct {
		binary string
		want   string
	}{
		{"pw", "freebsd"},
		{"useradd", "linux"},
	} {
		got, err := pickAccountTool(withTools(tc.binary))
		if err != nil {
			t.Errorf("a node with only %s got no tool: %v", tc.binary, err)
			continue
		}
		if got.Platform != tc.want {
			t.Errorf("a node with only %s got the %s tool", tc.binary, got.Platform)
		}
		if got.Binary != tc.binary {
			t.Errorf("the %s tool invokes %q", got.Platform, got.Binary)
		}
	}

	// With both present, this platform's own wins — which is only
	// assertable for the platform actually running.
	if both, err := pickAccountTool(withTools("pw", "useradd")); err == nil {
		switch runtime.GOOS {
		case "freebsd":
			if both.Platform != "freebsd" {
				t.Errorf("FreeBSD with both tools chose %s", both.Platform)
			}
		case "linux":
			if both.Platform != "linux" {
				t.Errorf("Linux with both tools chose %s", both.Platform)
			}
		}
	}

	// And a node with neither is refused by name, naming both tools so
	// an operator knows which to install.
	_, err := pickAccountTool(withTools())
	if err == nil {
		t.Fatal("a node with no account tool got one anyway")
	}
	for _, want := range []string{"pw", "useradd", runtime.GOOS} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}
