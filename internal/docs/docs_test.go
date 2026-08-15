// Package docs holds no code. It holds the checks that keep this
// repository's documentation and examples honest about the code, so that
// "the docs are up to date" is something `make check` decides rather than
// something somebody remembers.
//
// Everything here reads the tree from disk. The alternative — trusting a
// doc because it was edited in the same commit as the feature — is how
// halite ended up shipping a module reference that named states nobody had
// built, and a compatibility checker blind to twenty modules' arguments.
package docs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/compat"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/modules"
	"github.com/edlitmus/halite/internal/orch"
)

// root is the repository, relative to this package.
const root = "../.."

// execModuleDoc is the section documenting the read-only execution
// modules together.
const execModuleDoc = "# Execution modules"

func read(t *testing.T, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}

func TestEveryStateIsInTheModuleReference(t *testing.T) {
	states := read(t, "docs/states.md")
	// A heading often covers a state and its opposite —
	// "### file.append / file.prepend" — so the check is that some heading
	// names the state, not that it has one to itself.
	headings := regexp.MustCompile(`(?m)^#{2,3} .*$`).FindAllString(states, -1)
	documented := func(name string) bool {
		boundary := regexp.MustCompile(regexp.QuoteMeta(name) + `([^a-z_0-9.]|$)`)
		for _, heading := range headings {
			if boundary.MatchString(heading) {
				return true
			}
		}
		return false
	}
	for name := range modules.Registry {
		if !documented(name) {
			t.Errorf("no heading in docs/states.md names %s", name)
		}
	}
	for name := range modules.ExecRegistry {
		if !strings.Contains(states, name) {
			t.Errorf("docs/states.md does not mention the execution module %s", name)
		}
	}
	if !strings.Contains(states, execModuleDoc) {
		t.Errorf("docs/states.md lost its %q section", execModuleDoc)
	}
}

func TestEveryHeadingNamesARealState(t *testing.T) {
	headings := regexp.MustCompile(`(?m)^### ([a-z_0-9]+\.[a-z_]+)`)
	for _, match := range headings.FindAllStringSubmatch(read(t, "docs/states.md"), -1) {
		name := match[1]
		if modules.Registry[name] == nil && name != orch.StepFunction {
			t.Errorf("docs/states.md documents %s, which is not a registered state", name)
		}
	}
}

func TestClaimedCountsMatchTheRegistry(t *testing.T) {
	counts := regexp.MustCompile(`(\d+) state functions`)
	readme := read(t, "README.md")
	if matches := counts.FindAllStringSubmatch(readme, -1); len(matches) == 0 {
		t.Error("the README no longer says how many state functions there are")
	} else {
		for _, match := range matches {
			if match[1] != fmt.Sprint(len(modules.Registry)) {
				t.Errorf("README.md claims %s state functions; the registry has %d",
					match[1], len(modules.Registry))
			}
		}
	}
	// Salt's own counts are in the same sentence, so the claim to check is
	// the one that starts "halite has".
	halite := regexp.MustCompile(`(?s)halite has\s+(\d+) state functions and (\d+) execution modules`)
	match := halite.FindStringSubmatch(read(t, "docs/salt-parity.md"))
	if match == nil {
		t.Fatal("docs/salt-parity.md no longer states how many modules halite has")
	}
	if match[1] != fmt.Sprint(len(modules.Registry)) || match[2] != fmt.Sprint(len(modules.ExecRegistry)) {
		t.Errorf("docs/salt-parity.md claims %s state and %s execution modules; the registry has %d and %d",
			match[1], match[2], len(modules.Registry), len(modules.ExecRegistry))
	}
}

// TestEveryCommandIsDocumented reads the CLI out of its own source. A
// subcommand nobody documented is one nobody can find.
func TestEveryCommandIsDocumented(t *testing.T) {
	main := read(t, "cmd/halite/main.go")
	usage := section(main, "func usage()", "\n}")
	reference := referenceDocs(t)

	commands := regexp.MustCompile(`(?m)^\tcase "([a-z-]+)":`)
	for _, match := range commands.FindAllStringSubmatch(section(main, "switch os.Args[1]", "\n\t}"), -1) {
		name := match[1]
		if name == "-h" || name == "help" || name == "version" {
			continue // the shape of every CLI, not a feature to document
		}
		if !strings.Contains(usage, "halite "+name) {
			t.Errorf("`halite %s` is missing from usage()", name)
		}
		if !strings.Contains(reference, "halite "+name) {
			t.Errorf("`halite %s` is in neither the README nor docs/", name)
		}
	}
}

