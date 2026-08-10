package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveHostsFromACommaList(t *testing.T) {
	hosts, err := resolveHosts("web1, root@web2 ,web3", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"web1", "root@web2", "web3"}
	if strings.Join(hosts, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", hosts, want)
	}
}

func TestResolveHostsGlobsTheRoster(t *testing.T) {
	roster := filepath.Join(t.TempDir(), "roster")
	content := "# lab hosts\n\nweb1.example.com\nroot@web2.example.com\ndb1.example.com\n"
	if err := os.WriteFile(roster, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	hosts, err := resolveHosts("web*", roster)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("got %v, want the two web hosts", hosts)
	}
	// The user@ prefix is part of the destination but not part of the name
	// the glob matches.
	if hosts[1] != "root@web2.example.com" {
		t.Errorf("got %q, want the destination with its user prefix intact", hosts[1])
	}

	all, err := resolveHosts("*", roster)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("'*' matched %d hosts, want 3", len(all))
	}
}

func TestResolveHostsReportsAnEmptyMatch(t *testing.T) {
	roster := filepath.Join(t.TempDir(), "roster")
	if err := os.WriteFile(roster, []byte("web1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveHosts("db*", roster); err == nil {
		t.Error("a glob matching nothing must be an error, not an empty run")
	}
	if _, err := resolveHosts("", ""); err == nil {
		t.Error("an empty host spec must be an error")
	}
}

func TestRemoteCommandBuildsTheRightInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		test bool
		want string
	}{
		{"highstate", []string{"state.highstate"}, false, "apply -json"},
		{"highstate dry run", []string{"state.highstate"}, true, "apply -json -test"},
		{"apply named", []string{"state.apply", "web.nginx"}, false, "apply web.nginx -json"},
		{"call", []string{"call", "pkg.installed", "name=nginx"}, false, "call pkg.installed name=nginx"},
		// A dry run must reach the remote binary; dropping it here would
		// mutate the fleet under a flag that promised not to.
		{"call dry run", []string{"call", "pkg.installed", "name=nginx"}, true, "call pkg.installed name=nginx -test"},
		{"grains", []string{"grains"}, false, "grains -json"},
	}
	for _, c := range cases {
		got, err := remoteCommand(c.args, c.test)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		// The first element is the kind, kept for local dispatch; the rest is
		// the remote command line.
		if joined := strings.Join(got[1:], " "); joined != c.want {
			t.Errorf("%s: got %q, want %q", c.name, joined, c.want)
		}
	}
}

func TestRemoteCommandRejectsMalformedInput(t *testing.T) {
	cases := map[string][]string{
		"unknown kind":            {"rm -rf"},
		"highstate with args":     {"state.highstate", "web"},
		"apply without sls":       {"state.apply"},
		"call without a function": {"call"},
		"call without a module":   {"call", "nodots"},
	}
	for name, args := range cases {
		if _, err := remoteCommand(args, false); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
}

func TestShellQuoteContainsSingleQuotes(t *testing.T) {
	// Everything crossing into the remote shell goes through this, so a
	// destination or path with a quote in it must not break out.
	got := shellQuote(`/tmp/it's; rm -rf /`)
	want := `'/tmp/it'\''s; rm -rf /'`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestSSHArgvPutsBatchModeFirst(t *testing.T) {
	// The `--` before the destination stops ssh and scp from reading a
	// hostile destination as an option.
	argv := sshArgv([]string{"Port=2222", "ProxyJump=bastion"}, "web1", "uname")
	want := "-o BatchMode=yes -o Port=2222 -o ProxyJump=bastion -- web1 uname"
	if strings.Join(argv, " ") != want {
		t.Errorf("got %q, want %q", strings.Join(argv, " "), want)
	}
}

func TestResolveHostsRejectsOptionLookingDestinations(t *testing.T) {
	// A destination beginning with '-' would be parsed by ssh as an option;
	// -oProxyCommand=... runs a local command instead of connecting anywhere.
	_, err := resolveHosts("web1,-oProxyCommand=touch /tmp/pwned", "")
	if err == nil {
		t.Fatal("a '-' destination must be an error, not an ssh option")
	}
	if !strings.Contains(err.Error(), "-oProxyCommand=touch /tmp/pwned") {
		t.Errorf("error %q does not name the offending entry", err)
	}

	roster := filepath.Join(t.TempDir(), "roster")
	if err := os.WriteFile(roster, []byte("web1\n-oProxyCommand=id\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveHosts("*", roster); err == nil {
		t.Error("a '-' roster line must be an error, not an ssh option")
	}
}

func TestNormalizeArch(t *testing.T) {
	for machine, want := range map[string]string{
		"x86_64": "amd64", "amd64": "amd64",
		"aarch64": "arm64", "arm64": "arm64",
		"riscv64": "riscv64",
	} {
		if got := normalizeArch(machine); got != want {
			t.Errorf("normalizeArch(%q) = %q, want %q", machine, got, want)
		}
	}
}

func TestBinaryForPrefersTheCrossBuiltMatch(t *testing.T) {
	dist := t.TempDir()
	target := filepath.Join(dist, "halite-linux-arm64")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &sshRunner{dist: dist}

	got, err := runner.binaryFor("linux", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Errorf("got %q, want %q", got, target)
	}

	if _, err := runner.binaryFor("openbsd", "sparc64"); err == nil {
		t.Error("an unbuilt platform must be an error, not a wrong binary")
	}
}

func TestRemoteOutputKeepsTheFleetReportValidJSON(t *testing.T) {
	cases := []struct {
		name string
		kind string
		out  string
		want string
	}{
		{"json passes through", "apply", `{"results": []}` + "\n", `{"results": []}`},
		// One host printing a motd or profile noise must not empty the whole
		// -json report.
		{"stray text is wrapped", "apply", "Welcome to web1!\n", `"Welcome to web1!"`},
		{"call is always text", "call", "----------\n  Result: True", `"----------\n  Result: True"`},
		{"empty output is omitted", "grains", "  \n", ""},
	}
	for _, c := range cases {
		got := remoteOutput(c.kind, c.out)
		if string(got) != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}

	// The whole point: a fleet report mixing all of the above still marshals.
	results := []sshHost{
		{Dest: "web1", Ok: true, Output: remoteOutput("apply", `{"ok": true}`)},
		{Dest: "web2", Ok: true, Output: remoteOutput("apply", "not json at all")},
		{Dest: "web3", Ok: true, Output: remoteOutput("grains", "")},
	}
	if _, err := json.MarshalIndent(results, "", "  "); err != nil {
		t.Errorf("fleet report failed to marshal: %v", err)
	}
}
