package main

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/schedule"
	"github.com/edlitmus/halite/internal/value"
)

// contrib/examples/tree is a tree halite manages itself with: every
// node's node.yaml and schedule, the hub's and the API's configuration and
// policy on the hub host, the certificates nothing renews by itself, and a
// signer that signs extensions. It is documentation an operator copies,
// so it is held to compiling, for every role it names and on both kinds of
// platform its map distinguishes, and what it writes is held to being
// configuration halite accepts: every node.yaml, hub.yaml and api.yaml it
// would serialize loads with no warning, its policy loads with no warning,
// and every node's schedule parses -- a schedule that does not stops the
// agent from starting.
//
// This compiles; it does not apply. Nearly every state here writes under
// /etc or /usr/local/etc and drives a service manager, which a test run as
// a user on a developer's machine cannot do, and the restart and
// publication steps have not been run by this project. The tree's
// README says so, state by state. DIVERGENCE 5.262.
func TestTheExampleTreeCompilesForEveryRole(t *testing.T) {
	tree := filepath.Join("..", "..", "contrib", "examples", "tree")
	states, pillar := filepath.Join(tree, "states"), filepath.Join(tree, "pillar")

	roles := []struct {
		id   string
		sls  []string // empty is the highstate
		want []string
	}{
		{"web1.example.com", nil, []string{
			"halite node configuration", "halite node restart when its configuration changes", "halite node agent"}},
		{"hub.example.com", nil, []string{
			"halite node configuration", "halite hub configuration", "halite hub policy", "halite hub service",
			"halite api configuration", "halite api service"}},
		{"hub.example.com", []string{"halite.certs"}, []string{
			"halite api ca", "halite api serving certificate", "halite api operator certificate", "halite api service"}},
		{"signer.example.com", nil, []string{
			"halite node configuration", "halite signing key", "halite publish signed extensions"}},
		{"signer.example.com", []string{"halite.signer"}, []string{
			"halite signing key", "halite sign hello 1.0.0", "halite publish signed extensions"}},
	}
	for _, kernel := range []struct{ name, conf string }{
		{"FreeBSD", "/usr/local/etc/halite"},
		{"Linux", "/etc/halite"},
	} {
		// The kernel grain decides the paths and service names; a static
		// grain overrides what this machine is.
		cfgDir := t.TempDir()
		cfg := filepath.Join(cfgDir, "node.yaml")
		if err := os.WriteFile(cfg, []byte("grains:\n  kernel: "+kernel.name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, role := range roles {
			args := append([]string{"state", "show_lowstate"}, role.sls...)
			args = append(args, "--local", "--file-root", states, "--pillar-root", pillar,
				"--root", t.TempDir(), "--config", cfg, "--id", role.id, "--out", "json")
			got := run(t, args...)
			label := kernel.name + " " + role.id + " " + strings.Join(role.sls, ",")
			if got.code != 0 {
				t.Errorf("%s: the tree does not compile:\n%s", label, got.stderr)
				continue
			}
			var chunks []map[string]any
			if err := json.Unmarshal([]byte(got.stdout), &chunks); err != nil {
				t.Fatalf("%s: %v\n%s", label, err, got.stdout)
			}
			ids := map[string]bool{}
			for _, c := range chunks {
				ids[c["__id__"].(string)] = true
				if c["state"] == "file" && c["fun"] == "serialize" {
					checkWrittenConfig(t, label, kernel.conf, c)
				}
			}
			for _, w := range role.want {
				if !ids[w] {
					t.Errorf("%s: no state %q", label, w)
				}
			}
		}
	}
}

// checkWrittenConfig loads what one file.serialize would write the way the
// program that reads it does, and fails on anything it would warn about.
func checkWrittenConfig(t *testing.T, label, conf string, chunk map[string]any) {
	t.Helper()
	name, _ := chunk["name"].(string)
	// A path on the machine the tree manages, so slash-separated
	// whatever this test runs on: filepath would turn it into
	// backslashes on Windows and compare it with a slash path.
	if path.Dir(name) != conf {
		t.Errorf("%s: %s is not under this platform's config root %s", label, name, conf)
	}
	body, err := json.Marshal(chunk["dataset"])
	if err != nil {
		t.Fatal(err)
	}
	// JSON is YAML, so the loaders read it as they would the file.
	file := filepath.Join(t.TempDir(), path.Base(name))
	if err := os.WriteFile(file, body, 0o644); err != nil {
		t.Fatal(err)
	}
	var role config.Role
	switch path.Base(name) {
	case "policy.yaml":
		_, warnings, err := policy.Load(body, file)
		if err != nil || len(warnings) > 0 {
			t.Errorf("%s: the policy it writes does not load cleanly: %v %v", label, err, warnings)
		}
		return
	case "node.yaml":
		role = config.Node
	case "hub.yaml":
		role = config.Hub
	case "api.yaml":
		role = config.API
	default:
		t.Errorf("%s: serializes %s, which this test does not know how to check", label, name)
		return
	}
	loaded, err := config.Load(role, config.LoadOptions{Path: file, DropInDir: t.TempDir()})
	if err != nil {
		t.Errorf("%s: %s does not load: %v", label, name, err)
		return
	}
	if len(loaded.Warnings) > 0 {
		t.Errorf("%s: %s loads with warnings: %v", label, name, loaded.Warnings)
	}
	if role == config.Node {
		if raw, ok := loaded.Get("schedule"); ok {
			if _, err := schedule.Parse(value.Deep(raw), time.UTC); err != nil {
				t.Errorf("%s: the schedule it writes does not parse, so the agent would not start: %v", label, err)
			}
		}
	}
}
