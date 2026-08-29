package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// See cmd/halite-node/cli_test.go: the CLI is tested by re-executing this
// test binary rather than by building the real one, so it works where the
// Go toolchain is not reachable.
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

func salttree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestVersionAndUnknownSubcommand(t *testing.T) {
	if got := run(t, "version"); got.code != 0 || !strings.HasPrefix(got.stdout, "halite-hub ") {
		t.Errorf("version = %+v", got)
	}
	if got := run(t, "nosuchthing"); got.code != 2 || !strings.Contains(got.stderr, "unknown subcommand") {
		t.Errorf("unknown subcommand = %+v", got)
	}
	// The subcommands named in the usage but not built must say so,
	// rather than fail as typos. Either a phase they are waiting for or
	// a plain "not built": a feature whose phase came and went without
	// it is not in a later phase.
	for _, sub := range []string{"files"} {
		got := run(t, sub)
		if got.code == 0 ||
			(!strings.Contains(got.stderr, "phase") && !strings.Contains(got.stderr, "not built")) {
			t.Errorf("%s = %+v", sub, got)
		}
	}
	// `ssh` is built now. Bare, it describes itself and exits non-zero,
	// because a call with no target and no function is a mistake.
	if got := run(t, "ssh"); got.code == 0 || !strings.Contains(got.stderr, "no agent") {
		t.Errorf("ssh = %+v", got)
	}

	// `runner` and `orch` are built. Bare, each describes itself and
	// exits non-zero, because a call with no function or subcommand is
	// a mistake, not a request for nothing.
	if got := run(t, "runner"); got.code != 2 || !strings.Contains(got.stderr, "module.function") {
		t.Errorf("runner = %+v", got)
	}
	if got := run(t, "orch"); got.code != 2 || !strings.Contains(got.stderr, "orch run") {
		t.Errorf("orch = %+v", got)
	}
}

// TestMigrateFailOnLevels is the gate documented in
// docs/migrating-from-salt.md. A tree with a review finding and no
// blocking one passes the default gate and fails the stricter one.
func TestMigrateFailOnLevels(t *testing.T) {
	tree := salttree(t, map[string]string{
		"top.sls": "base:\n  '*':\n    - web\n",
		// `yes` is a YAML 1.1 boolean, which is a review finding: the
		// tree compiles and the value may not be what was written. An
		// unquoted mode would be blocking, since compilation refuses it.
		"web.sls": "nginx:\n  service.running:\n    - enable: yes\n",
	})

	got := run(t, "migrate", tree)
	if got.code != 0 {
		t.Errorf("a review finding should pass the default gate: %+v", got)
	}
	if !strings.Contains(got.stdout, "REVIEW") {
		t.Errorf("the report should carry the finding: %s", got.stdout)
	}

	if got := run(t, "migrate", tree, "--fail-on", "review"); got.code != 1 {
		t.Errorf("--fail-on review should fail here: %+v", got)
	}
	if got := run(t, "migrate", tree, "--fail-on", "note"); got.code != 1 {
		t.Errorf("--fail-on note should fail here: %+v", got)
	}

	// A level that is not a level is refused. Falling through to the
	// default means a pipeline asking for a stricter gate silently gets
	// a looser one.
	got = run(t, "migrate", tree, "--fail-on", "reveiw")
	if got.code != 1 || !strings.Contains(got.stderr, "not a level") {
		t.Errorf("a misspelled level should be refused by name: %+v", got)
	}
}

func TestMigrateCleanTreeAndJSON(t *testing.T) {
	tree := salttree(t, map[string]string{
		"top.sls": "base:\n  '*':\n    - web\n",
		"web.sls": "/etc/motd:\n  file.managed:\n    - mode: '0644'\n",
	})

	got := run(t, "migrate", tree)
	if got.code != 0 {
		t.Errorf("a clean tree should pass: %+v", got)
	}

	got = run(t, "migrate", tree, "--out", "json")
	if got.code != 0 || !json.Valid([]byte(got.stdout)) {
		t.Errorf("json report = %+v", got)
	}
}

