package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The handbook's examples are checked here, by the program a reader will run
// them with. internal/handbook's Check holds each code block in a chapter to
// the file it quotes; these hold the files to doing what the chapters say.
// "The examples in this handbook were run" is a sentence in a book that a
// reader will believe, so it is made true on every test run, on a machine
// that need not be the author's.

const handbookExamples = "../../contrib/examples/handbook"

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// isolated returns the flags for a local run of a tree, with the machine's own
// configuration out of the way.
func isolated(t *testing.T, states, pillar string, extra ...string) []string {
	t.Helper()
	empty := t.TempDir()
	args := []string{"--local", "--file-root", states, "--pillar-root", pillar,
		"--root", empty, "--config", filepath.Join(empty, "absent.yaml")}
	return append(args, extra...)
}

// The tour of chapter 3 and the idempotency chapter's claim, run: the first
// apply changes the machine and exits 0, the second finds nothing to do and
// exits 2, a tampered file is repaired, and a pillar edit changes only what
// it should. The playground writes under a directory the pillar names, so the
// test points it at a temporary one.
func TestTheGuidedTourConvergesAndRepairsDrift(t *testing.T) {
	dir := t.TempDir()
	tree := t.TempDir()
	copyTree(t, filepath.Join(handbookExamples, "playground"), tree)
	pillarFile := filepath.Join(tree, "pillar", "playground.sls")
	body, err := os.ReadFile(pillarFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "/tmp/halite-playground") {
		t.Fatal("the playground pillar no longer names /tmp/halite-playground; update this test and chapter 3")
	}
	target := filepath.Join(dir, "playground")
	if err := os.WriteFile(pillarFile, []byte(strings.ReplaceAll(string(body), "/tmp/halite-playground", target)), 0o644); err != nil {
		t.Fatal(err)
	}
	flags := isolated(t, filepath.Join(tree, "states"), filepath.Join(tree, "pillar"))
	apply := func(extra ...string) result {
		return run(t, append(append([]string{"state", "apply"}, flags...), extra...)...)
	}

	// A rehearsal changes nothing, and says what it would do.
	rehearsal := apply("--test")
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("--test created %s", target)
	}
	if !strings.Contains(rehearsal.stdout, "Result: None") || !strings.Contains(rehearsal.stdout, "would be created") {
		t.Errorf("the rehearsal does not say what it would do:\n%s", rehearsal.stdout)
	}

	if got := apply(); got.code != 0 {
		t.Fatalf("the first apply exited %d, want 0:\n%s%s", got.code, got.stdout, got.stderr)
	}
	motd, err := os.ReadFile(filepath.Join(target, "motd"))
	if err != nil || !strings.Contains(string(motd), "Owner: the documentation team") {
		t.Fatalf("the motd is %q (%v)", motd, err)
	}
	log := func() string {
		b, _ := os.ReadFile(filepath.Join(target, "changes.log"))
		return string(b)
	}
	if n := strings.Count(log(), "motd changed"); n != 1 {
		t.Errorf("changes.log has %d line(s) after the first apply, want 1", n)
	}

	// The claim chapter 4 stands on.
	if got := apply(); got.code != 2 {
		t.Fatalf("the second apply exited %d, want 2 (converged):\n%s", got.code, got.stdout)
	}
	if n := strings.Count(log(), "motd changed"); n != 1 {
		t.Errorf("a converged run ran the onchanges command: %d lines", n)
	}

	// Drift, found by a rehearsal and repaired by an apply.
	if err := os.WriteFile(filepath.Join(target, "motd"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := apply("--test"); !strings.Contains(got.stdout, "would be updated: its contents") {
		t.Errorf("the rehearsal did not find the drift:\n%s", got.stdout)
	}
	if got := apply(); got.code != 0 {
		t.Fatalf("the repair exited %d, want 0", got.code)
	}
	if n := strings.Count(log(), "motd changed"); n != 2 {
		t.Errorf("the repair should have fired the onchanges command once more; log has %d lines", n)
	}

	// Chapter 3, step 8: changing the data changes the one state it feeds.
	changed := strings.ReplaceAll(string(body), "/tmp/halite-playground", target)
	changed = strings.ReplaceAll(changed, "the documentation team", "the platform team")
	if err := os.WriteFile(pillarFile, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	out := apply("--out", "json")
	var returns map[string]map[string]any
	if err := json.Unmarshal([]byte(out.stdout), &returns); err != nil {
		t.Fatalf("--out json: %v\n%s", err, out.stdout)
	}
	changedIDs := map[string]bool{}
	for _, r := range returns {
		if ch, _ := r["changes"].(map[string]any); len(ch) > 0 {
			changedIDs[r["__id__"].(string)] = true
		}
	}
	if !changedIDs["playground_motd"] || changedIDs["playground_dir"] {
		t.Errorf("a pillar edit changed %v; want the motd and not the directory", changedIDs)
	}
}

// The two errors chapter 3 shows a reader, with the messages it quotes.
func TestTheGuidedTourErrorsAreTheOnesTheChapterShows(t *testing.T) {
	tree := t.TempDir()
	copyTree(t, filepath.Join(handbookExamples, "playground"), tree)
	sls := filepath.Join(tree, "states", "playground.sls")
	good, _ := os.ReadFile(sls)
	flags := isolated(t, filepath.Join(tree, "states"), filepath.Join(tree, "pillar"))
	apply := func() result {
		return run(t, append([]string{"state", "apply", "--test"}, flags...)...)
	}

	if err := os.WriteFile(sls, []byte(strings.Replace(string(good), "mode: '0644'", "mode: 0644", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := apply()
	if got.code != 1 || !strings.Contains(got.stderr, "must be a quoted string, found the integer 420") {
		t.Errorf("an unquoted mode: exit %d, stderr %q", got.code, got.stderr)
	}

	if err := os.WriteFile(sls, []byte(strings.Replace(string(good), "['owner']", "['ownr']", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	got = apply()
	if got.code != 1 || !strings.Contains(got.stderr, "ownr is undefined") {
		t.Errorf("a misspelt pillar key: exit %d, stderr %q", got.code, got.stderr)
	}
}

// The web-server tree compiles for every platform it is meant for, which is
// the check chapter 16's CI script makes. The platform is a grain override.
func TestTheWebserverTreeCompilesOnEveryPlatform(t *testing.T) {
	states := filepath.Join(handbookExamples, "webserver", "states")
	pillar := filepath.Join(handbookExamples, "webserver", "pillar")
	cases := []struct {
		name, kernel, family, os string
		bsdTuning                bool
		confDir                  string
	}{
		{"Debian", "Linux", "Debian", "Debian", false, "/etc/nginx"},
		{"RedHat", "Linux", "RedHat", "RedHat", false, "/etc/nginx"},
		{"FreeBSD", "FreeBSD", "FreeBSD", "FreeBSD", true, "/usr/local/etc/nginx"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := filepath.Join(dir, "node.yaml")
			grains := "grains:\n  kernel: " + c.kernel + "\n  os_family: " + c.family + "\n  os: " + c.os + "\n"
			if err := os.WriteFile(cfg, []byte(grains), 0o644); err != nil {
				t.Fatal(err)
			}
			args := []string{"state", "show_lowstate", "--local", "--file-root", states, "--pillar-root", pillar,
				"--root", dir, "--config", cfg, "--id", "web1.example.com", "--out", "json"}
			got := run(t, args...)
			if got.code != 0 {
				t.Fatalf("does not compile: %s", got.stderr)
			}
			var chunks []map[string]any
			if err := json.Unmarshal([]byte(got.stdout), &chunks); err != nil {
				t.Fatal(err)
			}
			ids := map[string]map[string]any{}
			for _, ch := range chunks {
				ids[ch["__id__"].(string)] = ch
			}
			for _, want := range []string{"common_packages", "admin_alice", "admin_bob", "motd",
				"nginx", "nginx_conf", "site_www", "site_api", "nginx_service"} {
				if ids[want] == nil {
					t.Errorf("no state %q", want)
				}
			}
			if ids["nginx_conf"] != nil && ids["nginx_conf"]["name"] != c.confDir+"/nginx.conf" {
				t.Errorf("nginx_conf writes %v, want %s/nginx.conf", ids["nginx_conf"]["name"], c.confDir)
			}
			if has := ids["somaxconn"] != nil; has != c.bsdTuning {
				t.Errorf("somaxconn present = %v, want %v", has, c.bsdTuning)
			}
			// The ordering chapters 6 and 10 describe: the service comes
			// after every site file that watches into it.
			order := func(id string) float64 { return ids[id]["__run_num__"].(float64) }
			if order("nginx_service") < order("site_api") || order("nginx_service") < order("nginx_conf") {
				t.Errorf("nginx_service ran at %v, before a file it watches", order("nginx_service"))
			}
		})
	}

	// A node that matches no rule gets only the common states.
	dir := t.TempDir()
	got := run(t, "state", "show_states", "--local", "--file-root", states, "--pillar-root", pillar,
		"--root", dir, "--config", filepath.Join(dir, "absent.yaml"), "--id", "db1.example.com")
	if strings.Contains(got.stdout, "webserver.") {
		t.Errorf("a database node was given web-server states:\n%s", got.stdout)
	}
}

// Chapter 6's requisites, with the outcomes it quotes. The file fails on
// purpose (one state, `breaker`, always fails), so the exit status is 1.
func TestTheRequisitesTreeBehavesAsChapterSixSays(t *testing.T) {
	root := filepath.Join(handbookExamples, "requisites")
	flags := isolated(t, filepath.Join(root, "states"), filepath.Join(root, "pillar"), "--out", "json")
	got := run(t, append([]string{"state", "apply"}, flags...)...)
	if got.code != 1 {
		t.Fatalf("exit %d, want 1 (breaker fails by design)", got.code)
	}
	var returns map[string]map[string]any
	if err := json.Unmarshal([]byte(got.stdout), &returns); err != nil {
		t.Fatalf("%v\n%s", err, got.stdout)
	}
	byID := map[string]map[string]any{}
	for _, r := range returns {
		byID[r["__id__"].(string)] = r
	}
	check := func(id string, result bool, comment string) {
		t.Helper()
		r := byID[id]
		if r == nil {
			t.Errorf("no state %s", id)
			return
		}
		if r["result"] != result {
			t.Errorf("%s: result %v, want %v (%v)", id, r["result"], result, r["comment"])
		}
		if !strings.Contains(r["comment"].(string), comment) {
			t.Errorf("%s: comment %q does not contain %q", id, r["comment"], comment)
		}
	}
	check("needs_quiet", true, "does nothing, successfully")
	check("needs_breaker", false, "was skipped because breaker (test.fail_without_changes) failed")
	check("react_to_changer", true, `"echo reacted" ran`)
	check("react_to_quiet", true, "waits for a watch requisite to fire, and none did")
	check("only_if_changer_changed", true, "reported a synthetic change")
	check("only_if_quiet_changed", true, "none of its onchanges requisites reported changes")
	check("rollback", true, "reported a synthetic change")
	check("no_rollback_needed", true, "none of its onfail requisites failed")
	check("at_the_end", true, "Listening reaction, run at the end of the state run")
	if byID["declared_first"] == nil || byID["declared_second"] == nil {
		t.Error("the require_in pair is missing")
	} else if byID["declared_first"]["__run_num__"].(float64) > byID["declared_second"]["__run_num__"].(float64) {
		t.Error("require_in did not order declared_first before declared_second")
	}
}
