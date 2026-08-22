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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/builtin"
	hexec "github.com/edlitmus/halite/internal/exec"
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
	// devShowLowstate marks a place where Salt's show_lowstate is not
	// the order it will run in. 3006 resolves the reversed requisites
	// (`require_in` and its siblings) while executing rather than while
	// compiling, so they are absent from what it prints; 3008 resolves
	// them into the low state, as halite does. The runs agree; the
	// projection does not.
	devShowLowstate = "Salt 3006 applies the reversed requisites at run time, so show_lowstate does not carry them"
)

var deviations = []deviation{
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
	requireVisibleTempDir(t, p)
	return p
}

// requireVisibleTempDir checks that salt-call can see a directory this
// process created. On a FreeBSD host running the test binary under the
// Linux compat layer the two have separate /tmp namespaces, so the
// configuration written for salt-call is not there when it looks. That
// is the environment rather than a difference worth reporting, and it
// must skip rather than fail — but loudly, saying which it was.
func requireVisibleTempDir(t *testing.T, saltcall string) {
	t.Helper()
	dir := t.TempDir()
	config := "file_client: local\ncachedir: " + filepath.Join(dir, "cache") +
		"\nroot_dir: " + dir + "\nid: " + nodeID + "\n"
	if err := os.WriteFile(filepath.Join(dir, "minion"), []byte(config), 0o644); err != nil { // lexicon:allow — Salt requires this filename
		t.Fatal(err)
	}
	cmd := exec.Command(saltcall, "--config-dir="+dir, "--local", "test.true", "--out=json")
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	out, err := cmd.CombinedOutput()
	if err != nil && strings.Contains(string(out), "does not exist") {
		t.Skipf("Salt differential skipped: %s cannot see %s, so the two processes "+
			"do not share a filesystem namespace. This is what running a Linux test "+
			"binary against a FreeBSD salt-call looks like. Run the gate natively.",
			saltcall, dir)
	}
}

// saltRun invokes one Salt function against a tree, masterless.
func saltRun(t *testing.T, saltcall string, tree corpusTree, function string, args ...string) []byte {
	t.Helper()
	dir := t.TempDir()
	pillarRoot := tree.pillar
	if pillarRoot == "" {
		pillarRoot = filepath.Join(dir, "empty-pillar")
		os.MkdirAll(pillarRoot, 0o755)
	} else if _, err := os.Stat(pillarRoot); err != nil {
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
`, tree.states, pillarRoot, filepath.Join(dir, "cache"), dir, tree.id)
	if err := os.WriteFile(filepath.Join(dir, "minion"), []byte(config), 0o644); err != nil { // lexicon:allow — Salt requires this filename
		t.Fatal(err)
	}

	argv := append([]string{"--config-dir=" + dir, "--local", function}, args...)
	argv = append(argv, "--out=json", "--log-level=quiet")
	cmd := exec.Command(saltcall, argv...)
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
		t.Fatalf("salt-call %s on %s: %v\n%s", function, tree.name, err, stderr)
	}
	return out
}

// saltLowstate compiles a tree with Salt and returns its low state.
func saltLowstate(t *testing.T, saltcall string, tree corpusTree) []map[string]any {
	t.Helper()
	out := saltRun(t, saltcall, tree, "state.show_lowstate")
	var wrapper struct {
		Local []map[string]any `json:"local"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		t.Fatalf("decoding salt's low state for %s: %v\n%s", tree.name, err, out)
	}
	return wrapper.Local
}

// saltPillar compiles a tree's pillar with Salt.
func saltPillar(t *testing.T, saltcall string, tree corpusTree) map[string]any {
	t.Helper()
	// Salt 3008 redacts every string in pillar.items output, which would
	// make the comparison assert only that both sides have the same
	// shape of asterisks. 3006 accepts the argument and ignores it.
	out := saltRun(t, saltcall, tree, "pillar.items", "unmask=True")
	var wrapper struct {
		Local map[string]any `json:"local"`
	}
	if err := json.Unmarshal(out, &wrapper); err != nil {
		t.Fatalf("decoding salt's pillar for %s: %v\n%s", tree.name, err, out)
	}
	return wrapper.Local
}

