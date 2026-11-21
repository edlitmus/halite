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
// true or false is taken as quiesced, and unreadable carries it so that
// startup can say so. Nothing is lost by erring this way: lifting quiesce
// is a local edit whichever way it was set.
func quiesceSetting(cfg *config.Config) (on bool, unreadable string) {
	if cfg == nil {
		return false, ""
	}
	v, ok := cfg.Get("quiesce")
	if !ok || v == nil {
		return false, ""
	}
	switch t := v.(type) {
	case bool:
		return t, ""
	case string:
		if b, err := strconv.ParseBool(t); err == nil {
			return b, ""
		}
	}
	return true, fmt.Sprint(v)
}

// quiesceRefusal says whether a quiesced node will run fun, and why not.
//
// The allowlist is matched exactly, as Salt matches
// minion_blackout_whitelist (`func not in whitelist`, minion.py and // lexicon:allow
// utils/schedule.py at v3006.9), because the shim converts one into the
// other and a glob here would quietly widen a converted list. Salt also
// lets `saltutil.refresh_pillar` through whatever the list says; that is
// not copied, because it exists so that pillar, where Salt's blackout
// lives, can lift it -- and here the setting is the node's own
// configuration, which no job can change. Lifting it is a local edit and
// a restart, which is what taking a machine out of automation means.
//
// Read from the configuration on every call rather than at startup, so
// that a node built without one (a test, agentless mode) is never
// quiesced by accident and the answer cannot drift from the file.
func (n *node) quiesceRefusal(fun string) error {
	if on, _ := quiesceSetting(n.cfg); !on {
		return nil
	}
	allowed := n.cfg.StringSlice("quiesce_allowlist")
	if slices.Contains(allowed, fun) {
		return nil
	}
	detail := fmt.Sprintf("this node is quiesced and its quiesce_allowlist is empty, so it runs no %s", fun)
	if len(allowed) > 0 {
		detail = fmt.Sprintf("this node is quiesced and runs only %s, which %s is not",
			strings.Join(allowed, ", "), fun)
	}
	return &job.Refusal{Reason: job.ReasonQuiesced, Detail: detail}
}