// TestEveryFlagIsDocumented holds the flags to the same standard. A flag
// that exists only in `-h` output is a flag nobody knows to reach for.
func TestEveryFlagIsDocumented(t *testing.T) {
	prose := referenceDocs(t)
	flags := regexp.MustCompile(`fs\.(?:String|Bool|Int|Duration)\("([a-z-]+)"`)

	entries, err := os.ReadDir(filepath.Join(root, "cmd/halite"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		for _, match := range flags.FindAllStringSubmatch(read(t, "cmd/halite/"+entry.Name()), -1) {
			name := match[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			if !strings.Contains(prose, "-"+name) {
				t.Errorf("-%s is documented nowhere", name)
			}
		}
	}
}

func TestArchitectureListsEveryPackage(t *testing.T) {
	layout := read(t, "docs/architecture.md")
	entries, err := os.ReadDir(filepath.Join(root, "internal"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "docs" {
			continue // this package is the checks, not a part of the tool
		}
		if !strings.Contains(layout, "internal/"+entry.Name()+"/") {
			t.Errorf("docs/architecture.md's layout does not list internal/%s", entry.Name())
		}
	}
}

func TestInternalLinksResolve(t *testing.T) {
	link := regexp.MustCompile(`\]\(([a-zA-Z0-9_./-]+\.md)(#[a-zA-Z0-9-]+)?\)`)
	for _, doc := range markdownFiles(t) {
		dir := filepath.Dir(filepath.Join(root, doc))
		for _, match := range link.FindAllStringSubmatch(read(t, doc), -1) {
			if _, err := os.Stat(filepath.Join(dir, match[1])); err != nil {
				t.Errorf("%s links to %s, which does not exist", doc, match[1])
			}
		}
	}
}

// TestExamplesCompile runs every example through the same checker `halite
// parse` uses. An example that does not compile is worse than no example:
// it is a format somebody will copy.
func TestExamplesCompile(t *testing.T) {
	// The daemons read these; they are not state files and do not compile
	// as one.
	daemonConfigs := map[string]bool{
		"beacons.sls": true, "mine.sls": true, "schedule.sls": true, "reactor.sls": true,
	}
	// Examples guarded on a grain have to be checked under the grains they
	// are for, or the guard hides them from the check.
	grainSets := []map[string]any{
		{"id": "web1", "host": "web1", "os": "FreeBSD", "os_family": "FreeBSD", "kernel": "FreeBSD"},
		{"id": "web1", "host": "web1", "os": "Debian", "os_family": "Debian", "kernel": "Linux"},
		{"id": "web1", "host": "web1", "os": "Windows", "os_family": "Windows", "kernel": "Windows"},
	}

	var files []string
	for _, dir := range []string{"examples", "examples/orch"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".sls") && !daemonConfigs[entry.Name()] {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
	}
	if len(files) == 0 {
		t.Fatal("no examples found: this check would pass on an empty directory")
	}

	for _, file := range files {
		for _, grains := range grainSets {
			scanner := &compat.Scanner{Grains: grains}
			report := scanner.ScanFile(filepath.Join(root, file), filepath.Dir(filepath.Join(root, file)),
				compat.KindState)
			for _, finding := range report.Findings {
				if finding.Severity == compat.SevError {
					t.Errorf("%s (os_family=%v): %s: %s",
						file, grains["os_family"], finding.Code, finding.Message)
				}
			}
		}
	}
}

// TestStateFunctionsAreWrittenAsMappingKeys catches the one YAML mistake
// that reads correctly and does not parse: a state function on its own
// line without the colon that makes it a key.
func TestStateFunctionsAreWrittenAsMappingKeys(t *testing.T) {
	bare := regexp.MustCompile(`(?m)^[ \t]+([a-z_0-9]+\.[a-z_]+)$`)
	for _, doc := range append(markdownFiles(t), exampleFiles(t)...) {
		for _, match := range bare.FindAllStringSubmatch(read(t, doc), -1) {
			name := match[1]
			if modules.Registry[name] == nil && name != orch.StepFunction {
				continue // a file name or a path, not a state
			}
			t.Errorf("%s writes `%s` with no colon: that is a state function, "+
				"and the parser needs it to be a mapping key", doc, name)
		}
	}
}

// section returns the text between a starting marker and the first
// occurrence of an ending one, for reading a function out of its source.
func section(body, start, end string) string {
	from := strings.Index(body, start)
	if from < 0 {
		return ""
	}
	rest := body[from:]
	to := strings.Index(rest, end)
	if to < 0 {
		return rest
	}
	return rest[:to]
}

func markdownFiles(t *testing.T) []string {
	t.Helper()
	files := []string{"README.md", "CHANGELOG.md", "examples/README.md"}
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".md") {
			files = append(files, "docs/"+entry.Name())
		}
	}
	return files
}

func exampleFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "examples"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".sls") {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// referenceDocs is the documentation a reader is sent to: the README and
// docs/. The changelog is deliberately not part of it — a flag mentioned
// only in a release note is a flag nobody can look up.
func referenceDocs(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, doc := range markdownFiles(t) {
		if doc == "CHANGELOG.md" {
			continue
		}
		b.WriteString(read(t, doc))
	}
	return b.String()
}

// TestEverySysrcVariableIsDocumented holds the rc.d scripts to the same
// standard as the flags: a knob nobody wrote down is a knob nobody can
// set.
func TestEverySysrcVariableIsDocumented(t *testing.T) {
	service := read(t, "docs/service.md")
	defaults := regexp.MustCompile(`(?m)^: \$\{(halite_[a-z_]+):`)

	for _, script := range []string{"contrib/rc.d/halite_master", "contrib/rc.d/halite_agent"} {
		matches := defaults.FindAllStringSubmatch(read(t, script), -1)
		if len(matches) == 0 {
			t.Fatalf("%s declares no settings: this check would pass on an empty file", script)
		}
		for _, match := range matches {
			if !strings.Contains(service, match[1]) {
				t.Errorf("%s sets %s, which docs/service.md does not document", script, match[1])
			}
		}
	}
}

// TestRcScriptsParse checks the shell scripts with a shell. They are the
// part of halite that no Go test would otherwise touch.
func TestRcScriptsParse(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	for _, script := range []string{"contrib/rc.d/halite_master", "contrib/rc.d/halite_agent"} {
		out, err := exec.Command("/bin/sh", "-n", filepath.Join(root, script)).CombinedOutput()
		if err != nil {
			t.Errorf("%s does not parse: %v\n%s", script, err, out)
		}
		info, err := os.Stat(filepath.Join(root, script))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable; rc.d will not run it", script)
		}
	}
}