// halitePillar compiles the same pillar with halite.
func halitePillar(t *testing.T, tree corpusTree) *value.Map {
	t.Helper()
	g, _ := grains.Collect(grains.Options{NodeID: tree.id})

	pillarDir := tree.pillar
	if pillarDir == "" {
		pillarDir = filepath.Join(t.TempDir(), "empty-pillar")
		os.MkdirAll(pillarDir, 0o755)
	} else if _, err := os.Stat(pillarDir); err != nil {
		pillarDir = filepath.Join(t.TempDir(), "empty-pillar")
		os.MkdirAll(pillarDir, 0o755)
	}
	pillars := pillar.Compiler{
		Loader: fileserver.NewRoots(map[string][]string{"base": {pillarDir}}),
		Config: pillar.Config{
			Env: "base", NodeID: tree.id, Grains: g,
			ConfigValues: value.NewMap(0), Undefined: template.Strict, Local: true,
			// Salt lets a pillar top target on any grain; halite requires
			// the grain to be named in pillar_trusted_grains, which is a
			// recorded divergence rather than a difference to discover
			// here. The corpus names what it targets on so that the two
			// are comparing the same thing.
			TrustedGrains: []string{"kernel", "os", "os_family", "nodename", "role"},
		},
	}
	compiledPillar := pillars.Compile()
	if err := compiledPillar.Err(); err != nil {
		t.Fatalf("compiling the pillar of %s: %v", tree.name, err)
	}
	return compiledPillar.Pillar
}

// haliteLowstate compiles the same tree with halite.
func haliteLowstate(t *testing.T, tree corpusTree) []*state.Chunk {
	t.Helper()
	g, _ := grains.Collect(grains.Options{NodeID: tree.id})
	registries := builtin.New()
	pillarValues := halitePillar(t, tree)
	compiler := &state.Compiler{
		Loader:   fileserver.NewRoots(map[string][]string{"base": {tree.states}}),
		Registry: registries.States.Signatures(),
		Config: state.Config{
			Env: "base", PillarEnv: "base", NodeID: tree.id, JobID: "saltdiff",
			Grains: g, Pillar: pillarValues, ConfigValues: value.NewMap(0),
			Undefined: template.Strict,
			// `salt['pillar.get']` is ordinary in an SLS file, and a
			// harness that does not bind the dispatcher reports it as an
			// undefined name. The written corpus never used one, so this
			// hole stayed invisible until a real tree went through.
			Salt: hexec.TemplateDispatcher{
				Registry: registries.Exec,
				Context: &hexec.Context{
					Ctx: context.Background(), Grains: g, Pillar: pillarValues,
					Config: value.NewMap(0), NodeID: tree.id, Env: "base",
				},
			},
		},
	}
	out := compiler.CompileHighstate()
	if err := out.Err(); err != nil {
		t.Fatalf("compiling %s: %v", tree.name, err)
	}
	return out.Low
}

// saltMeta are the low state keys Salt adds that are not arguments.
var saltMeta = map[string]bool{
	"state": true, "name": true, "fun": true,
	"__id__": true, "__sls__": true, "__env__": true, "order": true,
}

