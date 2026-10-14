package saltdiff

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// pillarEnvCase is a pillar served from more than one environment, and
// the `pillarenv` it is compiled with ("" for unset).
type pillarEnvCase struct {
	name      string
	pillarenv string
	// files maps env -> file -> contents. Each environment gets a root.
	files map[string]map[string]string
}

// The keys deliberately do not collide across environments: Salt reads
// the environments out of a Python set, so which one wins a shared key
// changes from run to run, and a comparison of that would be noise.
var pillarEnvCases = []pillarEnvCase{
	{
		// Two repositories, each with its own top file under its own
		// heading. Unset, Salt merges both; halite compiled base alone.
		name: "unset-merges-every-environment",
		files: map[string]map[string]string{
			"base": {"top.sls": "base:\n  '*':\n    - common\n", "common.sls": "common_key: from-base\n"},
			"ref":  {"top.sls": "ref:\n  '*':\n    - refapp\n", "refapp.sls": "ref_key: from-ref\n"},
		},
	},
	{
		name:      "set-selects-one-environment",
		pillarenv: "ref",
		files: map[string]map[string]string{
			"base": {"top.sls": "base:\n  '*':\n    - common\n", "common.sls": "common_key: from-base\n"},
			"ref":  {"top.sls": "ref:\n  '*':\n    - refapp\n", "refapp.sls": "ref_key: from-ref\n"},
		},
	},
	{
		// One top file with two headings: the ref: entry is read from
		// ref's root, which halite read from base's.
		name: "an-entry-is-read-from-its-headings-environment",
		files: map[string]map[string]string{
			"base": {
				"top.sls":    "base:\n  '*':\n    - common\nref:\n  '*':\n    - refapp\n",
				"common.sls": "common_key: from-base\n",
			},
			"ref": {"refapp.sls": "ref_key: from-ref\n"},
		},
	},
	{
		// prod's top file lists its entry under base:. With pillarenv
		// prod only the prod: heading counts, so nothing is delivered.
		name:      "set-ignores-other-headings",
		pillarenv: "prod",
		files: map[string]map[string]string{
			"base": {"top.sls": "base:\n  '*':\n    - v\n", "v.sls": "base_key: from-base\n"},
			"prod": {"top.sls": "base:\n  '*':\n    - v\n", "v.sls": "prod_key: from-prod\n"},
		},
	},
}

// TestPillarEnvironmentsMatchSalt compiles each case with Salt and with
// halite and requires the same pillar. DIVERGENCE 5.288.
func TestPillarEnvironmentsMatchSalt(t *testing.T) {
	saltcall := saltCall(t)
	for _, c := range pillarEnvCases {
		t.Run(c.name, func(t *testing.T) {
			roots := writePillarEnvs(t, c.files)
			theirs := saltPillarEnvs(t, saltcall, roots, c.pillarenv)
			ours := halitePillarEnvs(t, roots, c.pillarenv)

			var oursJSON map[string]any
			encoded, err := value.EncodeJSON(ours, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &oursJSON); err != nil {
				t.Fatal(err)
			}
			if got, want := canonicalJSON(t, oursJSON), canonicalJSON(t, theirs); got != want {
				t.Errorf("pillar differs\nhalite: %s\nsalt:   %s", got, want)
			}
		})
	}
}

func writePillarEnvs(t *testing.T, files map[string]map[string]string) map[string][]string {
	t.Helper()
	dir := t.TempDir()
	roots := map[string][]string{}
	for env, fs := range files {
		root := filepath.Join(dir, env)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range fs {
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		roots[env] = []string{root}
	}
	return roots
}

func saltPillarEnvs(t *testing.T, saltcall string, roots map[string][]string, pillarenv string) map[string]any {
	t.Helper()
	dir := t.TempDir()
	states := filepath.Join(dir, "states")
	if err := os.MkdirAll(states, 0o755); err != nil {
		t.Fatal(err)
	}
	envs := make([]string, 0, len(roots))
	for env := range roots {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	var b strings.Builder
	fmt.Fprintf(&b, "file_client: local\nfile_roots:\n  base: [%s]\npillar_roots:\n", states)
	for _, env := range envs {
		fmt.Fprintf(&b, "  %s: [%s]\n", env, roots[env][0])
	}
	if pillarenv != "" {
		fmt.Fprintf(&b, "pillarenv: %s\n", pillarenv)
	}
	fmt.Fprintf(&b, "cachedir: %s\nroot_dir: %s\nid: %s\n", filepath.Join(dir, "cache"), dir, nodeID)
	if err := os.WriteFile(filepath.Join(dir, "minion"), []byte(b.String()), 0o644); err != nil { // lexicon:allow — Salt requires this filename
		t.Fatal(err)
	}
	argv := append([]string{"--config-dir=" + dir, "--local", "pillar.items"}, pillarArgs(t, saltcall)...)
	argv = append(argv, "--out=json", "--log-level=quiet")
	cmd := exec.Command(saltcall, argv...)
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	out, err := cmd.Output()
	if err != nil && !looksLikeJSON(out) {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("salt-call pillar.items: %v\n%s", err, stderr)
	}
	var wrapper struct {
		Local map[string]any `json:"local"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		t.Fatalf("decoding salt's pillar: %v\n%s", err, out)
	}
	return wrapper.Local
}

func halitePillarEnvs(t *testing.T, roots map[string][]string, pillarenv string) *value.Map {
	t.Helper()
	g, _ := grains.Collect(grains.Options{NodeID: nodeID})
	env := pillarenv
	if env == "" {
		env = "base"
	}
	c := pillar.Compiler{
		Loader: fileserver.NewRoots(roots),
		Config: pillar.Config{
			Env: env, AllEnvs: pillarenv == "", NodeID: nodeID, Grains: g,
			ConfigValues: value.NewMap(0), Undefined: template.Strict, Local: true,
		},
	}
	out := c.Compile()
	if err := out.Err(); err != nil {
		t.Fatalf("compiling: %v", err)
	}
	return out.Pillar
}

func canonicalJSON(t *testing.T, v any) string {
	t.Helper()
	if v == nil {
		v = map[string]any{}
	}
	b, err := json.Marshal(v) // encoding/json sorts map keys
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
