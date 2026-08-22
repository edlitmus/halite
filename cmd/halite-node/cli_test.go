package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The CLI is tested by re-executing this test binary as halite-node.
// Building the real binary would need the Go toolchain at test time,
// which a cross-compiled test binary run under emulation cannot reach;
// re-execution needs nothing but the binary already running. SPEC 11.8's
// exit codes and the argument dispatch are the contract under test, and
// both are only observable from outside the process.
const reexec = "HALITE_TEST_REEXEC"

func TestMain(m *testing.M) {
	if os.Getenv(reexec) != "" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type result struct {
	stdout string
	stderr string
	code   int
}

func run(t *testing.T, args ...string) result {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), reexec+"=1")
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return result{out.String(), errb.String(), code}
}

// tree writes an SLS root and returns the flags that point at it. Each
// test gets its own root and its own target directory.
func tree(t *testing.T, files map[string]string) []string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// --root isolates the run from the machine's own configuration. The
	// default root is probed for `state` and `pillar` directories, so a
	// test that left it alone would read the configuration of whatever
	// host it happens to run on.
	empty := t.TempDir()
	return []string{
		"--local",
		"--file-root", root,
		"--pillar-root", empty,
		"--root", empty,
		"--config", filepath.Join(empty, "absent.yaml"),
	}
}

func TestVersionAndHelp(t *testing.T) {
	if got := run(t, "version"); got.code != 0 || !strings.HasPrefix(got.stdout, "halite-node ") {
		t.Errorf("version = %+v", got)
	}
	if got := run(t, "help"); got.code != 0 || !strings.Contains(got.stdout, "Usage:") {
		t.Errorf("help = %+v", got)
	}
}

func TestUnknownSubcommandIsUsageNotSuccess(t *testing.T) {
	// Exit 2 is "converged" for a state run, so a typo must not reach it
	// by a path a script would read as success. Usage failure is also 2
	// by convention, and both go to stderr, which is what tells them
	// apart from a run that produced output.
	got := run(t, "nosuchthing")
	if got.code != 2 {
		t.Errorf("exit = %d, want 2", got.code)
	}
	if !strings.Contains(got.stderr, "unknown subcommand") || got.stdout != "" {
		t.Errorf("got %+v", got)
	}

	if got := run(t); got.code != 2 || !strings.Contains(got.stderr, "Usage:") {
		t.Errorf("no arguments = %+v", got)
	}
}

func TestPhaseTwoSubcommandsSayWhy(t *testing.T) {
	for _, sub := range []string{"serve", "event"} {
		got := run(t, sub)
		if got.code != 1 {
			t.Errorf("%s exit = %d, want 1", sub, got.code)
		}
		if !strings.Contains(got.stderr, "phase 2") {
			t.Errorf("%s should name the phase: %q", sub, got.stderr)
		}
	}
}

