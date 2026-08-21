// Package saltdiff holds SPEC section 31's differential gate: the same
// state tree compiled by halite and by Salt, compared.
//
// SPEC 31 names this the primary correctness gate, on the reasoning that
// no amount of unit testing establishes that an existing tree means the
// same thing under a reimplementation. Only the reference implementation
// can say that.
//
// The gate skips, loudly, where Salt is not installed. It must never pass
// quietly for that reason:
//
//	python3 -m venv /tmp/salt && /tmp/salt/bin/pip install salt
//	HALITE_SALT_CALL=/tmp/salt/bin/salt-call go test ./internal/saltdiff/
//
// Set HALITE_SALTDIFF_REGEN=1 to print the deviation table.
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

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

const nodeID = "saltdiff"

// deviation records one chunk the two implementations compile
// differently. The table is enforced in both directions: an unrecorded
// difference fails, and so does a row whose difference has gone away.
type deviation struct {
	tree  string
	chunk string
	// salt is the major version the difference was observed under, or
	// empty for every version. The two supported majors disagree with
	// each other about what show_lowstate projects, so a row that did
	// not say which one it meant would be unfalsifiable.
	salt   string
	reason string
}

const (
	// devOption marks an argument halite reads as a per-state option of
	// SPEC 11.7 and Salt passes through to the module. The schemas
	// differ; the behaviour does not, because halite applies the option
	// to the state's execution.
	devOption = "halite reads this as a per-state option of SPEC 11.7, and applies it there"

	// devShowLowstate marks a place where Salt's show_lowstate is not
	// the order it will run in. 3006 resolves the reversed requisites
	// (`require_in` and its siblings) while executing rather than while
	// compiling, so they are absent from what it prints; 3008 resolves
	// them into the low state, as halite does. The runs agree; the
	// projection does not.
	devShowLowstate = "Salt 3006 applies the reversed requisites at run time, so show_lowstate does not carry them"
)

var deviations = []deviation{
	{"types", "args/typed_arguments|cmd.run|echo typed", "", devOption},
	{"requisites", "ordering", "3006", devShowLowstate},
}

// saltVersion asks salt-call what it is, so that a deviation row can name
// the version it was observed under.
func saltVersion(t *testing.T, saltcall string) string {
	t.Helper()
	cmd := exec.Command(saltcall, "--version")
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s --version: %v", saltcall, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		t.Fatalf("cannot read a version out of %q", out)
	}
	return fields[1]
}

// saltCall finds an installed salt-call, or says why the gate cannot run.
func saltCall(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("HALITE_SALT_CALL"); p != "" {
		return p
	}
	p, err := exec.LookPath("salt-call")
	if err != nil {
		t.Skip("Salt differential skipped: salt-call not found. " +
			"Set HALITE_SALT_CALL to one, or `pip install salt` into a virtualenv. " +
			"SPEC 31 calls this the primary correctness gate, so a skip here is a gap, not a pass.")
	}
	return p
}

