// Package specaudit holds SPEC.md and docs/DIVERGENCE.md to each other and
// to what a build actually ships.
//
// A gap that is not written down is a gap that gets rediscovered, and a
// gap that is written down and then quietly filled leaves a document that
// lies. Both are test failures here rather than documentation problems.
package specaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/hub"
)

const (
	specFile   = "SPEC.md"
	ledgerFile = "docs/DIVERGENCE.md"
)

// repoFile reads a file relative to the repository root.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

// ---- reading SPEC.md ----

// section returns the body of a numbered subsection, such as "15.2".
func section(t *testing.T, spec, number string) string {
	t.Helper()
	head := "### " + number + " "
	i := strings.Index(spec, head)
	if i < 0 {
		t.Fatalf("SPEC.md has no section %s; the audit is reading a spec it was not written for", number)
	}
	rest := spec[i+len(head):]
	if j := strings.Index(rest, "\n### "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

var backticked = regexp.MustCompile("`([a-z0-9_]+)`")

// namesIn pulls the backticked identifiers out of a fragment, in order and
// without repeats.
func namesIn(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range backticked.FindAllStringSubmatch(text, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// specExecModules is every execution module the spec names, across the core
// list, the platform table, and the language and runtime list.
//
// Only the parts of each section that are a list of module names are read.
// The prose that follows section 15.2's list enumerates *functions* in the
// same backticked style, so reading past it would produce nonsense entries
// such as a module named "purge".
func specExecModules(t *testing.T, spec string) map[string]string {
	t.Helper()
	out := map[string]string{}

	core := section(t, spec, "15.2")
	if i := strings.Index(core, "Notes on the ones"); i > 0 {
		core = core[:i]
	} else {
		t.Fatal("SPEC 15.2 no longer separates its module list from its notes; the audit cannot tell them apart")
	}
	for _, n := range namesIn(core) {
		out[n] = "core"
	}

	for _, row := range tableRows(section(t, spec, "15.3")) {
		if len(row) < 2 {
			continue
		}
		for _, n := range namesIn(row[1]) {
			out[n] = "platform: " + row[0]
		}
	}

	lang := section(t, spec, "15.4")
	if i := strings.Index(lang, "They shell out"); i > 0 {
		lang = lang[:i]
	}
	for _, n := range namesIn(lang) {
		out[n] = "language runtime"
	}
	return out
}

// specStateModules is every state module section 15.5 names.
func specStateModules(t *testing.T, spec string) map[string]string {
	t.Helper()
	body := section(t, spec, "15.5")
	if i := strings.Index(body, "`file.accumulated`"); i > 0 {
		body = body[:i]
	}
	out := map[string]string{}
	for _, n := range namesIn(body) {
		out[n] = "core state"
	}
	return out
}

// tableRows splits the pipe-delimited rows of a markdown table, skipping
// the header and the separator.
func tableRows(text string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		if strings.Contains(line, "---") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, cells)
	}
	if len(rows) > 0 {
		rows = rows[1:] // the header
	}
	return rows
}

// ---- reading the ledger ----

// ledgerEntry is one row of a module table in docs/DIVERGENCE.md.
type ledgerEntry struct {
	Module      string
	Implemented bool
	Functions   int
	Line        int
}

var ledgerRow = regexp.MustCompile("^\\| `([a-z0-9_]+)` \\| (implemented|not implemented) \\| ([0-9]+) \\|")

// ledgerEntries reads the module rows under one heading of the ledger.
// The heading is what separates the execution table from the state table:
// a module such as `file` has a row in both, and matching the wrong one
// compares a state module's function count against an execution module's.
func ledgerEntries(t *testing.T, doc, heading string) map[string]ledgerEntry {
	t.Helper()
	lines := strings.Split(doc, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), heading) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s has no heading %q; the audit is reading a document it was not written for", ledgerFile, heading)
	}

	out := map[string]ledgerEntry{}
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "### ") || strings.HasPrefix(line, "## ") {
			break
		}
		m := ledgerRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[3])
		if err != nil {
			t.Fatalf("%s:%d: function count %q is not a number", ledgerFile, i+1, m[3])
		}
		if prev, dup := out[m[1]]; dup {
			t.Errorf("%s:%d: `%s` has a second row under %s; the first is at line %d",
				ledgerFile, i+1, m[1], heading, prev.Line)
		}
		out[m[1]] = ledgerEntry{Module: m[1], Implemented: m[2] == "implemented", Functions: n, Line: i + 1}
	}
	if len(out) == 0 {
		t.Fatalf("%s has no module rows under %s in the expected shape", ledgerFile, heading)
	}
	return out
}

const (
	execHeading  = "### 2.1 Core execution modules"
	stateHeading = "### 2.2 Core state modules"
	langHeading  = "### 2.4 Language and runtime modules"
)

// ledgerNames is the set of module names mentioned anywhere in the ledger,
// which is what a bulk gap listing such as the platform table produces.
func ledgerNames(doc string) map[string]bool {
	out := map[string]bool{}
	for _, n := range namesIn(doc) {
		out[n] = true
	}
	return out
}