// perStateOptions are arguments in Salt's schema and options in
// halite's: SPEC 11.7 lifts them out of the module's arguments and the
// runner applies them, so comparing their spelling compares two
// schemas rather than two behaviours. They are compared through what
// they do — `timeout` bounds the module's context, `unless` gates the
// state — and not through where each implementation files them.
var perStateOptions = map[string]bool{
	"unless": true, "onlyif": true, "creates": true, "check_cmd": true,
	"retry": true, "timeout": true, "runas": true, "umask": true,
	"failhard": true, "order": true, "parallel": true, "fire_event": true,
	"reload_modules": true, "aggregate": true,
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
		if saltMeta[k] || requisiteKeys[k] || perStateOptions[k] || strings.HasPrefix(k, "__") {
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
		if k == "name" || requisiteKeys[k] || perStateOptions[k] {
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

// corpusTree is one tree to compile with both implementations. The
// corpus ones keep their pillar in a `pillar` subdirectory; an external
// one names both halves, because a real estate does not arrange itself
// for this test's convenience.
type corpusTree struct {
	name   string
	states string
	pillar string
	// id is the node identity both implementations compile as. The
	// corpus uses a fixed one so the comparison is reproducible; a real
	// tree keys its pillar by hostname and its top file targets on it,
	// so an external tree is compiled as the host it was written for.
	id string
}

// trees returns the written corpus, plus any tree named by
// HALITE_SALTDIFF_TREES.
//
// SPEC 31 asks for "a corpus of real SLS and pillar trees from this
// estate", and the written corpus is not that: it covers constructs,
// which is the part that can be written down, and not volume, which is
// where the surprises are. Pointing this at a real tree is the cheapest
// finding-per-hour this project has.
//
//	HALITE_SALTDIFF_TREES=/srv/salt:/srv/pillar go test ./internal/saltdiff/
//
// Several may be given, separated by commas. The pillar half is optional.
func trees(t *testing.T) []corpusTree {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("testdata", "trees"))
	if err != nil {
		t.Fatal(err)
	}
	var out []corpusTree
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir, err := filepath.Abs(filepath.Join("testdata", "trees", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, corpusTree{name: e.Name(), states: dir, pillar: filepath.Join(dir, "pillar"), id: nodeID})
	}

	for _, spec := range strings.Split(os.Getenv("HALITE_SALTDIFF_TREES"), ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		states, pillar, _ := strings.Cut(spec, ":")
		host, err := os.Hostname()
		if err != nil {
			t.Fatalf("an external tree is compiled as this host, and its name could not be read: %v", err)
		}
		out = append(out, corpusTree{
			name: "external:" + filepath.Base(states), states: states, pillar: pillar, id: host,
		})
	}
	return out
}

// TestPillarMatchesSalt is the second half of what SPEC 31 asks the
// differential to compare. A pillar difference is worse than a low state
// difference, because it is silent: the tree compiles either way and the
// values inside it are wrong.
func TestPillarMatchesSalt(t *testing.T) {
	saltcall := saltCall(t)
	for _, tree := range trees(t) {
		if tree.pillar == "" {
			continue
		}
		if _, err := os.Stat(tree.pillar); err != nil {
			continue
		}
		t.Run(tree.name, func(t *testing.T) {
			theirs := saltPillar(t, saltcall, tree)
			ours := halitePillar(t, tree)

			for _, e := range ours.Entries() {
				k := value.KeyString(e.Key)
				got := shape(jsonShape(e.Val))
				want, ok := theirs[k]
				if !ok {
					t.Errorf("halite's pillar has %q = %s; Salt's does not have the key", k, got)
					continue
				}
				if w := shape(want); got != w {
					t.Errorf("pillar %q:\nhalite: %s\nsalt:   %s", k, got, w)
				}
			}
			for k := range theirs {
				if _, ok := ours.Get(k); !ok {
					t.Errorf("Salt's pillar has %q and halite's does not", k)
				}
			}
		})
	}
}

// TestLowstateMatchesSalt is the gate. For every tree in the corpus, the
// two implementations must produce the same chunks, with the same
// arguments, in the same order.
func TestLowstateMatchesSalt(t *testing.T) {
	saltcall := saltCall(t)

	version := saltVersion(t, saltcall)
	t.Logf("differential against Salt %s", version)
	warnAboutHardwareGrains(t, saltcall)

	recorded := map[string]string{}
	for _, d := range deviations {
		if d.salt != "" && !strings.HasPrefix(version, d.salt) {
			continue
		}
		recorded[d.tree+" "+d.chunk] = d.reason
	}
	seen := map[string]bool{}

	for _, tree := range trees(t) {
		name := tree.name
		t.Run(name, func(t *testing.T) {
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
			ordered := strings.Join(theirOrder, "\n") == strings.Join(ourOrder, "\n")
			if !ordered {
				key := name + " ordering"
				seen[key] = true
				if _, ok := recorded[key]; !ok {
					t.Errorf("the low state differs:\n%s", describeOrderDifference(ourOrder, theirOrder))
				}
			}

			// The arguments of the chunks both sides do have are still
			// worth comparing. Stopping at the sequence would let one
			// differing chunk hide fifty others, which on a real tree is
			// most of the answer.
			byIdentity := map[string]int{}
			for i, c := range theirs {
				byIdentity[saltIdentity(c)] = i
			}

			for _, c := range ours {
				id := haliteIdentity(c)
				theirIndex, both := byIdentity[id]
				if !both {
					continue
				}
				ourArgs := haliteArguments(c)
				theirArgs := saltArguments(theirs[theirIndex])
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

// describeOrderDifference reports what differs between two chunk
// sequences rather than printing both. A real tree is fifty chunks and
// two full lists side by side are not a diagnostic, they are homework.
func describeOrderDifference(ours, theirs []string) string {
	inTheirs := map[string]bool{}
	for _, c := range theirs {
		inTheirs[c] = true
	}
	inOurs := map[string]bool{}
	for _, c := range ours {
		inOurs[c] = true
	}

	var b strings.Builder
	for _, c := range ours {
		if !inTheirs[c] {
			fmt.Fprintf(&b, "  only halite has:  %s\n", c)
		}
	}
	for _, c := range theirs {
		if !inOurs[c] {
			fmt.Fprintf(&b, "  only salt has:    %s\n", c)
		}
	}
	if b.Len() > 0 {
		fmt.Fprintf(&b, "  (halite %d chunks, salt %d)\n", len(ours), len(theirs))
		return b.String()
	}

	// The same chunks in a different order: report the first position
	// they disagree at, which is where the ordering rule differs.
	for i := range ours {
		if i >= len(theirs) {
			break
		}
		if ours[i] != theirs[i] {
			fmt.Fprintf(&b, "  the same %d chunks, ordered differently from position %d:\n", len(ours), i)
			fmt.Fprintf(&b, "    halite: %s\n", ours[i])
			fmt.Fprintf(&b, "    salt:   %s\n", theirs[i])
			return b.String()
		}
	}
	return "  the sequences differ in length only\n"
}

// warnAboutHardwareGrains says so when Salt's hardware grains are
// unreadable, because the difference it causes looks like a defect and
// is not one.
//
// Salt reads the SMBIOS tables by shelling out to dmidecode, which needs
// /dev/mem and therefore root. Unprivileged it does not fail: it returns
// the error text as the grain's value, so `productname` comes back as
// "Can't read memory from /dev/mem" and a tree branching on the hardware
// takes the wrong branch. halite reads the same values through kenv on a
// BSD and /sys/class/dmi on Linux, neither of which needs privilege, so
// it gets the right answer and the two disagree.
func warnAboutHardwareGrains(t *testing.T, saltcall string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "minion"), // lexicon:allow — Salt requires this filename
		[]byte("file_client: local\ncachedir: "+filepath.Join(dir, "cache")+"\nroot_dir: "+dir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(saltcall, "--config-dir="+dir, "--local",
		"grains.item", "productname", "--out=json", "--log-level=quiet")
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	out, err := cmd.Output()
	if err != nil {
		return
	}
	if strings.Contains(string(out), "/dev/mem") {
		t.Log("note: this Salt cannot read the SMBIOS tables and is returning the error text " +
			"as the value of its hardware grains, so a tree that branches on `productname` or " +
			"`manufacturer` will differ here and would not as root")
	}
}
