package modules

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJailConfIsRendered(t *testing.T) {
	got, err := renderJailConf("www", map[string]any{
		"path":      "/usr/local/jails/www",
		"hostname":  "www.example.com",
		"ip4_addr":  "10.0.0.10",
		"interface": "em0",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `# managed by halite
www {
	path = "/usr/local/jails/www";
	host.hostname = "www.example.com";
	ip4.addr = "10.0.0.10";
	interface = "em0";
	exec.start = "/bin/sh /etc/rc";
	exec.stop = "/bin/sh /etc/rc.shutdown";
	mount.devfs;
}
`
	if got != want {
		t.Fatalf("want:\n%s\ngot:\n%s", want, got)
	}
}

func TestHostnameDefaultsToTheJailName(t *testing.T) {
	got, err := renderJailConf("db1", map[string]any{"path": "/jails/db1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `host.hostname = "db1";`) {
		t.Fatalf("want the name as the hostname:\n%s", got)
	}
}

func TestJailParameters(t *testing.T) {
	got, err := renderJailConf("www", map[string]any{
		"path": "/jails/www",
		"params": map[string]any{
			"allow.raw_sockets": "true",
			"devfs_ruleset":     "4",
			"ip4.addr":          []any{"10.0.0.10", "10.0.0.11"},
			"exec.stop":         "", // an override to nothing drops it
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\tallow.raw_sockets;\n",                       // true is a bare flag
		"\tdevfs_ruleset = \"4\";\n",                   // a scalar is quoted
		"\tip4.addr = \"10.0.0.10\", \"10.0.0.11\";\n", // a list is comma separated
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("want %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "exec.stop") {
		t.Fatalf("an empty override should drop the parameter:\n%s", got)
	}
}

func TestParametersOverrideTheDefaults(t *testing.T) {
	got, err := renderJailConf("www", map[string]any{
		"path":   "/jails/www",
		"params": map[string]any{"exec.start": "/bin/sh /etc/rc.local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "/bin/sh /etc/rc;") {
		t.Fatalf("the default should be gone:\n%s", got)
	}
	if !strings.Contains(got, `exec.start = "/bin/sh /etc/rc.local";`) {
		t.Fatalf("want the override:\n%s", got)
	}
	if strings.Count(got, "exec.start") != 1 {
		t.Fatalf("a parameter should be written once:\n%s", got)
	}
}

func TestRenderingIsStable(t *testing.T) {
	// Map iteration is random; a block that reordered itself would report a
	// change on every run.
	args := map[string]any{
		"path": "/jails/www",
		"params": map[string]any{
			"allow.raw_sockets": "true",
			"devfs_ruleset":     "4",
			"allow.sysvipc":     "true",
			"osrelease":         "14.1-RELEASE",
		},
	}
	first, err := renderJailConf("www", args)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, err := renderJailConf("www", args)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("run %d differed:\n%s\n%s", i, first, got)
		}
	}
}

func TestJailValuesAreQuoted(t *testing.T) {
	got, err := renderJailConf("www", map[string]any{
		"path":   `/jails/with "quotes"`,
		"params": map[string]any{"exec.poststart": `echo "hello"`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `path = "/jails/with \"quotes\"";`) {
		t.Fatalf("a quote in a value has to be escaped:\n%s", got)
	}
	if !strings.Contains(got, `exec.poststart = "echo \"hello\"";`) {
		t.Fatalf("a quote in a parameter has to be escaped:\n%s", got)
	}
}

func TestJailNamesAreChecked(t *testing.T) {
	for _, name := range []string{"two words", `quo"te`, "semi;colon", "brace{"} {
		if _, err := renderJailConf(name, map[string]any{"path": "/jails/x"}); err == nil {
			t.Fatalf("%q is not a name jail.conf can hold", name)
		}
	}
}

func TestParamsMustBeAMapping(t *testing.T) {
	if _, err := renderJailConf("www", map[string]any{
		"path": "/jails/www", "params": []any{"allow.raw_sockets"},
	}); err == nil {
		t.Fatal("a list of params should be reported, not silently ignored")
	}
}

func TestBooleanOffIsNotGuessed(t *testing.T) {
	// jail.conf spells a cleared boolean by prefixing the last component
	// with "no" (allow.nomount.devfs). Guessing where that prefix goes is
	// how a state writes a file that means something else, so `false` is
	// written as a value and the negated name is the operator's to spell.
	got, err := renderJailConf("www", map[string]any{
		"path":   "/jails/www",
		"params": map[string]any{"allow.nomount.devfs": "true", "allow.raw_sockets": "false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "\tallow.nomount.devfs;\n") {
		t.Fatalf("the negated name should be written as given:\n%s", got)
	}
	if !strings.Contains(got, `allow.raw_sockets = "false";`) {
		t.Fatalf("false is a value, not a spelling halite invents:\n%s", got)
	}
}

func TestJailStatesAreFreeBSDOnly(t *testing.T) {
	// The guard is what the other platforms see; on FreeBSD these need a
	// path and a real jls, so this only asserts the platform check itself.
	if err := jailsSupported(); err != nil && !strings.Contains(err.Error(), "FreeBSD") {
		t.Fatalf("unexpected error %v", err)
	}
}

func TestJailConfPathFollowsTheConvention(t *testing.T) {
	if got := jailConfPath("www", nil); got != "/etc/jail.conf.d/www.conf" {
		t.Fatalf("want the rc.d/jail location, got %q", got)
	}
	if got := jailConfPath("www", map[string]any{"config": "/tmp/www.conf"}); got != "/tmp/www.conf" {
		t.Fatalf("want the override, got %q", got)
	}
}

// TestJail8ParsesWhatHaliteWrites checks the rendering against the real
// parser rather than against my idea of the format. jail(8) has no lint
// mode, but `-e` parses a file and exhibits what it found without creating
// anything, which is exactly the check worth having.
func TestJail8ParsesWhatHaliteWrites(t *testing.T) {
	if runtime.GOOS != "freebsd" || !has("jail") {
		t.Skip("needs jail(8)")
	}
	body, err := renderJailConf("www", map[string]any{
		"path":      "/usr/local/jails/www",
		"hostname":  "www.example.com",
		"ip4_addr":  "10.0.0.10",
		"interface": "em0",
		"params": map[string]any{
			"allow.raw_sockets": "true",
			"devfs_ruleset":     "4",
			"ip4.addr":          []any{"10.0.0.20", "10.0.0.21"},
			"exec.poststart":    `echo "started"`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "www.conf")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, _, rc, err := run("jail", "-f", path, "-e", ";")
	if err != nil || rc != 0 {
		t.Fatalf("jail(8) rejected the file (rc=%d, %v):\n%s\n%s", rc, err, body, out)
	}
	for _, want := range []string{
		"name=www", "path=/usr/local/jails/www", "host.hostname=www.example.com",
		"allow.raw_sockets", "devfs_ruleset=4", "ip4.addr=10.0.0.20,10.0.0.21",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("jail(8) did not report %q in:\n%s", want, out)
		}
	}
}