// ---- what the build actually ships ----

type inventory struct {
	exec  map[string][]string
	state map[string][]string
}

func shipped() inventory {
	r := builtin.New()
	inv := inventory{exec: map[string][]string{}, state: map[string][]string{}}
	for _, n := range r.Exec.Names() {
		m, f, _ := strings.Cut(n, ".")
		inv.exec[m] = append(inv.exec[m], f)
	}
	for _, n := range r.States.Names() {
		m, f, _ := strings.Cut(n, ".")
		inv.state[m] = append(inv.state[m], f)
	}
	return inv
}

// specAlias maps a spec module name to the name the build registers it
// under, where the two differ for a stated reason.
var specAlias = map[string]string{
	// SPEC 15.2 names the execution module ssh_auth; the build registers
	// it as ssh.auth_keys, matching Salt, where ssh_auth is the state and
	// ssh is the execution module.
	"ssh_auth": "ssh",
}

func resolve(inv map[string][]string, name string) ([]string, bool) {
	if fns, ok := inv[name]; ok {
		return fns, true
	}
	if alias, ok := specAlias[name]; ok {
		fns, ok := inv[alias]
		return fns, ok
	}
	return nil, false
}

// ---- the audit ----

// Every module SPEC.md names is either shipped or written down as a gap.
// Neither half is optional: an unshipped module that nobody recorded is a
// surprise waiting for whoever plans the next phase.
func TestEverySpecModuleIsShippedOrRecordedAsAGap(t *testing.T) {
	spec := repoFile(t, specFile)
	doc := repoFile(t, ledgerFile)
	mentioned := ledgerNames(doc)
	inv := shipped()

	check := func(kind string, want map[string]string, have map[string][]string) {
		var unrecorded []string
		for name, tier := range want {
			if _, ok := resolve(have, name); ok {
				continue
			}
			if mentioned[name] {
				continue
			}
			unrecorded = append(unrecorded, fmt.Sprintf("%s (%s)", name, tier))
		}
		sort.Strings(unrecorded)
		if len(unrecorded) > 0 {
			t.Errorf("%d %s module(s) in SPEC.md are neither shipped nor recorded in %s:\n  %s",
				len(unrecorded), kind, ledgerFile, strings.Join(unrecorded, "\n  "))
		}
	}
	check("execution", specExecModules(t, spec), inv.exec)
	check("state", specStateModules(t, spec), inv.state)
}

// A gap that has since been filled leaves a document that lies, which is
// worse than no document, because it is trusted.
func TestNoLedgerRowClaimsAGapThatIsFilled(t *testing.T) {
	doc := repoFile(t, ledgerFile)
	inv := shipped()

	for _, table := range []struct {
		heading string
		kind    string
		have    map[string][]string
	}{
		{execHeading, "execution", inv.exec},
		{langHeading, "language runtime", inv.exec},
		{stateHeading, "state", inv.state},
	} {
		for _, e := range ledgerEntries(t, doc, table.heading) {
			_, ships := resolve(table.have, e.Module)
			switch {
			case e.Implemented && !ships:
				t.Errorf("%s:%d: `%s` is recorded as an implemented %s module but the build does not register it",
					ledgerFile, e.Line, e.Module, table.kind)
			case !e.Implemented && ships:
				t.Errorf("%s:%d: `%s` is recorded as a %s module gap but the build registers it; the ledger is stale",
					ledgerFile, e.Line, e.Module, table.kind)
			}
		}
	}
}

// The function counts are the part most likely to drift, because adding a
// function to an existing module feels like it changes nothing.
func TestLedgerFunctionCountsMatchTheBuild(t *testing.T) {
	doc := repoFile(t, ledgerFile)
	inv := shipped()

	for _, table := range []struct {
		heading string
		have    map[string][]string
	}{
		{execHeading, inv.exec},
		{langHeading, inv.exec},
		{stateHeading, inv.state},
	} {
		for _, e := range ledgerEntries(t, doc, table.heading) {
			fns, ok := resolve(table.have, e.Module)
			if !ok {
				if e.Functions != 0 {
					t.Errorf("%s:%d: `%s` is not registered but the ledger claims %d function(s)",
						ledgerFile, e.Line, e.Module, e.Functions)
				}
				continue
			}
			if len(fns) != e.Functions {
				sort.Strings(fns)
				t.Errorf("%s:%d: `%s` ships %d function(s) but the ledger says %d: %s",
					ledgerFile, e.Line, e.Module, len(fns), e.Functions, strings.Join(fns, " "))
			}
		}
	}
}

// The ledger opens with the totals, and a reader who checks nothing else
// checks those.
func TestLedgerTotalsMatchTheBuild(t *testing.T) {
	doc := repoFile(t, ledgerFile)
	inv := shipped()

	execFns := 0
	for _, fns := range inv.exec {
		execFns += len(fns)
	}
	stateFns := 0
	for _, fns := range inv.state {
		stateFns += len(fns)
	}
	want := fmt.Sprintf("**%d execution modules / %d functions** and **%d state\nmodules / %d functions**",
		len(inv.exec), execFns, len(inv.state), stateFns)
	if !strings.Contains(doc, want) {
		t.Errorf("%s does not state the shipped totals; it should read:\n%s", ledgerFile, want)
	}
}

