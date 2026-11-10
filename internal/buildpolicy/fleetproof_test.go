package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The `debian` leg's proof names every test only it can run.
//
// # The failure this is built against
//
// `contrib/docker/fleet/run.sh` drives `-run TestLive` in a real Debian
// and then requires that each test it claims printed `--- PASS`. That
// check is worth exactly as much as its list: a fourteenth test gated on
// `HALITE_FLEET_LIVE` would run in the container, could skip for any
// reason, and the leg would stay green because nothing named it.
//
// So the list is held to the tree here, in the direction the script
// cannot check for itself. The script catches a test that stopped
// passing; this catches one the script never knew about, and it catches
// it in `make check` rather than in a nightly log.
//
// # Which tests those are
//
// Two gates mean "this container or nowhere":
//
//   - `live(t)` reads `HALITE_FLEET_LIVE`, which `run.sh` alone sets.
//   - `requireDpkg(t)` needs a dpkg, and `fleet.yml`'s `linux` leg does
//     not select the `TestLiveDpkg` family, so the container is the only
//     place in CI one executes (DIVERGENCE 5.633).
//
// Read from the gate rather than from a file name, because a file name is
// a convention and the gate is what decides where a test can run.
func TestTheDebianLegsProofNamesEveryTestOnlyItCanRun(t *testing.T) {
	root := repoRoot(t)
	expect := debianLegExpect(t, root)
	gated := debianOnlyLiveTests(t, root)

	for _, name := range sortedKeys(gated) {
		if expect[name] {
			continue
		}
		t.Errorf("%s is gated on %s, so the fleet image is the only place it runs, and "+
			"contrib/docker/fleet/run.sh does not name it. A test that leg does not "+
			"claim can skip there and leave the leg green; add it to `expect`.",
			name, gated[name])
	}

	declared := liveTestNames(t, root)
	for _, name := range sortedKeys(expect) {
		// A subtest is named as Parent/sub_test_name. Only the parent is
		// a declared function, and whether the subtest still exists is
		// something only the run can answer -- which it does, by not
		// printing its PASS line.
		parent := name
		if i := strings.Index(name, "/"); i >= 0 {
			parent = name[:i]
		}
		if !declared[parent] {
			t.Errorf("contrib/docker/fleet/run.sh expects %q to pass and no TestLive "+
				"function named %s exists. A renamed test would fail the leg nightly; "+
				"this is the same finding a day earlier.", name, parent)
		}
	}

	// The recipe must not pass a test command of its own. run.sh refuses
	// an argument, so a recipe that passed one would fail loudly rather
	// than run a second, unproven way -- but it would fail for everybody
	// at once, in a nightly job, and this says so here instead.
	recipe := fleetcheckRecipe(t, root)
	if strings.Contains(recipe, "go test") {
		t.Errorf("the fleetcheck recipe runs its own `go test`:\n%s\nrun.sh owns the "+
			"command so that the run and the list of tests that must have passed "+
			"cannot drift apart.", recipe)
	}

	t.Logf("%d test(s) can only run in the fleet image; run.sh claims %d name(s)",
		len(gated), len(expect))
}

// debianLegExpect is the set of names run.sh requires to have passed.
func debianLegExpect(t *testing.T, root string) map[string]bool {
	t.Helper()
	path := filepath.Join(root, "contrib", "docker", "fleet", "run.sh")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	m := regexp.MustCompile(`(?s)\nexpect='([^']*)'`).FindSubmatch(body)
	if m == nil {
		t.Fatalf("%s has no `expect='...'` list, so the leg no longer states which "+
			"tests must have passed. If it was restructured, update this audit rather "+
			"than deleting it.", path)
	}
	out := map[string]bool{}
	for _, name := range strings.Fields(string(m[1])) {
		out[name] = true
	}
	if len(out) == 0 {
		t.Fatalf("%s's expect list is empty; its proof is checking nothing", path)
	}
	return out
}

// debianOnlyLiveTests maps each live test that can only run in the fleet
// image to the gate that says so.
func debianOnlyLiveTests(t *testing.T, root string) map[string]string {
	t.Helper()
	dir := filepath.Join(root, "internal", "builtin")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// `\blive\(t\)` and not `live(t)`, so that `pkgLive(t)` -- the
	// HALITE_SYSTEM_LIVE gate the apt tests share with hostname and
	// sysctl, which run on the `linux` leg -- is not mistaken for it.
	gates := []struct {
		call *regexp.Regexp
		name string
	}{
		{regexp.MustCompile(`\blive\(t\)`), "live(t), the HALITE_FLEET_LIVE gate"},
		{regexp.MustCompile(`\brequireDpkg\(t\)`), "requireDpkg(t)"},
	}
	out := map[string]string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for name, fn := range liveTestBodies(string(body)) {
			for _, g := range gates {
				if g.call.MatchString(fn) {
					out[name] = g.name
					break
				}
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no live test under %s is gated on the fleet image, which cannot be "+
			"true while run.sh exists; this audit has stopped reading the tree", dir)
	}
	return out
}

// liveTestBodies splits a test file into its TestLive* function bodies.
//
// A line that is exactly `}` ends a top-level declaration in gofmt'd
// code, and `make check` runs gofmt before this, so there is no need for
// a parser to find where one function stops and the next begins.
func liveTestBodies(src string) map[string]string {
	decl := regexp.MustCompile(`^func (TestLive[A-Za-z0-9_]*)\(`)
	out := map[string]string{}
	var name string
	var body strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if m := decl.FindStringSubmatch(line); m != nil {
			name, body = m[1], strings.Builder{}
			continue
		}
		if name == "" {
			continue
		}
		if line == "}" {
			out[name] = body.String()
			name = ""
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	return out
}

// fleetcheckRecipe is the body of the Makefile's `fleetcheck` target.
func fleetcheckRecipe(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "Makefile")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "fleetcheck:") {
			continue
		}
		var recipe []string
		for _, l := range lines[i+1:] {
			if !strings.HasPrefix(l, "\t") {
				break
			}
			recipe = append(recipe, l)
		}
		if len(recipe) == 0 {
			t.Fatalf("%s's fleetcheck target has no recipe", path)
		}
		return strings.Join(recipe, "\n")
	}
	t.Fatalf("%s has no `fleetcheck:` target; if it was renamed, this audit needs to "+
		"know the new name rather than to stop checking", path)
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
