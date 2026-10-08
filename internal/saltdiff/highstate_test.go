package saltdiff

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/runner"
	"github.com/edlitmus/halite/internal/value"
)

// halite renders a state run as Salt's highstate outputter does, and the
// proof is Salt's own outputter.
//
// Salt runs a tree that draws every shape the outputter treats
// differently, and returns it as JSON. That one return is then rendered
// twice: by Salt's highstate outputter, from Salt's own Python, and by
// runner.Highstate, after value.DecodeJSON, which is how the hub reads a
// node's return. Nothing differs between the two inputs, timings
// included, so the outputs must be identical, byte for byte. Once for a
// real run and once in test mode.
//
// halite's node printed `Result: succeeded` and a one-line summary of
// its own, and the hub indented each block under the node's name; the
// owner asked for both to match Salt. DIVERGENCE 5.244, 5.245.
func TestHighstateMatchesSalt(t *testing.T) {
	saltcall := saltCall(t)
	python := saltPython(t, saltcall)
	tree := corpusTree{
		name:   "highstate",
		states: filepath.Join("testdata", "highstate"),
		id:     nodeID,
	}
	abs, err := filepath.Abs(tree.states)
	if err != nil {
		t.Fatal(err)
	}
	tree.states = abs

	for _, test := range []bool{false, true} {
		args := []string{"output"}
		if test {
			args = append(args, "test=True")
		}
		raw := saltRun(t, saltcall, tree, "state.sls", args...)

		want := saltHighstate(t, python, raw)

		decoded, err := value.DecodeJSON(raw)
		if err != nil {
			t.Fatalf("Salt's return is not JSON: %v\n%s", err, raw)
		}
		top, ok := decoded.(*value.Map)
		if !ok || top.Len() != 1 {
			t.Fatalf("Salt's return is not one host: %s", raw)
		}
		e := top.Entries()[0]
		returns, ok := e.Val.(*value.Map)
		if !ok {
			t.Fatalf("Salt's return for %v is not a map: %s", e.Key, raw)
		}
		got := runner.Highstate(value.KeyString(e.Key), returns, nil)

		if got != want {
			t.Errorf("test=%v: halite's highstate output differs from Salt's for the same return.\n%s",
				test, lineDiff(want, got))
		}
	}
}

// saltPython finds the interpreter Salt runs under, which is the one
// that can import it. The onedir build ships it beside salt-call;
// otherwise salt-call's own #! line names it.
func saltPython(t *testing.T, saltcall string) string {
	t.Helper()
	candidates := []string{
		filepath.Join(filepath.Dir(saltcall), "bin", "python3"),
	}
	if f, err := os.Open(saltcall); err == nil {
		line, _ := bufio.NewReader(f).ReadString('\n')
		f.Close()
		if strings.HasPrefix(line, "#!") {
			fields := strings.Fields(strings.TrimPrefix(line, "#!"))
			if len(fields) > 0 {
				candidates = append(candidates, fields[0])
			}
		}
	}
	for _, p := range candidates {
		if err := exec.Command(p, "-c", "import salt.output").Run(); err == nil {
			return p
		}
	}
	// In the container, Salt is there by construction, so not finding
	// its interpreter is a broken gate rather than an absent one.
	if os.Getenv("HALITE_SALTDIFF_RESULTS") != "" {
		t.Fatalf("no Python that can import salt beside %s; tried %v", saltcall, candidates)
	}
	t.Skipf("no Python that can import salt beside %s; tried %v", saltcall, candidates)
	return ""
}

// saltHighstate renders a return with Salt's highstate outputter, with
// a masterless node's default options and colour off, which is what
// `salt-call --local --no-color` prints.
func saltHighstate(t *testing.T, python string, raw []byte) string {
	t.Helper()
	dir := t.TempDir()
	config := "file_client: local\ncachedir: " + filepath.Join(dir, "cache") +
		"\nroot_dir: " + dir + "\nid: " + nodeID + "\ncolor: False\n"
	path := filepath.Join(dir, "minion") // lexicon:allow — Salt requires this filename
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	const script = `
import json, sys
import salt.config, salt.output
opts = salt.config.minion_config(sys.argv[1])
opts["color"] = False
sys.stdout.write(salt.output.out_format(json.load(sys.stdin), "highstate", opts) + "\n")
`
	cmd := exec.Command(python, "-c", script, path)
	cmd.Stdin = strings.NewReader(string(raw))
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("Salt's highstate outputter: %v\n%s", err, stderr)
	}
	return string(out)
}

// lineDiff shows two renderings line by line, marking where they part.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	n := max(len(w), len(g))
	for i := 0; i < n; i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		mark := "  "
		if wl != gl {
			mark = "! "
		}
		b.WriteString(mark + "salt:   " + strings.ReplaceAll(wl, " ", "·") + "\n")
		if wl != gl {
			b.WriteString(mark + "halite: " + strings.ReplaceAll(gl, " ", "·") + "\n")
		}
	}
	return b.String()
}
