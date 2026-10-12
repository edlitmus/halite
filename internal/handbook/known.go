package handbook

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/beacon"
	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/hub"
)

// The kinds of identifier the index recognises.
const (
	KindCommand   = "Command"
	KindSetting   = "Setting"
	KindFunction  = "Function"
	KindEvent     = "Event"
	KindMetric    = "Metric"
	KindPackage   = "Package"
	KindRequisite = "Requisite"
	KindRunner    = "Runner"
	KindBeacon    = "Beacon"
)

// commandTree is the command words the index knows to look for, nested one
// level where a command has subcommands that people type. It is for the
// index only, so a stale entry costs an index entry for a word nobody types
// and not a wrong sentence; the coverage audits read the dispatch switches.
var commandTree = map[string][]string{
	"halite-node": {"call", "state", "grains", "pillar", "lint", "version", "enroll", "renew", "connect",
		"event send", "doctor", "verify-evidence",
		"state apply", "state highstate", "state sls", "state show_top", "state show_highstate",
		"state show_lowstate", "state show_sls", "state show_states",
		"grains items", "grains item", "grains get", "grains ls", "pillar items", "pillar get"},
	"halite-hub": {"serve", "keys", "run", "runner", "orch", "jobs", "policy", "event", "evidence", "metrics",
		"ssh", "doctor", "extensions", "migrate", "lint", "version",
		"keys list", "keys show", "keys fingerprint", "keys accept", "keys reject", "keys revoke",
		"keys delete", "keys export-crl", "keys token create", "keys token list", "keys token revoke",
		"keys token delete", "keys operator create", "keys signer create",
		"jobs list", "jobs show", "jobs lookup", "jobs missing", "jobs active", "jobs resume", "jobs kill",
		"jobs export", "jobs prune", "policy show", "policy test", "event listen", "event tags",
		"orch run", "orch lint", "orch show", "orch list", "orch resume", "evidence anchors",
		"extensions list", "extensions sync", "extensions run", "extensions verify",
		"extensions key create", "extensions sign"},
	"halite-api": {"serve", "token", "account", "doctor", "version",
		"token list", "token show", "token revoke", "token prune", "account hash", "account list"},
}

// requisiteNames are the dependency words and the options every state takes.
var requisiteNames = []string{
	"require", "require_any", "watch", "watch_any", "onchanges", "onchanges_any", "onfail",
	"onfail_any", "onfail_all", "prereq", "use", "listen",
	"require_in", "watch_in", "onchanges_in", "onfail_in", "prereq_in", "listen_in", "use_in",
	"unless", "onlyif", "creates", "names", "order", "failhard", "retry", "timeout", "check_cmd",
}

// ambiguousBeacons are beacon names that are also ordinary words or module
// names, and would fill the index with references that are not about
// beacons.
var ambiguousBeacons = map[string]bool{"status": true, "service": true, "load": true, "log": true, "ps": true, "sh": true, "pkg": true}

// EventTags runs `halite-hub event tags` and returns the tag patterns it
// prints, the namespace this build fires.
func EventTags(root string) ([]string, error) {
	cmd := exec.Command("go", "run", "./cmd/halite-hub", "event", "tags")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=vendor", "GOPROXY=off")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var tags []string
	for _, line := range strings.Split(out.String(), "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasPrefix(f[0], "halite/") {
			tags = append(tags, f[0])
		}
	}
	return tags, nil
}

var metricRe = regexp.MustCompile("`(halite_[a-z0-9_]+)`")

// KnownIdentifiers collects, from the build itself, the identifiers the
// index recognises when a chapter puts one in a code span.
func KnownIdentifiers(root string) ([]Known, error) {
	var out []Known

	// Commands.
	var cmds []string
	for bin, subs := range commandTree {
		cmds = append(cmds, bin)
		for _, s := range subs {
			cmds = append(cmds, bin+" "+s)
		}
	}
	sort.Strings(cmds)
	out = append(out, Known{Kind: KindCommand, Names: cmds, Prefix: true})

	// Settings.
	seen := map[string]bool{}
	var settings []string
	for _, role := range []config.Role{config.Node, config.Hub, config.API} {
		for _, k := range config.KeysFor(role) {
			if !seen[k.Name] {
				seen[k.Name] = true
				settings = append(settings, k.Name)
			}
		}
	}
	sort.Strings(settings)
	out = append(out, Known{Kind: KindSetting, Names: settings})

	// Functions: execution and state.
	r := builtin.New()
	fseen := map[string]bool{}
	var funcs []string
	for _, n := range append(r.Exec.Names(), r.States.Names()...) {
		if !fseen[n] {
			fseen[n] = true
			funcs = append(funcs, n)
		}
	}
	sort.Strings(funcs)
	out = append(out, Known{Kind: KindFunction, Names: funcs})

	// Runners.
	out = append(out, Known{Kind: KindRunner, Names: hub.NewRunners().Names()})

	// Beacons.
	var beacons []string
	for _, n := range beacon.New().Names() {
		if !ambiguousBeacons[n] {
			beacons = append(beacons, n)
		}
	}
	out = append(out, Known{Kind: KindBeacon, Names: beacons})

	// Requisites and state options.
	out = append(out, Known{Kind: KindRequisite, Names: requisiteNames})

	// Event tags.
	if tags, err := EventTags(root); err == nil {
		out = append(out, Known{Kind: KindEvent, Names: tags})
	}

	// Metrics, from the reference that a test already holds to the code.
	if b, err := os.ReadFile(filepath.Join(root, "docs", "metrics.md")); err == nil {
		mseen := map[string]bool{}
		var metrics []string
		for _, m := range metricRe.FindAllStringSubmatch(string(b), -1) {
			if !mseen[m[1]] {
				mseen[m[1]] = true
				metrics = append(metrics, m[1])
			}
		}
		sort.Strings(metrics)
		out = append(out, Known{Kind: KindMetric, Names: metrics})
	}

	// Source packages.
	if pkgs, err := EveryPackage(root); err == nil {
		out = append(out, Known{Kind: KindPackage, Names: pkgs})
	}
	return out, nil
}