// TestSampleConfigsNameRealSettings keeps examples/master.conf and
// agent.conf from drifting into settings the daemons do not have — the
// mistake the config loader refuses to start on.
func TestSampleConfigsNameRealSettings(t *testing.T) {
	fleet := read(t, "cmd/halite/fleet.go")
	setting := regexp.MustCompile(`(?m)^#?\s*([a-z-]+):`)

	for _, sample := range []struct{ file, command string }{
		{"examples/master.conf", "func cmdMaster"},
		{"examples/agent.conf", "func cmdAgent"},
	} {
		body := section(fleet, sample.command, "\n}")
		if body == "" {
			t.Fatalf("cannot find %s in cmd/halite/fleet.go", sample.command)
		}
		for _, match := range setting.FindAllStringSubmatch(read(t, sample.file), -1) {
			name := match[1]
			if !strings.Contains(body, `"`+name+`"`) {
				t.Errorf("%s names %q, which is not a flag of %s", sample.file, name, sample.command)
			}
		}
	}
}

// units are the systemd units, paired with the daemon they start and the
// function in cmd/halite that owns that daemon's flags.
var units = []struct{ file, daemon, command string }{
	{"contrib/systemd/halite-master.service", "master", "func cmdMaster"},
	{"contrib/systemd/halite-agent.service", "agent", "func cmdAgent"},
}

