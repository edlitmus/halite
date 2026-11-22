package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/job"
)

// quiesceSetting reads `quiesce`, and fails closed.
//
// config.Bool returns its default for a value it cannot read, so
// `quiesce: maybe` or `quiesce: 1` used to load without a word and leave
// the node running everything -- which is the one outcome an operator
// reaching for this setting did not want. A value that is not plainly
// true or false is taken as quiesced. The loader warns about it, and
// says "true applies", because internal/config lists quiesce among its
// fail-closed booleans; TestTheLoaderSaysWhatQuiesceDoesWithAnUnreadableValue
// holds the two together. Nothing is lost by erring this way: lifting a
// quiesce set here is a local edit whichever way it was set.
func quiesceSetting(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	v, ok := cfg.Get("quiesce")
	if !ok {
		return false
	}
	return quiesceValue(v)
}

// quiesceValue is the one reading of a quiesce flag, for the
// configuration and for pillar alike, so that `quiesce: maybe` means the
// same thing in both places. Absent and null are off; a boolean is
// itself; a string strconv can read is what it says; anything else is
// on, because the dangerous reading of an unreadable value is "off".
func quiesceValue(v any) bool {
	if v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		if b, err := strconv.ParseBool(t); err == nil {
			return b
		}
	}
	return true
}

// quiesceSource is one thing that quiesces this node, and what it still
// lets through.
type quiesceSource struct {
	// Name says where it came from, in the words the refusal and the log
	// use: "its configuration", "its pillar's quiesce", "its pillar's
	// minion_blackout". // lexicon:allow
	Name string `json:"name"`
	// Allowlist is the functions this source still permits.
	Allowlist []string `json:"allowlist"`
}

// localQuiesce is the node's own configuration as a source, if it
// quiesces.
func localQuiesce(cfg *config.Config) (quiesceSource, bool) {
	if !quiesceSetting(cfg) {
		return quiesceSource{}, false
	}
	return quiesceSource{Name: "its configuration", Allowlist: cfg.StringSlice("quiesce_allowlist")}, true
}

// permits says whether every source lets fun through.
//
// Every source, not any: the allowlist that applies is the intersection
// of the lists of the sources that quiesce. That is how a pillar can set
// quiesce and never lift one -- a pillar's list can only narrow what the
// configuration's allows, never widen it -- and it is what Salt itself
// does with its two sources, pillar and grains, each of which raises the
// violation on its own (`minion.py` at 221495872f, the tip of Salt's // lexicon:allow
// development branch, 2026-11-19).
func permits(sources []quiesceSource, fun string) bool {
	for _, s := range sources {
		if !slices.Contains(s.Allowlist, fun) {
			return false
		}
	}
	return true
}

// effectiveAllowlist is the intersection, for a message to say.
func effectiveAllowlist(sources []quiesceSource) []string {
	if len(sources) == 0 {
		return nil
	}
	var out []string
	for _, fun := range sources[0].Allowlist {
		if permits(sources, fun) && !slices.Contains(out, fun) {
			out = append(out, fun)
		}
	}
	return out
}

func sourceNames(sources []quiesceSource) string {
	names := make([]string, len(sources))
	for i, s := range sources {
		names[i] = s.Name
	}
	return strings.Join(names, " and by ")
}

// quiesceSources is everything that quiesces this node now: its
// configuration, and the last pillar the agent read. Only the agent
// holds a pillar's quiesce (n.pillarQuiesce is nil elsewhere).
func (n *node) quiesceSources() []quiesceSource {
	var sources []quiesceSource
	if local, ok := localQuiesce(n.cfg); ok {
		sources = append(sources, local)
	}
	return append(sources, n.pillarQuiesce.current()...)
}

// quiesceRefusal says whether a quiesced node will run fun, and why not.
//
// The allowlist is matched exactly, as Salt matches
// minion_blackout_whitelist (`func not in whitelist`, minion.py and // lexicon:allow
// utils/schedule.py at v3006.9), because the shim converts one into the
// other and a glob here would quietly widen a converted list.
//
// Salt also lets `saltutil.refresh_pillar` through whatever the list
// says, so that a blackout set in pillar can be lifted. That is not
// copied, and nothing here needs it: when a pillar's quiesce is what
// refuses fun, the pillar is read again here, before the answer, so a
// pillar that has since lifted it lets the job through. A pillar that
// cannot be read again leaves the quiesce standing (DIVERGENCE 5.703).
// A quiesce in the configuration is never re-read for: no pillar can
// lift it, and lifting it is an edit and a restart.
//
// The configuration is read on every call rather than at startup, so
// that a node built without one (a test, agentless mode) is never
// quiesced by accident and the answer cannot drift from the file.
func (n *node) quiesceRefusal(fun string) error {
	sources := n.quiesceSources()
	if permits(sources, fun) {
		return nil
	}
	if !n.pillarRefuses(fun) {
		return quiescedRefusal(sources, fun, nil)
	}
	_, err := n.compilePillarOrErr()
	sources = n.quiesceSources()
	if permits(sources, fun) {
		return nil
	}
	return quiescedRefusal(sources, fun, err)
}

// quiesceHeld is quiesceRefusal without the re-read, for a job that has
// just compiled its pillar and so has nothing fresher to ask for.
func (n *node) quiesceHeld(fun string) error {
	sources := n.quiesceSources()
	if permits(sources, fun) {
		return nil
	}
	return quiescedRefusal(sources, fun, nil)
}

// pillarRefuses says whether a pillar's quiesce is part of the reason fun
// is refused: the configuration on its own would let it through.
func (n *node) pillarRefuses(fun string) bool {
	if len(n.pillarQuiesce.current()) == 0 {
		return false
	}
	local, ok := localQuiesce(n.cfg)
	return !ok || slices.Contains(local.Allowlist, fun)
}

func quiescedRefusal(sources []quiesceSource, fun string, reread error) error {
	allowed := effectiveAllowlist(sources)
	who := sourceNames(sources)
	detail := fmt.Sprintf("this node is quiesced by %s, and nothing is allowlisted by all of them, so it runs no %s", who, fun)
	if len(sources) == 1 {
		detail = fmt.Sprintf("this node is quiesced by %s and its allowlist is empty, so it runs no %s", who, fun)
	}
	if len(allowed) > 0 {
		detail = fmt.Sprintf("this node is quiesced by %s and runs only %s, which %s is not",
			who, strings.Join(allowed, ", "), fun)
	}
	if reread != nil {
		detail += fmt.Sprintf("; the pillar could not be read again to see whether it still says so (%v), "+
			"so the last one this node read stands", reread)
	}
	return &job.Refusal{Reason: job.ReasonQuiesced, Detail: detail}
}