func TestGrainsItemAnswersEveryKey(t *testing.T) {
	flags := tree(t, nil)
	got := run(t, append([]string{"grains", "item", "kernel", "osrelease", "--out", "json"}, flags...)...)
	if got.code != 0 {
		t.Fatalf("%+v", got)
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal([]byte(got.stdout), &decoded); err != nil {
		t.Fatalf("output is not JSON: %v: %s", err, got.stdout)
	}
	for _, m := range decoded {
		if len(m) != 2 || m["kernel"] == "" || m["osrelease"] == "" {
			t.Errorf("item answered %v, want both keys", m)
		}
	}

	// get is one key, and refuses a second rather than answering about
	// the first and dropping the rest.
	if got := run(t, append([]string{"grains", "get", "kernel", "osrelease"}, flags...)...); got.code == 0 {
		t.Errorf("get with two keys should fail: %+v", got)
	}
}

func TestCallRunsAModuleFunction(t *testing.T) {
	flags := tree(t, nil)
	got := run(t, append([]string{"call", "test.ping", "--out", "json"}, flags...)...)
	if got.code != 0 || !strings.Contains(got.stdout, "true") {
		t.Errorf("test.ping = %+v", got)
	}

	// An unknown function is an error naming it, not an empty result.
	got = run(t, append([]string{"call", "nosuch.function"}, flags...)...)
	if got.code == 0 || !strings.Contains(got.stderr, "nosuch.function") {
		t.Errorf("unknown function = %+v", got)
	}
}

// TestExitCodeContract is SPEC 11.8: 0 when something changed, 2 when
// there was nothing to do, non-zero-non-2 when it failed. A scheduler
// reads these, so they are the most load-bearing integers in the
// program.
func TestExitCodeContract(t *testing.T) {
	target := filepath.Join(t.TempDir(), "managed.conf")
	flags := tree(t, map[string]string{
		"top.sls": "base:\n  '*':\n    - m\n",
		"m.sls": target + `:
  file.managed:
    - contents: hello
    - mode: '0644'
`,
	})

	if got := run(t, append([]string{"state", "apply"}, flags...)...); got.code != 0 {
		t.Fatalf("a run that changes something should exit 0: %+v", got)
	}
	// contents gains a trailing newline, as Salt's does.
	if data, err := os.ReadFile(target); err != nil || string(data) != "hello\n" {
		t.Fatalf("the file was not written: %q %v", data, err)
	}

	if got := run(t, append([]string{"state", "apply"}, flags...)...); got.code != 2 {
		t.Errorf("a converged run should exit 2: %+v", got)
	}

	// Drift, and it changes again.
	os.WriteFile(target, []byte("drifted"), 0o644)
	if got := run(t, append([]string{"state", "apply"}, flags...)...); got.code != 0 {
		t.Errorf("a reconverging run should exit 0: %+v", got)
	}
	if data, _ := os.ReadFile(target); string(data) != "hello\n" {
		t.Errorf("drift was not corrected: %q", data)
	}
}

func TestTestModeChangesNothing(t *testing.T) {
	target := filepath.Join(t.TempDir(), "managed.conf")
	flags := tree(t, map[string]string{
		"top.sls": "base:\n  '*':\n    - m\n",
		"m.sls":   target + ":\n  file.managed:\n    - contents: hello\n",
	})

	got := run(t, append([]string{"state", "apply", "--test"}, flags...)...)
	if got.code != 0 {
		t.Errorf("a test run with pending work should exit 0: %+v", got)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("--test wrote the file")
	}
}

func TestCompilationErrorsAreNotExitTwo(t *testing.T) {
	flags := tree(t, map[string]string{
		"top.sls": "base:\n  '*':\n    - m\n",
		"m.sls":   "broken:\n  file.managed:\n    - contents: {{ undefined_name }}\n",
	})
	got := run(t, append([]string{"state", "apply"}, flags...)...)
	if got.code == 0 || got.code == 2 {
		t.Errorf("a failed compile must not report changed or converged: %+v", got)
	}
	if !strings.Contains(got.stderr+got.stdout, "undefined_name") {
		t.Errorf("the error should name the identifier: %+v", got)
	}
}

func TestOutputFormatsAreParseable(t *testing.T) {
	flags := tree(t, nil)
	if got := run(t, append([]string{"grains", "items", "--out", "json"}, flags...)...); got.code != 0 {
		t.Errorf("json: %+v", got)
	} else if !json.Valid([]byte(got.stdout)) {
		t.Errorf("json output does not parse: %s", got.stdout)
	}
	if got := run(t, append([]string{"grains", "items", "--out", "quiet"}, flags...)...); got.stdout != "" {
		t.Errorf("quiet printed %q", got.stdout)
	}
}

// TestUsageAdvertisesOnlyRealSubcommands reads the subcommand lists out
// of the usage text and runs each one. The usage offered `grains setval`
// for as long as it existed, and no such subcommand was ever
// implemented; an operator reading `--help` found out from an error.
func TestUsageAdvertisesOnlyRealSubcommands(t *testing.T) {
	flags := tree(t, nil)
	for _, line := range strings.Split(usage, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "halite-node" {
			continue
		}
		// `<a|b|c>` is a list of real subcommands; `<subcommand>` is a
		// placeholder for one, and there is nothing to check in it.
		spec := ""
		for _, f := range fields[2:] {
			if strings.HasPrefix(f, "<") && strings.HasSuffix(f, ">") && strings.Contains(f, "|") {
				spec = strings.Trim(f, "<>")
			}
		}
		if spec == "" {
			continue
		}
		for _, sub := range strings.Split(spec, "|") {
			got := run(t, append([]string{fields[1], sub, "x"}, flags...)...)
			if strings.Contains(got.stderr, "has no subcommand") {
				t.Errorf("`%s %s` is advertised in the usage and does not exist", fields[1], sub)
			}
		}
	}
}

// TestSaltDispatcherIsBound covers a field the compilers passed to the
// renderer and nothing ever set: `salt['pillar.get']` and its neighbours
// were undefined in every SLS and pillar file. It is ordinary Salt, and
// a real tree used it six times in four files.
func TestSaltDispatcherIsBound(t *testing.T) {
	pillarRoot := t.TempDir()
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(pillarRoot, "top.sls", "base:\n  '*':\n    - v\n")
	write(pillarRoot, "v.sls", "app:\n  port: 8080\n")

	flags := tree(t, map[string]string{
		"top.sls": "base:\n  '*':\n    - s\n",
		"s.sls": `report:
  cmd.run:
    - name: echo {{ salt['pillar.get']('app:port') }} {{ salt['grains.get']('kernel') }} {{ salt['pillar.get']('app:absent', 'fallback') }}
`,
	})
	flags = append(flags, "--pillar-root", pillarRoot)

	got := run(t, append([]string{"state", "show_lowstate", "--out", "json"}, flags...)...)
	if got.code != 0 {
		t.Fatalf("%+v", got)
	}
	var chunks []map[string]any
	if err := json.Unmarshal([]byte(got.stdout), &chunks); err != nil {
		t.Fatalf("%v: %s", err, got.stdout)
	}
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	name, _ := chunks[0]["name"].(string)
	if !strings.HasPrefix(name, "echo 8080 ") || !strings.HasSuffix(name, " fallback") {
		t.Errorf("name = %q; the pillar value, the grain, and the default should all have resolved", name)
	}

	// A pillar file may call it too, and sees the pillar built so far.
	write(pillarRoot, "top.sls", "base:\n  '*':\n    - v\n    - w\n")
	write(pillarRoot, "w.sls", "echoed: {{ salt['pillar.get']('app:port', 'unset') }}\n")
	got = run(t, append([]string{"pillar", "items", "--out", "json"}, flags...)...)
	if got.code != 0 || !strings.Contains(got.stdout, "8080") {
		t.Errorf("a pillar file calling salt['pillar.get'] = %+v", got)
	}
}

// TestCommandMatrixIsTrue reads docs/command-reference.md and checks
// every halite-node command it presents as working.
//
// A table of equivalents is the page an operator reads first and the
// page nothing else keeps honest: it is prose, so nothing fails when a
// subcommand is renamed or a module function moves. This runs each one.
func TestCommandMatrixIsTrue(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "command-reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	flags := tree(t, nil)

	checked := 0
	for _, row := range strings.Split(string(data), "\n") {
		cells := matrixRow(row)
		if len(cells) < 3 {
			continue
		}
		command := strings.Fields(strings.Trim(cells[1], "`"))
		if len(command) < 2 || command[0] != "halite-node" {
			continue
		}

		// A row that promises something in a later phase must name a
		// subcommand the binary knows and that says which phase, rather
		// than one it reports as a typo.
		if strings.HasPrefix(cells[2], "phase") {
			got := run(t, command[1])
			output := got.stdout + got.stderr
			if strings.Contains(output, "unknown subcommand") {
				t.Errorf("the matrix promises `%s` in %s, and the binary has never heard of it",
					strings.Join(command, " "), cells[2])
			} else if !strings.Contains(output, "phase") {
				t.Errorf("`%s` should say which phase it arrives in: %s",
					strings.Join(command, " "), strings.TrimSpace(output))
			}
			continue
		}
		if cells[2] != "works" {
			continue
		}
		checked++

		// The subcommand and, where there is one, the thing after it:
		// `state apply`, `grains item`, `call test.ping`. Running the
		// pair is enough to prove the dispatch knows it; the arguments
		// after it are the operator's business.
		args := []string{command[1]}
		if len(command) > 2 && !strings.HasPrefix(command[2], "-") {
			args = append(args, command[2])
		}
		got := run(t, append(args, flags...)...)
		output := got.stdout + got.stderr
		for _, broken := range []string{
			"unknown subcommand",
			"has no subcommand",
			"is not a function this build ships",
			"no function named",
		} {
			if strings.Contains(output, broken) {
				t.Errorf("the matrix presents `%s` as working, and it reports:\n  %s",
					strings.Join(command, " "), strings.TrimSpace(output))
			}
		}
	}
	if checked == 0 {
		t.Error("no halite-node row in the matrix was checked; this test has stopped checking anything")
	}
	t.Logf("checked %d halite-node commands from the matrix", checked)
}

// matrixRow splits a markdown table row into its cells, or returns nil
// for a line that is not one.
func matrixRow(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|---") {
		return nil
	}
	var cells []string
	for _, c := range strings.Split(strings.Trim(line, "|"), "|") {
		cells = append(cells, strings.TrimSpace(c))
	}
	return cells
}