// directives returns a unit file's settings as directive -> values. A
// systemd unit is INI-shaped, and the parts this package checks — one
// ExecStart, the paths it names — do not need more than that.
func directives(t *testing.T, rel string) map[string][]string {
	t.Helper()
	found := map[string][]string{}
	for n, line := range strings.Split(read(t, rel), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			found["section"] = append(found["section"], strings.Trim(line, "[]"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Errorf("%s:%d: not a section, comment, or Key=value: %q", rel, n+1, line)
			continue
		}
		found[key] = append(found[key], value)
	}
	return found
}

// TestUnitsStartTheDaemonTheyClaimTo checks the units against the flags
// the daemons actually have — the same standard the sample configs are
// held to, because a unit naming a flag that went away fails at boot on
// somebody else's machine.
func TestUnitsStartTheDaemonTheyClaimTo(t *testing.T) {
	fleet := read(t, "cmd/halite/fleet.go")

	for _, unit := range units {
		found := directives(t, unit.file)
		for _, required := range []string{"Description", "ExecStart", "WantedBy"} {
			if len(found[required]) == 0 {
				t.Errorf("%s has no %s", unit.file, required)
			}
		}
		if len(found["ExecStart"]) != 1 {
			t.Fatalf("%s has %d ExecStart lines; this check assumes one", unit.file, len(found["ExecStart"]))
		}

		flags := section(fleet, unit.command, "\n}")
		if flags == "" {
			t.Fatalf("cannot find %s in cmd/halite/fleet.go", unit.command)
		}
		fields := strings.Fields(found["ExecStart"][0])
		if len(fields) < 2 || fields[1] != unit.daemon {
			t.Errorf("%s: ExecStart does not run `halite %s`: %q", unit.file, unit.daemon, found["ExecStart"][0])
		}
		for i, field := range fields {
			if !strings.HasPrefix(field, "-") {
				continue
			}
			name := strings.TrimLeft(field, "-")
			if !strings.Contains(flags, `"`+name+`"`) {
				t.Errorf("%s: ExecStart passes -%s, which is not a flag of halite %s", unit.file, name, unit.daemon)
			}
			// A unit that points somewhere other than the path the daemon
			// would have read anyway is a second answer to the same
			// question, and one of them will be forgotten.
			if name == "config" && i+1 < len(fields) {
				if want := config.DefaultPath(unit.daemon, "/etc/halite"); fields[i+1] != want {
					t.Errorf("%s: -config names %s, not the Linux default %s", unit.file, fields[i+1], want)
				}
			}
		}
	}
}

// TestUnitFilesAreVerified hands the units to systemd itself where there
// is one. On a host without systemd the shape checks above are all there
// is, which is worth knowing when reading a green run on FreeBSD.
func TestUnitFilesAreVerified(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("no systemd-analyze on this host")
	}
	for _, unit := range units {
		out, err := exec.Command(analyze, "verify", filepath.Join(root, unit.file)).CombinedOutput()
		if err != nil {
			t.Errorf("%s does not verify: %v\n%s", unit.file, err, out)
		}
	}
}

// TestEveryUnitKnobIsDocumented holds the units to the same standard as
// the sysrc variables: an operator has to be able to look up the account
// a daemon runs as and the files it reads before installing it.
func TestEveryUnitKnobIsDocumented(t *testing.T) {
	service := read(t, "docs/service.md")

	for _, unit := range units {
		if !strings.Contains(service, filepath.Base(unit.file)) {
			t.Errorf("docs/service.md does not name %s", unit.file)
		}
		found := directives(t, unit.file)
		documented := append(append([]string{}, found["EnvironmentFile"]...), found["User"]...)
		documented = append(documented, found["ReadWritePaths"]...)
		if len(documented) == 0 {
			t.Fatalf("%s sets no paths or account: this check would pass on an empty file", unit.file)
		}
		for _, value := range documented {
			// Backticked, because these are paths and an account name:
			// a bare substring search finds "nobody" in a sentence and
			// calls the account documented.
			literal := "`" + strings.TrimPrefix(value, "-") + "`"
			if !strings.Contains(service, literal) {
				t.Errorf("%s uses %q, which docs/service.md does not document", unit.file, value)
			}
		}
	}
}
