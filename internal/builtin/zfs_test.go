package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// The read-back lines below are SYNTHETIC. Nobody has captured
// `zfs get -H -p -o property,value` for these properties on a real
// machine in this repository: the only `get` output a run has produced is
// zpool's `ashift\t12`, asserted by contrib/vm/zfs/probe.sh. Each line is
// what OpenZFS prints according to its source and zfsprops(7) — on|off
// for a boolean, the exact byte count under -p for a size, 0 under -p for
// an unset quota — and DIVERGENCE 5.290 lists what a FreeBSD lab run has
// to capture to replace them. Until then these tests check that the state
// agrees with that reading of zfs, not that zfs agrees with it.

// These tests call the state functions directly rather than through the
// registry. Through the registry they would skip on macOS and Windows,
// where zfs is not declared (skipWithoutZFSPlatform), and the developer
// machine this fix was written on is a Mac: a comparison test that skips
// there is one that never ran where it was written. What they test is a
// comparison and a command line, neither of which depends on the
// platform; the registry's own refusal is tested beside zpool's states.

// zfsFixture returns a node with a scripted zfs on which tank/data
// exists, unless gone is set, in which case `zfs list` says it does not.
func zfsFixture(t *testing.T, gone bool, responses map[string]string) *exec.Context {
	t.Helper()
	scripted := map[string]exec.Result{}
	if gone {
		scripted["zfs list -H -o name tank/data"] = exec.Result{Code: 1,
			Stderr: "cannot open 'tank/data': dataset does not exist"}
	} else {
		scripted["zfs list -H -o name tank/data"] = exec.Result{Stdout: "tank/data"}
	}
	for cmd, out := range responses {
		scripted[cmd] = exec.Result{Stdout: out}
	}
	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{Responses: scripted}
	c.Lookup = func(name string) string { return "/sbin/" + name }
	return c
}

// zfsPoolFixture is zpoolFixture without the platform skip: tank is
// absent unless the responses say otherwise.
func zfsPoolFixture(t *testing.T, responses map[string]string) *exec.Context {
	t.Helper()
	scripted := map[string]exec.Result{
		"zpool list -H -o name tank": {Code: 1, Stderr: "cannot open 'tank': no such pool"},
	}
	for cmd, out := range responses {
		scripted[cmd] = exec.Result{Stdout: out}
	}
	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{Responses: scripted}
	c.Lookup = func(name string) string { return "/sbin/" + name }
	return c
}

// ranAny reports any command that starts with prefix.
func ranAny(c *exec.Context, prefix string) []string {
	var out []string
	for _, got := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(got, prefix) {
			out = append(out, got)
		}
	}
	return out
}

