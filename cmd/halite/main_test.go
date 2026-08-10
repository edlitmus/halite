package main

import "testing"

func TestParseCallArgsHonorsTestAnywhere(t *testing.T) {
	// `halite ssh ... call ... -test` appends the flag after the key=value
	// arguments, so its position must not matter — a dropped -test would
	// mutate a host under a flag that promised a dry run.
	for name, args := range map[string][]string{
		"flag last":    {"pkg.installed", "name=nginx", "-test"},
		"flag first":   {"-test", "pkg.installed", "name=nginx"},
		"flag between": {"pkg.installed", "-test", "name=nginx"},
	} {
		fn, test, callArgs, err := parseCallArgs(args)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !test {
			t.Errorf("%s: -test was dropped", name)
		}
		if fn != "pkg.installed" {
			t.Errorf("%s: function = %q, want pkg.installed", name, fn)
		}
		if callArgs["name"] != "nginx" {
			t.Errorf("%s: args = %v, want name=nginx", name, callArgs)
		}
	}
}

func TestParseCallArgsWithoutTest(t *testing.T) {
	fn, test, callArgs, err := parseCallArgs([]string{"cmd.run", "cmd=id"})
	if err != nil {
		t.Fatal(err)
	}
	if test {
		t.Error("test = true without -test")
	}
	if fn != "cmd.run" || callArgs["cmd"] != "id" {
		t.Errorf("got %q %v, want cmd.run cmd=id", fn, callArgs)
	}
}

func TestParseCallArgsRejectsMalformedInput(t *testing.T) {
	if _, _, _, err := parseCallArgs(nil); err == nil {
		t.Error("no function: accepted, want an error")
	}
	if _, _, _, err := parseCallArgs([]string{"pkg.installed", "nginx"}); err == nil {
		t.Error("a bare argument is not key=value: accepted, want an error")
	}
}
