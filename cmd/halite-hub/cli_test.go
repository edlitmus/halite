package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	// The phase 2 subcommands are named in the usage and must say why
	// rather than fail as typos.
	for _, sub := range []string{"serve", "run", "keys", "jobs"} {
		got := run(t, sub)
		if got.code == 0 || !strings.Contains(got.stderr, "phase 2") {
			t.Errorf("%s = %+v", sub, got)
		}
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
		command := strings.Fields(strings.Trim(cells[1], "`"))
		if len(command) < 2 || command[0] != "halite-hub" {
			continue
		}
		status := cells[2]

		args := []string{command[1]}
		if command[1] == "migrate" || command[1] == "lint" {
			args = append(args, tree)
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
		case strings.HasPrefix(status, "phase"):
			phased++
			// The binary must know the name and say which phase, rather
			// than reporting it as a typo.
			if strings.Contains(output, "unknown subcommand") {
				t.Errorf("the matrix promises `%s` in %s, and the binary has never heard of it",
					strings.Join(command, " "), status)
			}
			if !strings.Contains(output, "phase") {
				t.Errorf("`%s` should say which phase it arrives in: %s",
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