// yamlProperties reads a properties mapping through the real parser with
// its default options, so that `off` is whatever this project's YAML
// makes of it rather than whatever the test assumes it makes.
func yamlProperties(t *testing.T, src string) *value.Map {
	t.Helper()
	parsed, _, err := yaml.Parse([]byte(src), yaml.DefaultOptions("zfs_test.sls"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := parsed.(*value.Map)
	if !ok {
		t.Fatalf("%q parsed to %T", src, parsed)
	}
	return m
}

// A dataset that already carries what is declared is left alone, in
// every spelling the commonest properties are written in. Before
// DIVERGENCE 5.290 each of these was a difference on every run, and the
// state ran `zfs set atime=false` (which zfs refuses) or set the quota it
// already had.
func TestZfsFilesystemPresentConvergesOnTheSpellingZfsReadsBack(t *testing.T) {
	for _, tc := range []struct {
		name     string
		yaml     string // declared, as an SLS writes it
		readBack string // the synthetic `zfs get -H -p` line
	}{
		{"atime off, a YAML 1.1 boolean", "atime: off", "atime\toff"},
		{"atime on, a YAML 1.1 boolean", "atime: on", "atime\ton"},
		{"atime false, a YAML boolean", "atime: false", "atime\toff"},
		{"compression on, a YAML 1.1 boolean", "compression: on", "compression\ton"},
		{"readonly on", "readonly: yes", "readonly\ton"},
		{"a quota in gigabytes", "quota: 10G", "quota\t10737418240"},
		{"a quota with a B", "quota: 10GB", "quota\t10737418240"},
		{"a lower-case unit", "quota: 10g", "quota\t10737418240"},
		{"a decimal quota", "quota: 1.5G", "quota\t1610612736"},
		{"recordsize in kilobytes", "recordsize: 128K", "recordsize\t131072"},
		{"recordsize in bytes, a YAML integer", "recordsize: 131072", "recordsize\t131072"},
		{"no quota", "quota: none", "quota\t0"},
		{"no refreservation", "refreservation: none", "refreservation\t0"},
		{"a user's quota", "userquota@bob: 1G", "userquota@bob\t1073741824"},
		{"a string property as written", "compression: lz4", "compression\tlz4"},
	} {
		props := yamlProperties(t, tc.yaml)
		prop := props.StringKeys()[0]
		c := zfsFixture(t, false, map[string]string{
			"zfs get -H -p -o property,value " + prop + " tank/data": tc.readBack,
		})
		res, err := zfsFilesystemPresent(c, value.MapOf("name", "tank/data", "properties", props))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Result == nil || !*res.Result {
			t.Errorf("%s: the state failed: %s", tc.name, res.Comment)
		}
		if res.HasChanges() {
			t.Errorf("%s: a converged dataset reported a change: %v", tc.name, res.Changes)
		}
		if set := ranAny(c, "zfs set"); len(set) != 0 {
			t.Errorf("%s: a converged dataset was set: %v", tc.name, set)
		}
	}
}

// A real difference is still one, and what reaches zfs is a spelling it
// takes: on|off for a boolean, the size as it was declared.
func TestZfsFilesystemPresentStillSetsARealDifference(t *testing.T) {
	for _, tc := range []struct {
		name     string
		yaml     string
		readBack string
		want     string
	}{
		{"atime turned off", "atime: off", "atime\ton", "zfs set atime=off tank/data"},
		{"atime turned on", "atime: on", "atime\toff", "zfs set atime=on tank/data"},
		{"a bigger quota", "quota: 10G", "quota\t5368709120", "zfs set quota=10G tank/data"},
		{"a quota removed", "quota: none", "quota\t10737418240", "zfs set quota=none tank/data"},
		{"a quota added", "quota: 10G", "quota\t0", "zfs set quota=10G tank/data"},
		{"a bigger recordsize", "recordsize: 1M", "recordsize\t131072", "zfs set recordsize=1M tank/data"},
		{"a different compression", "compression: lz4", "compression\ton", "zfs set compression=lz4 tank/data"},
		// A user property is a string to zfs, so a size spelling there
		// is not a size and the normalisation must not reach it.
		{"a user property is compared as written", "'com.example:size': 10G",
			"com.example:size\t10737418240", "zfs set com.example:size=10G tank/data"},
	} {
		props := yamlProperties(t, tc.yaml)
		prop := props.StringKeys()[0]
		c := zfsFixture(t, false, map[string]string{
			"zfs get -H -p -o property,value " + prop + " tank/data": tc.readBack,
		})
		res, err := zfsFilesystemPresent(c, value.MapOf("name", "tank/data", "properties", props))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Result == nil || !*res.Result {
			t.Errorf("%s: the state failed: %s", tc.name, res.Comment)
		}
		if !res.HasChanges() {
			t.Errorf("%s: a real difference was not reported", tc.name)
		}
		if set := ranAny(c, "zfs set"); len(set) != 1 || set[0] != tc.want {
			t.Errorf("%s: ran %v, want [%s]", tc.name, set, tc.want)
		}
	}
}

// Creating a dataset passes the same spellings to `zfs create -o`, where
// `atime=false` would have refused the whole creation.
func TestZfsFilesystemPresentCreatesWithSpellingsZfsTakes(t *testing.T) {
	c := zfsFixture(t, true, nil)
	props := yamlProperties(t, "atime: off\ncompression: on\nquota: 10G\n")
	res, err := zfsFilesystemPresent(c, value.MapOf("name", "tank/data", "properties", props))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("creating the dataset failed: %s", res.Comment)
	}
	assertRan(t, c, "zfs create -o atime=off -o compression=on -o quota=10G tank/data")
}

// The pool's boolean properties go through the same rendering:
// `autotrim: on` is a YAML 1.1 boolean too.
func TestZpoolPresentConvergesOnBooleanProperties(t *testing.T) {
	for _, tc := range []struct {
		name     string
		yaml     string
		readBack string
		want     string // the `zpool set` expected, or "" for none
	}{
		{"autotrim off, converged", "autotrim: off", "autotrim\toff", ""},
		{"autotrim on, converged", "autotrim: on", "autotrim\ton", ""},
		{"autoexpand on, converged", "autoexpand: on", "autoexpand\ton", ""},
		{"autoexpand off, converged", "autoexpand: off", "autoexpand\toff", ""},
		{"autotrim turned on", "autotrim: on", "autotrim\toff", "zpool set autotrim=on tank"},
		{"autoexpand turned off", "autoexpand: off", "autoexpand\ton", "zpool set autoexpand=off tank"},
	} {
		props := yamlProperties(t, tc.yaml)
		prop := props.StringKeys()[0]
		c := zfsPoolFixture(t, map[string]string{
			"zpool list -H -o name tank":                          "tank",
			"zpool get -H -p -o property,value " + prop + " tank": tc.readBack,
		})
		res, err := zpoolPresent(c, value.MapOf("name", "tank", "properties", props))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Result == nil || !*res.Result {
			t.Errorf("%s: the state failed: %s", tc.name, res.Comment)
		}
		set := ranAny(c, "zpool set")
		switch {
		case tc.want == "" && (len(set) != 0 || res.HasChanges()):
			t.Errorf("%s: a converged pool was changed: ran %v, changes %v", tc.name, set, res.Changes)
		case tc.want != "" && (len(set) != 1 || set[0] != tc.want):
			t.Errorf("%s: ran %v, want [%s]", tc.name, set, tc.want)
		}
	}
}

// A pool created with boolean pool and filesystem properties hands zpool
// on|off, not true|false.
func TestZpoolPresentCreatesWithOnOff(t *testing.T) {
	c := zfsPoolFixture(t, nil)
	res, err := zpoolPresent(c, value.MapOf(
		"name", "tank",
		"layout", []any{"/dev/sda"},
		"properties", yamlProperties(t, "autotrim: on"),
		"filesystem_properties", yamlProperties(t, "atime: off"),
		"import", false,
	))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("creating the pool failed: %s", res.Comment)
	}
	assertRan(t, c, "zpool create -o autotrim=on -O atime=off tank /dev/sda")
}

// The size grammar is salt.utils.zfs's re_zfs_size plus the bare byte
// count -p prints. What it refuses falls back to an exact comparison,
// which can fail to converge but cannot set something undeclared.
func TestZfsSizesAreReadAsSaltReadsThem(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"0", 0, true},
		{"131072", 131072, true},
		{"128K", 131072, true},
		{"128k", 131072, true},
		{"128KB", 131072, true},
		{"128kb", 131072, true},
		{"10G", 10737418240, true},
		{"1.5G", 1610612736, true},
		{"1T", 1 << 40, true},
		{"1P", 1 << 50, true},
		{"1E", 1 << 60, true},
		{"16E", 0, false},  // past uint64
		{"1.3K", 0, false}, // 1331.2 bytes: not a whole number
		{"1KiB", 0, false}, // libzfs takes it; Salt's grammar does not
		{"1Z", 0, false},
		{"1B", 0, false},
		{".5G", 0, false},
		{"1.G", 0, false},
		{"G", 0, false},
		{"", 0, false},
		{"none", 0, false}, // handled per property, not here
		{"-1G", 0, false},
	} {
		got, ok := zfsParseSize(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("zfsParseSize(%q) = %d, %v; want %d, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