// saltLowstate compiles a tree with Salt and returns its low state.
func saltLowstate(t *testing.T, saltcall, tree string) []map[string]any {
	t.Helper()
	dir := t.TempDir()
	pillarRoot := filepath.Join(tree, "pillar")
	if _, err := os.Stat(pillarRoot); err != nil {
		pillarRoot = filepath.Join(dir, "empty-pillar")
		os.MkdirAll(pillarRoot, 0o755)
	}
	config := fmt.Sprintf(`file_client: local
file_roots:
  base: [%s]
pillar_roots:
  base: [%s]
cachedir: %s
root_dir: %s
id: %s
`, tree, pillarRoot, filepath.Join(dir, "cache"), dir, nodeID)
	if err := os.WriteFile(filepath.Join(dir, "minion"), []byte(config), 0o644); err != nil { // lexicon:allow — Salt requires this filename
		t.Fatal(err)
	}

	cmd := exec.Command(saltcall, "--config-dir="+dir, "--local",
		"state.show_lowstate", "--out=json", "--log-level=quiet")
	// Salt reads $SHELL to decide what to run commands with, and refuses
	// to start if it does not exist. A FreeBSD host has no /bin/bash, so
	// an inherited Linux $SHELL stops the gate before it begins.
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("salt-call on %s: %v\n%s", tree, err, stderr)
	}
	var wrapper struct {
		Local []map[string]any `json:"local"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		t.Fatalf("decoding salt's low state for %s: %v\n%s", tree, err, out)
	}
	return wrapper.Local
}

// haliteLowstate compiles the same tree with halite.
func haliteLowstate(t *testing.T, tree string) []*state.Chunk {
	t.Helper()
	g, _ := grains.Collect(grains.Options{NodeID: nodeID})

	pillarDir := filepath.Join(tree, "pillar")
	if _, err := os.Stat(pillarDir); err != nil {
		pillarDir = filepath.Join(t.TempDir(), "empty-pillar")
		os.MkdirAll(pillarDir, 0o755)
	}
	pillars := pillar.Compiler{
		Loader: fileserver.NewRoots(map[string][]string{"base": {pillarDir}}),
		Config: pillar.Config{
			Env: "base", NodeID: nodeID, Grains: g,
			ConfigValues: value.NewMap(0), Undefined: template.Strict, Local: true,
		},
	}
	compiledPillar := pillars.Compile()
	if err := compiledPillar.Err(); err != nil {
		t.Fatalf("compiling the pillar of %s: %v", tree, err)
	}

	compiler := &state.Compiler{
		Loader:   fileserver.NewRoots(map[string][]string{"base": {tree}}),
		Registry: builtin.New().States.Signatures(),
		Config: state.Config{
			Env: "base", PillarEnv: "base", NodeID: nodeID, JobID: "saltdiff",
			Grains: g, Pillar: compiledPillar.Pillar, ConfigValues: value.NewMap(0),
			Undefined: template.Strict,
		},
	}
	out := compiler.CompileHighstate()
	if err := out.Err(); err != nil {
		t.Fatalf("compiling %s: %v", tree, err)
	}
	return out.Low
}

// saltMeta are the low state keys Salt adds that are not arguments.
var saltMeta = map[string]bool{
	"state": true, "name": true, "fun": true,
	"__id__": true, "__sls__": true, "__env__": true, "order": true,
}

// requisiteKeys are arguments in Salt's schema and structure in halite's.
var requisiteKeys = map[string]bool{
	"require": true, "watch": true, "onchanges": true, "onfail": true,
	"prereq": true, "use": true, "listen": true,
	"require_in": true, "watch_in": true, "onchanges_in": true,
	"onfail_in": true, "prereq_in": true, "use_in": true, "listen_in": true,
	"require_any": true, "watch_any": true, "onchanges_any": true, "onfail_any": true,
}

// identity is the part of a chunk both implementations must agree on
// exactly: which state function runs against which name, declared where.
func saltIdentity(c map[string]any) string {
	return fmt.Sprintf("%v/%v|%v.%v|%v",
		c["__sls__"], c["__id__"], c["state"], c["fun"], c["name"])
}

func haliteIdentity(c *state.Chunk) string {
	return fmt.Sprintf("%s/%s|%s.%s|%s", c.SLS, c.ID, c.State, c.Fun, c.Name)
}

// arguments renders a chunk's arguments in a form the two schemas share.
// Requisites are left out: they are compared through the ordering they
// produce, which is what they exist for, rather than through their
// spelling, which the two schemas keep differently.
func saltArguments(c map[string]any) string {
	parts := []string{}
	for k, v := range c {
		if saltMeta[k] || requisiteKeys[k] || strings.HasPrefix(k, "__") {
			continue
		}
		parts = append(parts, k+"="+shape(v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func haliteArguments(c *state.Chunk) string {
	parts := []string{}
	for _, e := range c.Args.Entries() {
		k := value.KeyString(e.Key)
		// `name` is part of the identity on both sides, and Salt keeps it
		// outside the arguments where halite keeps it inside.
		if k == "name" || requisiteKeys[k] {
			continue
		}
		parts = append(parts, k+"="+shape(jsonShape(e.Val)))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// jsonShape converts halite's parsed values into the types encoding/json
// would have produced, so that both sides go through one shape function.
func jsonShape(v any) any {
	switch t := v.(type) {
	case *value.Map:
		out := map[string]any{}
		for _, e := range t.Entries() {
			out[value.KeyString(e.Key)] = jsonShape(e.Val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = jsonShape(x)
		}
		return out
	case int64:
		return float64(t)
	case int:
		return float64(t)
	}
	return v
}

func shape(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%v", t), ".0")
	case string:
		return strconvQuote(t)
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = shape(x)
		}
		return "[" + strings.Join(parts, " ") + "]"
	case map[string]any:
		parts := make([]string, 0, len(t))
		for k, x := range t {
			parts = append(parts, k+":"+shape(x))
		}
		sort.Strings(parts)
		return "{" + strings.Join(parts, " ") + "}"
	}
	return fmt.Sprintf("%v", v)
}

func strconvQuote(s string) string { return fmt.Sprintf("%q", s) }

func trees(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("testdata", "trees"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestLowstateMatchesSalt is the gate. For every tree in the corpus, the
// two implementations must produce the same chunks, with the same
// arguments, in the same order.
func TestLowstateMatchesSalt(t *testing.T) {
	saltcall := saltCall(t)

	version := saltVersion(t, saltcall)
	t.Logf("differential against Salt %s", version)

	recorded := map[string]string{}
	for _, d := range deviations {
		if d.salt != "" && !strings.HasPrefix(version, d.salt) {
			continue
		}
		recorded[d.tree+" "+d.chunk] = d.reason
	}
	seen := map[string]bool{}

	for _, name := range trees(t) {
		t.Run(name, func(t *testing.T) {
			tree, err := filepath.Abs(filepath.Join("testdata", "trees", name))
			if err != nil {
				t.Fatal(err)
			}
			theirs := saltLowstate(t, saltcall, tree)
			ours := haliteLowstate(t, tree)

			// The chunk sequence is the state ordering, which is the
			// thing an operator most depends on and the thing a
			// reimplementation is most likely to get subtly wrong.
			theirOrder := make([]string, len(theirs))
			for i, c := range theirs {
				theirOrder[i] = saltIdentity(c)
			}
			ourOrder := make([]string, len(ours))
			for i, c := range ours {
				ourOrder[i] = haliteIdentity(c)
			}
			if strings.Join(theirOrder, "\n") != strings.Join(ourOrder, "\n") {
				key := name + " ordering"
				seen[key] = true
				if _, ok := recorded[key]; !ok {
					t.Errorf("the low state differs.\nhalite:\n  %s\nsalt:\n  %s",
						strings.Join(ourOrder, "\n  "), strings.Join(theirOrder, "\n  "))
				}
				return
			}

			for i, c := range ours {
				id := haliteIdentity(c)
				ourArgs := haliteArguments(c)
				theirArgs := saltArguments(theirs[i])
				if ourArgs == theirArgs {
					if _, ok := recorded[name+" "+id]; ok {
						t.Errorf("%s has a deviation row and now agrees; remove the row", id)
					}
					continue
				}
				key := name + " " + id
				seen[key] = true
				if _, ok := recorded[key]; !ok {
					t.Errorf("%s has different arguments.\nhalite: %s\nsalt:   %s",
						id, ourArgs, theirArgs)
				}
			}
		})
	}

	for key := range recorded {
		if !seen[key] {
			t.Errorf("%q has a deviation row and did not differ", key)
		}
	}
}