// TestTransitionSwitchesTakeEffect. Three settings that SPEC names as
// the switch a tree throws during a migration, all of which were
// declared, documented, listed in the configuration reference, and read
// by nothing.
func TestTransitionSwitchesTakeEffect(t *testing.T) {
	flags := tree(t, map[string]string{
		"top.sls": "base:\n  '*':\n    - s\n",
		"s.sls":   "flag:\n  cmd.run:\n    - name: /bin/echo\n    - env:\n        on: yes\n",
	})

	// yaml_bool_11: false is SPEC 10.1.3's switch for a tree that has
	// been audited. With it, `on: yes` is two strings.
	got := run(t, append([]string{"state", "show_lowstate", "--out", "json"}, flags...)...)
	if !strings.Contains(got.stderr, "resolves to the boolean") {
		t.Errorf("the default should warn about a YAML 1.1 boolean: %q", got.stderr)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "node.yaml"), []byte("yaml_bool_11: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withConfig := append(append([]string{}, flags...), "--config", filepath.Join(root, "node.yaml"))
	got = run(t, append([]string{"state", "show_lowstate", "--out", "json"}, withConfig...)...)
	if strings.Contains(got.stderr, "resolves to the boolean") {
		t.Errorf("yaml_bool_11: false should turn the coercion off: %q", got.stderr)
	}
	if !strings.Contains(got.stdout, `"yes"`) {
		t.Errorf("`on: yes` should stay two strings: %s", got.stdout)
	}

	// legacy_arg_parse is SPEC 9.2's, and logs every coercion because
	// the log is the list of arguments that need a type or a quote.
	got = run(t, append([]string{"call", "test.echo", "1.0", "--out", "json"}, flags...)...)
	if !strings.Contains(got.stdout, `"1.0"`) {
		t.Errorf("an argument should be a string by default: %s", got.stdout)
	}
	got = run(t, append([]string{"call", "test.echo", "1.0", "--legacy-arg-parse", "--out", "json"}, flags...)...)
	if strings.Contains(got.stdout, `"1.0"`) {
		t.Errorf("--legacy-arg-parse should coerce: %s", got.stdout)
	}
	if !strings.Contains(got.stderr, "was read as float") {
		t.Errorf("every coercion should be logged: %q", got.stderr)
	}
}

// TestEnvironmentControls covers SPEC 28.3's pair to state_allowlist and
// state_denylist. They were declared, documented, listed in the
// configuration reference, and enforced by nothing — so a node
// restricted to `base` would apply whatever a `--env` asked for. A
// control that does not control is worse than an absent one, because
// someone is relying on it.
func TestEnvironmentControls(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	allow := write("allow.yaml", "env_allowlist:\n  - base\n  - prod\n")
	deny := write("deny.yaml", "env_denylist:\n  - '*-test'\n")
	both := write("both.yaml", "env_allowlist:\n  - '*'\nenv_denylist:\n  - forbidden\n")

	flags := tree(t, nil)
	call := func(cfg, env string) result {
		args := append([]string{"grains", "item", "id", "--out", "json"}, flags...)
		args = append(args, "--config", cfg)
		if env != "" {
			args = append(args, "--env", env)
		}
		return run(t, args...)
	}

	if got := call(allow, ""); got.code != 0 {
		t.Errorf("the default environment is in the allowlist: %+v", got)
	}
	if got := call(allow, "prod"); got.code != 0 {
		t.Errorf("prod is in the allowlist: %+v", got)
	}
	got := call(allow, "staging")
	if got.code == 0 {
		t.Error("an environment outside the allowlist should be refused")
	}
	if !strings.Contains(got.stderr, "env_allowlist") || !strings.Contains(got.stderr, "staging") {
		t.Errorf("the refusal should name the environment and the setting: %q", got.stderr)
	}

	// The denylist takes patterns, and an empty allowlist is no
	// restriction rather than a refusal of everything.
	if got := call(deny, "prod"); got.code != 0 {
		t.Errorf("an empty allowlist should not refuse anything: %+v", got)
	}
	if got := call(deny, "web-test"); got.code == 0 {
		t.Error("a pattern in the denylist should refuse")
	}

	// The denylist wins over the allowlist, which is what makes it worth
	// having alongside one.
	if got := call(both, "anything"); got.code != 0 {
		t.Errorf("`*` should allow: %+v", got)
	}
	if got := call(both, "forbidden"); got.code == 0 {
		t.Error("the denylist should beat an allowlist of `*`")
	}
}

// TestNodeIDModifiers covers Salt's `minion_id_lowercase` and
// `minion_id_remove_domain`, which the compatibility shim translates
// into keys nothing read — so a Salt configuration carrying them
// produced a different identity under halite, and an identity is what
// pillar and targeting are keyed by.
func TestNodeIDModifiers(t *testing.T) {
	root := t.TempDir()
	cfg := func(name, body string) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	flags := tree(t, nil)
	id := func(config string, extra ...string) string {
		args := append([]string{"grains", "get", "id", "--out", "json"}, flags...)
		args = append(args, "--config", config)
		args = append(args, extra...)
		got := run(t, args...)
		if got.code != 0 {
			t.Fatalf("%+v", got)
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(got.stdout), &m); err != nil {
			t.Fatalf("%v: %s", err, got.stdout)
		}
		for _, v := range m {
			return v
		}
		return ""
	}

	plain := id(cfg("none.yaml", "{}\n"))
	if plain == "" {
		t.Fatal("no identity was resolved")
	}

	// An explicit identity is the answer rather than a draft of it, so
	// the modifiers leave it alone. Salt draws the line in the same
	// place.
	explicit := id(cfg("explicit.yaml", "node_id: Web1.Example.COM\nnode_id_lowercase: true\n"))
	if explicit != "Web1.Example.COM" {
		t.Errorf("an explicit node_id should be used as written, got %q", explicit)
	}

	// A detected one is modified. The host this runs on may or may not
	// have a domain, so the assertion is about the relationship.
	stripped := id(cfg("strip.yaml", "node_id_remove_domain: true\n"))
	if host, _, found := strings.Cut(plain, "."); found {
		if stripped != host {
			t.Errorf("remove_domain gave %q, want %q", stripped, host)
		}
		// Naming the domain strips that one specifically.
		_, domain, _ := strings.Cut(plain, ".")
		named := id(cfg("named.yaml", "node_id_remove_domain: "+domain+"\n"))
		if named != host {
			t.Errorf("removing %q gave %q, want %q", domain, named, host)
		}
		// A domain the identity does not end in is left alone.
		other := id(cfg("other.yaml", "node_id_remove_domain: nowhere.invalid\n"))
		if other != plain {
			t.Errorf("removing a domain that does not match gave %q, want %q", other, plain)
		}
	} else if stripped != plain {
		t.Errorf("an identity with no domain should be unchanged, got %q", stripped)
	}
}