func TestMigrateNeedsATree(t *testing.T) {
	if got := run(t, "migrate"); got.code == 0 {
		t.Errorf("migrate with no tree should fail: %+v", got)
	}
	if got := run(t, "migrate", filepath.Join(t.TempDir(), "absent")); got.code == 0 {
		t.Errorf("migrate on a missing tree should fail: %+v", got)
	}
}

// TestCommandMatrixIsTrue is the halite-hub half of the check in
// cmd/halite-node. Every row the matrix presents as working must work,
// and every row it presents as arriving in a later phase must say so
// rather than being an unknown subcommand — a matrix that promises
// `halite-hub keys accept` in phase 2 is wrong if the binary has never
// heard of `keys`.
func TestCommandMatrixIsTrue(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "command-reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	tree := salttree(t, map[string]string{
		"top.sls": "base:\n  '*':\n    - web\n",
		"web.sls": "a:\n  cmd.run:\n    - name: /bin/echo\n",
	})

	works, phased := 0, 0
	for _, row := range strings.Split(string(data), "\n") {
		cells := matrixRow(row)
		if len(cells) < 3 {
			continue
		}
		// Only what is inside the first pair of backticks. Trimming
		// the cell instead let trailing prose — "`halite-hub metrics`
		// (the hub's own exposition)" — become part of the command, so
		// the row was checked against a subcommand nobody had written.
		command := strings.Fields(backticked(cells[1]))
		if len(command) < 2 || command[0] != "halite-hub" {
			continue
		}
		status := cells[2]

		args := []string{command[1]}
		switch command[1] {
		case "migrate", "lint":
			args = append(args, tree)
		case "serve", "run":
			// Asked what they are rather than run: one opens a
			// listener and creates an enrollment CA, and the other
			// dispatches a job to the fleet.
			args = append(args, "--help")
		case "ssh":
			// Asked what it is rather than run: it would reach for a
			// roster and open ssh connections to whatever is in it.
			args = append(args, "--help")
		case "runner":
			// `runner doc` reads the inventory out of this binary
			// without a hub, and the phase it prints is exactly what
			// the row claims. Calling the runner itself would need an
			// operator certificate and a listening hub.
			if len(command) > 2 {
				args = append(args, "doc", command[2])
			} else {
				args = append(args, "list")
			}
		}
		got := run(t, args...)
		output := got.stdout + got.stderr

		switch {
		case status == "works":
			works++
			if strings.Contains(output, "unknown subcommand") {
				t.Errorf("the matrix presents `%s` as working: %s",
					strings.Join(command, " "), strings.TrimSpace(output))
			}
		case strings.HasPrefix(status, "phase"), status == "not built":
			phased++
			// The binary must know the name and say why it does not
			// work, rather than reporting it as a typo.
			if strings.Contains(output, "unknown subcommand") {
				t.Errorf("the matrix promises `%s` (%s), and the binary has never heard of it",
					strings.Join(command, " "), status)
			}
			// "not built" is a status in its own right. A feature whose
			// phase has come and gone without it is not in a later
			// phase, and saying it is would be a promise nobody made.
			said := strings.ToLower(output)
			if !strings.Contains(said, "phase") && !strings.Contains(said, "not built") {
				t.Errorf("`%s` should say why it does not work: %s",
					strings.Join(command, " "), strings.TrimSpace(output))
			}
		}
	}
	if works == 0 || phased == 0 {
		t.Errorf("checked %d working and %d phased rows; this test has stopped checking something", works, phased)
	}
	t.Logf("checked %d working and %d phased halite-hub commands", works, phased)
}

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

