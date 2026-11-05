package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/redact"
)

// A `state.show_*` job does not send the hub a registered secret. The
// command line's forms were masked (5.556) and the job's were not, so the
// compiled states, with a password in `contents:` whole, were returned to
// the hub and kept in its job cache. DIVERGENCE 5.557.
func TestAStateShowJobDoesNotReturnARegisteredSecret(t *testing.T) {
	const secret = "s3cr3t-value-123"
	n := nodeWithBrokenPillar(t)
	dir := t.TempDir()
	states, pillars := filepath.Join(dir, "states"), filepath.Join(dir, "pillar")
	for path, body := range map[string]string{
		filepath.Join(states, "top.sls"):  "base:\n  '*':\n    - web\n",
		filepath.Join(states, "web.sls"):  "/etc/app.conf:\n  file.managed:\n    - contents: password=" + secret + "\n",
		filepath.Join(pillars, "top.sls"): "base: {}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n.pillars = fileserver.NewRoots(map[string][]string{"base": {pillars}})
	n.files = fileserver.NewFetcher(fileserver.NewRoots(map[string][]string{"base": {states}}))
	n.statesRunning = new(atomic.Int64)
	if n.secrets == nil {
		n.secrets = redact.New()
	}
	n.secrets.Add(secret)

	for _, fun := range []string{"state.show_lowstate", "state.show_highstate", "state.show_sls"} {
		ret := n.executeJob(&job.Job{JID: job.ID("20261103T1"), Fun: fun, Arg: []string{"web"}})
		if !ret.Success {
			t.Fatalf("%s failed: %s", fun, ret.Return)
		}
		if strings.Contains(string(ret.Return), secret) {
			t.Errorf("%s returned the registered secret to the hub: %s", fun, ret.Return)
		}
		if !strings.Contains(string(ret.Return), "/etc/app.conf") {
			t.Errorf("%s no longer says what the state is: %s", fun, ret.Return)
		}
	}
}

// `runas_password` is masked by name in every `state.show_*` form.
//
// It is a password written into the tree, not a pillar value, so the
// redactor does not hold it and maskValue cannot find it. 5.266 masked it
// by key in show_lowstate; show_highstate and show_sls render the high
// data, which is a different function, and printed it (DIVERGENCE 5.589).
func TestEveryStateShowFormMasksRunasPassword(t *testing.T) {
	const password = "unregistered-pw-4471"
	n := nodeWithBrokenPillar(t)
	dir := t.TempDir()
	states, pillars := filepath.Join(dir, "states"), filepath.Join(dir, "pillar")
	for path, body := range map[string]string{
		filepath.Join(states, "top.sls"): "base:\n  '*':\n    - web\n",
		filepath.Join(states, "web.sls"): "whoami:\n  cmd.run:\n    - runas: svc\n" +
			"    - runas_password: " + password + "\n",
		filepath.Join(pillars, "top.sls"): "base: {}\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n.pillars = fileserver.NewRoots(map[string][]string{"base": {pillars}})
	n.files = fileserver.NewFetcher(fileserver.NewRoots(map[string][]string{"base": {states}}))
	n.statesRunning = new(atomic.Int64)
	if n.secrets == nil {
		n.secrets = redact.New()
	}

	for _, fun := range []string{"state.show_lowstate", "state.show_highstate", "state.show_sls"} {
		ret := n.executeJob(&job.Job{JID: job.ID("20261105T1"), Fun: fun, Arg: []string{"web"}})
		if !ret.Success {
			t.Fatalf("%s failed: %s", fun, ret.Return)
		}
		if strings.Contains(string(ret.Return), password) {
			t.Errorf("%s returned runas_password: %s", fun, ret.Return)
		}
		if !strings.Contains(string(ret.Return), "runas_password") {
			t.Errorf("%s no longer says a password is set: %s", fun, ret.Return)
		}
	}
}