// The README states the same totals, and it is what a reader sees first.
func TestReadmeTotalsMatchTheBuild(t *testing.T) {
	readme := repoFile(t, "README.md")
	inv := shipped()
	want := fmt.Sprintf("ships %d execution modules and %d state modules", len(inv.exec), len(inv.state))
	if !strings.Contains(readme, want) {
		t.Errorf("README.md does not state the shipped totals; it should contain %q", want)
	}
}

// Every module the build ships is accounted for in the ledger, so that a
// module added without a row cannot hide.
func TestEveryShippedModuleAppearsInTheLedger(t *testing.T) {
	doc := repoFile(t, ledgerFile)
	mentioned := ledgerNames(doc)
	inv := shipped()

	// A module registered under a different name than the spec uses is
	// accounted for by whichever of the two the ledger names.
	underEitherName := func(name string) bool {
		if mentioned[name] {
			return true
		}
		for specName, buildName := range specAlias {
			if buildName == name && mentioned[specName] {
				return true
			}
		}
		return false
	}

	var missing []string
	for name := range inv.exec {
		if !underEitherName(name) {
			missing = append(missing, "exec "+name)
		}
	}
	for name := range inv.state {
		if !underEitherName(name) {
			missing = append(missing, "state "+name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d shipped module(s) have no entry in %s:\n  %s",
			len(missing), ledgerFile, strings.Join(missing, "\n  "))
	}
}

// The ledger cites specific sections of SPEC.md. A citation that no longer
// resolves sends the reader somewhere else entirely.
func TestLedgerCitationsResolve(t *testing.T) {
	spec := repoFile(t, specFile)
	doc := repoFile(t, ledgerFile)

	cite := regexp.MustCompile(`SPEC (?:section )?(\d+)\.(\d+)`)
	seen := map[string]bool{}
	for _, m := range cite.FindAllStringSubmatch(doc, -1) {
		ref := m[1] + "." + m[2]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if !strings.Contains(spec, "### "+ref+" ") {
			t.Errorf("%s cites SPEC %s, which SPEC.md does not have", ledgerFile, ref)
		}
	}
	if len(seen) == 0 {
		t.Errorf("%s cites no SPEC section; the citations are how a reader checks it", ledgerFile)
	}
}

// ---- the runner inventory ----

// runnerRowsExempt names the rows of SPEC 19.2 whose second cell is
// prose rather than a function list, so the audit does not read words
// out of a sentence and call them runners.
//
// Named rather than inferred: a new prose row has to be added here on
// purpose, which is the point. `virt` and its neighbours are the
// dropped-or-bridged row, and the notification row describes transports
// instead of listing functions.
var runnerRowsExempt = map[string]bool{
	"virt": true,
	"smtp": true,
}

// Every runner SPEC 19.2 names is declared by the build: either
// implemented, or registered with the phase it arrives in.
//
// The second half matters as much as the first. A name left out of the
// registry makes "orchestration is not written yet" and "you have
// mistyped state.orchestrate" the same message at the terminal, and an
// operator cannot tell which.
func TestEverySpecRunnerIsDeclared(t *testing.T) {
	spec := repoFile(t, specFile)
	reg := hub.NewRunners()

	declared := 0
	var missing []string
	for _, row := range tableRows(section(t, spec, "19.2")) {
		if len(row) < 2 {
			continue
		}
		modules := namesIn(row[0])
		if len(modules) == 0 || runnerRowsExempt[modules[0]] {
			continue
		}
		if len(modules) != 1 {
			t.Errorf("the SPEC 19.2 row for %v names several modules and is not exempt; "+
				"the audit cannot tell which functions belong to which", modules)
			continue
		}
		for _, fn := range namesIn(row[1]) {
			name := modules[0] + "." + fn
			declared++
			if !reg.Has(name) {
				missing = append(missing, name)
			}
		}
	}
	if declared < 50 {
		t.Fatalf("read %d runners out of SPEC 19.2; this audit has stopped checking", declared)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d runner(s) in SPEC 19.2 are neither built nor registered as pending:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
	t.Logf("checked %d runners from SPEC 19.2", declared)
}

// A runner registered as pending and since built leaves a registry that
// lies, in the direction that matters: an operator is told to wait for
// something that already works.
func TestNoBuiltRunnerIsStillMarkedPending(t *testing.T) {
	reg := hub.NewRunners()
	for _, name := range reg.Names() {
		when, pending := reg.Pending(name)
		if !pending {
			continue
		}
		if !strings.Contains(when, "phase") && !strings.Contains(when, "SPEC section") {
			t.Errorf("%s is pending on %q, which names neither a phase nor the section "+
				"that would deliver it", name, when)
		}
	}
}