// TestLintDecryptsAndRedacts. `lint` renders, and rendering a
// `#!yaml|gpg` file decrypts, so this program holds a secret even though
// it never applies a state. It had no redactor at all, and no gpg
// settings either — so a file the node renders perfectly well was
// reported as unrenderable whenever the keyring was not the one in the
// environment.
func TestLintDecryptsAndRedacts(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("no gpg on PATH; SPEC 12.6 drives the system binary")
	}
	const secret = "s3cret-value-from-the-pillar"

	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	gpg := func(args ...string) *exec.Cmd {
		c := exec.Command("gpg", args...)
		c.Env = append(os.Environ(), "GNUPGHOME="+home)
		return c
	}
	if out, err := gpg("--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--quick-generate-key", "hub lint <t@example.invalid>", "default", "default", "never").
		CombinedOutput(); err != nil {
		t.Skipf("a throwaway key could not be generated here: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("gpgconf", "--kill", "gpg-agent").Run() })

	enc := gpg("--batch", "--yes", "--trust-model", "always", "--encrypt", "--armor", "-r", "t@example.invalid")
	enc.Stdin = strings.NewReader(secret)
	armored, err := enc.Output()
	if err != nil {
		t.Fatalf("encrypting: %v", err)
	}
	indented := strings.ReplaceAll(strings.TrimRight(string(armored), "\n"), "\n", "\n    ")

	dir := t.TempDir()
	// `on:` gives the renderer something to warn about, so the run has
	// output for a secret to hide in.
	sls := filepath.Join(dir, "secrets.sls")
	if err := os.WriteFile(sls, []byte("#!yaml|gpg\non: yes\ntoken: |\n    "+indented+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "hub.yaml")
	if err := os.WriteFile(cfg, []byte("gpg_home: "+home+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := run(t, "lint", sls, "--config", cfg)
	all := got.stdout + got.stderr
	if strings.Contains(all, secret) {
		t.Errorf("the decrypted value reached lint's output:\n%s", all)
	}
	// The file renders, which is what proves the gpg settings were read:
	// without them the keyring is not found and this reports a failure.
	if !strings.Contains(all, "renders and parses") {
		t.Errorf("the file should render with the configured keyring:\n%s", all)
	}
}

// `runner list` answers from the build rather than from the hub, so an
// operator who cannot reach the hub can still find out what exists —
// and a pending runner has to say when it arrives, or the list reads as
// though everything works.
func TestRunnerListShowsBuiltAndPendingAlike(t *testing.T) {
	got := run(t, "runner", "list")
	if got.code != 0 {
		t.Fatalf("runner list = %+v", got)
	}
	for _, want := range []string{"manage.up", "jobs.lookup_jid", "state.orchestrate", "queue.insert"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("runner list does not mention %s", want)
		}
	}
	if !strings.Contains(got.stdout, "not built") {
		t.Error("runner list does not distinguish the runners that are not built")
	}

	doc := run(t, "runner", "doc", "manage.status")
	if doc.code != 0 || !strings.Contains(doc.stdout, "manage.status") {
		t.Errorf("runner doc = %+v", doc)
	}
	pending := run(t, "runner", "doc", "queue.insert")
	if pending.code != 0 || !strings.Contains(pending.stdout, "Not built yet") {
		t.Errorf("runner doc queue.insert = %+v", pending)
	}
	// And it says what it is waiting for, not just that it is waiting.
	if !strings.Contains(pending.stdout, "durable work queue") {
		t.Errorf("runner doc queue.insert says nothing useful: %+v", pending)
	}
	if bad := run(t, "runner", "nosuchrunner"); bad.code == 0 {
		t.Errorf("a runner name with no module was accepted: %+v", bad)
	}
}

// backticked returns the text inside the first pair of backticks, or
// the whole cell when there is no pair.
func backticked(cell string) string {
	_, rest, found := strings.Cut(cell, "`")
	if !found {
		return cell
	}
	inside, _, found := strings.Cut(rest, "`")
	if !found {
		return cell
	}
	return inside
}

// Salt writes a grain match as `-G 'os:FreeBSD' test.ping`, and the
// argument parser gives a single-letter flag the next token as its
// value — so the target arrives in the flag, not in the positionals.
//
// `run` read those flags with args.Bool, which returns the default for
// a value like "os:FreeBSD". Both the target and the kind were lost, and
// the operator was told the command needed a target they had just given.
func TestAMatcherFlagCarriesTheTarget(t *testing.T) {
	for _, c := range []struct {
		name       string
		argv       []string
		wantKind   string
		wantTarget string
		wantFun    string
	}{
		{"short grain", []string{"-G", "os:FreeBSD", "test.ping"}, "G", "os:FreeBSD", "test.ping"},
		{"long grain", []string{"--grain", "os:FreeBSD", "test.ping"}, "grain", "os:FreeBSD", "test.ping"},
		{"list", []string{"-L", "a.example,b.example", "test.ping"}, "L", "a.example,b.example", "test.ping"},
		{"plain glob", []string{"*", "test.ping"}, "", "*", "test.ping"},
		{"glob with a kind flag before it", []string{"-E", "web.*", "test.ping"}, "E", "web.*", "test.ping"},
	} {
		t.Run(c.name, func(t *testing.T) {
			args, err := cli.Parse(c.argv)
			if err != nil {
				t.Fatal(err)
			}
			kind, target, fun, _, err := resolveTarget(args)
			if err != nil {
				t.Fatal(err)
			}
			if kind != c.wantKind {
				t.Errorf("kind = %q, want %q", kind, c.wantKind)
			}
			if target != c.wantTarget {
				t.Errorf("target = %q, want %q", target, c.wantTarget)
			}
			if fun != c.wantFun {
				t.Errorf("function = %q, want %q", fun, c.wantFun)
			}
		})
	}
}

// splitCommand splits a documented command line, honouring the single
// quotes the matrix uses around a target expression.
func splitCommand(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '\'':
			quoted = !quoted
		case r == ' ' && !quoted:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// TestCommandMatrixTargetsParse closes the half of TestCommandMatrixIsTrue
// that `--help` cannot reach.
//
// That test asks `run` and `ssh` what they are rather than running them,
// which is right — one dispatches to a fleet and the other opens ssh
// connections — but it means the flags in a row are never read. The
// matrix promised `halite-hub run -G 'os:FreeBSD' state.apply` worked
// while the target was being swallowed by the flag, and said so for
// months.
//
// Parsing is the part that can be checked without doing anything: a row
// has to yield a target and a function.
func TestCommandMatrixTargetsParse(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "command-reference.md"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, row := range strings.Split(string(data), "\n") {
		cells := matrixRow(row)
		if len(cells) < 3 || !strings.Contains(cells[2], "works") {
			continue
		}
		text := backticked(cells[1])
		// A row written with an ellipsis is an illustration of one flag
		// rather than a whole command, and has no target to find.
		if strings.Contains(text, "…") || strings.Contains(text, "...") {
			continue
		}
		command := splitCommand(text)
		if len(command) < 3 || command[0] != "halite-hub" {
			continue
		}
		if command[1] != "run" && command[1] != "ssh" {
			continue
		}
		args, err := cli.Parse(command[2:])
		if err != nil {
			t.Errorf("%s does not parse: %v", backticked(cells[1]), err)
			continue
		}
		_, target, fun, _, err := resolveTarget(args)
		if err != nil {
			t.Errorf("the matrix says %q works, and it yields no target: %v",
				backticked(cells[1]), err)
			continue
		}
		checked++
		if target == "" || fun == "" {
			t.Errorf("%s yields target %q and function %q",
				backticked(cells[1]), target, fun)
		}
	}
	if checked == 0 {
		t.Error("no run or ssh rows were checked; this check has stopped checking")
	}
	t.Logf("checked %d documented target forms", checked)
}
