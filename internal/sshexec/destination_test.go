package sshexec

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/roster"
)

// Issue 321, M-14. The destination went on ssh's command line with no
// `--` in front of it and nothing checked it, and the cache roster takes
// the host from the `fqdn`, `host` or `nodename` grain the node reports.
// A node reporting `fqdn: -oProxyCommand=<cmd>` had an ssh older than
// 9.6 run <cmd> on the hub when an operator ran `halite-hub ssh
// --roster cache`. Reproduced with OpenSSH 9.5p1 on FreeBSD 15.1
// (DIVERGENCE 5.396).

// Each of these reached ssh's command line before the check.
var hostile = []struct {
	name   string
	target roster.Target
	says   string
}{
	{"a host that is an option", roster.Target{ID: "n1", Host: "-oProxyCommand=touch /tmp/x"}, "begins with `-`"},
	{"a user that is an option", roster.Target{ID: "n1", Host: "n1", User: "-oProxyCommand=touch /tmp/x"}, "begins with `-`"},
	{"a jump host that is an option", roster.Target{ID: "n1", Host: "n1", ProxyJump: "-oProxyCommand=touch /tmp/x"}, "begins with `-`"},
	{"a second jump hop that is an option", roster.Target{ID: "n1", Host: "n1", ProxyJump: "j1,-oProxyCommand=x"}, "begins with `-`"},
	{"a host a ProxyCommand shell would run", roster.Target{ID: "n1", Host: "n1$(touch /tmp/x)"}, `"$"`},
	{"a host with a space", roster.Target{ID: "n1", Host: "n1 -oProxyCommand=x"}, `" "`},
	{"a host with a newline", roster.Target{ID: "n1", Host: "n1\n"}, `"\n"`},
	{"a user with a backtick", roster.Target{ID: "n1", Host: "n1", User: "ed`id`"}, "\"`\""},
	{"a host with a percent token", roster.Target{ID: "n1", Host: "n1%d"}, `"%"`},
	{"a host with an at sign", roster.Target{ID: "n1", Host: "x@n1"}, `"@"`},
	{"no host", roster.Target{ID: "n1"}, "no host"},
}

func TestAHostileDestinationIsRefused(t *testing.T) {
	for _, c := range hostile {
		t.Run(c.name, func(t *testing.T) {
			err := checkTarget(c.target)
			if err == nil {
				t.Fatalf("accepted %+v", c.target)
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("refused, but with %q; want it to say %s", err, c.says)
			}
		})
	}
}

// What an estate's rosters actually hold must still pass.
func TestOrdinaryDestinationsAreAccepted(t *testing.T) {
	for _, target := range []roster.Target{
		{ID: "web1", Host: "web1"},
		{ID: "web1", Host: "web1.example.com", User: "deploy_user"},
		{ID: "web1", Host: "10.0.0.7", User: "root"},
		{ID: "web1", Host: "2001:db8::7"},
		{ID: "web1", Host: "web1", User: "ed@corp.example"},
		{ID: "web1", Host: "web1", User: "svc-halite.ops"},
		{ID: "web1", Host: "web1", ProxyJump: "bastion"},
		{ID: "web1", Host: "web1", ProxyJump: "ed@bastion:2222,inner.example.com"},
		{ID: "web1", Host: "web1", ProxyJump: "ssh://ed@bastion:2222"},
	} {
		if err := checkTarget(target); err != nil {
			t.Errorf("%+v: %v", target, err)
		}
	}
}

// `--` must come immediately before the destination on both command
// lines, whatever the roster and the options put before it.
func TestTheDestinationFollowsTheEndOfOptions(t *testing.T) {
	o := &Options{Options: []string{"StrictHostKeyChecking=yes"}}
	target := roster.Target{ID: "n1", Host: "n1.example.com", User: "ed", Port: 2222,
		Priv: "/k", IdentitiesOnly: true, ProxyJump: "bastion", TTY: true}

	args := o.execArgs(target, "/bin/sh -s")
	i := slices.Index(args, "ed@n1.example.com")
	if i < 1 || args[i-1] != "--" || i != len(args)-2 {
		t.Errorf("ssh's command line is %q; want `--`, the destination, the command, at the end", args)
	}

	args = o.copyArgs(target, "/bin/halite-node", "/var/tmp/halite-thin/x")
	i = slices.Index(args, "/bin/halite-node")
	if i < 1 || args[i-1] != "--" || i != len(args)-2 || args[i+1] != "ed@n1.example.com:/var/tmp/halite-thin/x" {
		t.Errorf("scp's command line is %q; want `--`, the binary, the destination, at the end", args)
	}
}

// The refusal comes before ssh is run at all, --clean's removal included,
// and it is that target's result rather than the whole run's.
func TestAHostileDestinationNeverReachesSSH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in ssh is a shell script")
	}
	dir := t.TempDir()
	ran := filepath.Join(dir, "ssh-ran")
	ssh := filepath.Join(dir, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\ntouch '"+ran+"'\nexit 255\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "halite-node")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	o := &Options{Binary: binary, SSH: ssh, SCP: ssh, Clean: true}
	for _, c := range hostile {
		target := c.target
		target.ThinDir = "/var/tmp/halite-thin"
		result := o.Run(context.Background(), target, nil)
		if result.Err == nil || !strings.HasPrefix(result.Err.Error(), "n1: ") {
			t.Errorf("%s: %v", c.name, result.Err)
		}
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("ssh was run for a target that should have been refused")
	}

	// And the stand-in does run for a good one, or the above proves
	// nothing.
	o.Run(context.Background(), roster.Target{ID: "n1", Host: "n1", ThinDir: "/var/tmp/halite-thin"}, nil)
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("the stand-in ssh never ran for a good target either: %v", err)
	}
}
