package main

import (
	"strings"
	"testing"
)

// TestStateShowCommandsDoNotPrintARegisteredSecret holds the display
// boundary of the show_* forms of `state`. `state apply`, `pillar` and
// `doctor` ran their output through the node's redactor; show_highstate,
// show_sls and show_lowstate printed the compiled states as they were,
// so a secret carried as a state argument (a password in `contents:`)
// appeared whole in a terminal or a CI log. The secret here is registered
// the way a real one is, by a configured credential (seedConfiguredSecrets),
// and the test asserts on the process's real stdout.
func TestStateShowCommandsDoNotPrintARegisteredSecret(t *testing.T) {
	const secret = "s3cr3t-value-123"
	dir := t.TempDir()
	files := tree(t, map[string]string{
		"top.sls": "base:\n  '*':\n    - web\n",
		"web.sls": "/etc/app.conf:\n  file.managed:\n    - contents: password=" + secret + "\n" +
			"deploy:\n  cmd.run:\n    - name: deploy --token " + secret + "\n" +
			"    - runas_password: " + secret + "\n",
	})
	// tree() points --config at an absent file; swap in one that holds the
	// secret, so the redactor is seeded the way a real node's is.
	cfg := writeConfig(t, dir, "returner_smtp_password: "+secret+"\n")
	for i, a := range files {
		if a == "--config" {
			files[i+1] = cfg
		}
	}

	for _, sub := range [][]string{
		{"show_highstate"}, {"show_sls", "web"}, {"show_lowstate"},
		{"show_lowstate", "--out", "json"}, {"show_highstate", "--out", "yaml"},
	} {
		got := run(t, append([]string{"state", sub[0]}, append(files, sub[1:]...)...)...)
		if got.code != 0 {
			t.Fatalf("%v exited %d: %s", sub, got.code, got.stderr)
		}
		if strings.Contains(got.stdout, secret) {
			t.Errorf("%v printed the registered secret:\n%s", sub, got.stdout)
		}
		if !strings.Contains(got.stdout, "password=") || !strings.Contains(got.stdout, "/etc/app.conf") {
			t.Errorf("%v lost the surrounding structure:\n%s", sub, got.stdout)
		}
	}

	// The two forms that print names only must keep working.
	for _, sub := range []string{"show_states", "show_top"} {
		got := run(t, append([]string{"state", sub}, files...)...)
		if got.code != 0 || !strings.Contains(got.stdout, "web") {
			t.Errorf("%s = %+v", sub, got)
		}
	}
}
