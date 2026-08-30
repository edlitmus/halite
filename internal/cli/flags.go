package cli

import (
	"fmt"
	"sort"
	"strings"
)

// alwaysKnown are accepted by every command whether or not its own
// usage text repeats them.
var alwaysKnown = map[string]bool{
	"help": true, "h": true, "version": true, "v": true,
}

// FlagNames reports the flags a usage text documents.
//
// The help and the check read the same string on purpose: a flag is
// accepted because it is described and described because it is
// accepted, so the two cannot drift into disagreeing. Adding a flag
// without documenting it makes it unusable rather than undiscoverable,
// which is the failure that gets noticed.
func FlagNames(usage string) map[string]bool {
	names := map[string]bool{}
	fields := strings.FieldsFunc(usage, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', '|', '(', ')', '`', '\'', '"', '[', ']', '<', '>':
			return true
		}
		return false
	})
	for _, field := range fields {
		if !strings.HasPrefix(field, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(field, "-"), "=")
		if name = strings.TrimRight(name, ".,:;"); name != "" {
			names[name] = true
		}
	}
	return names
}

// UnknownFlags reports the flags given on the command line that none of
// the usage texts documents, in order.
func UnknownFlags(a *Args, usages ...string) []string {
	known := knownFlags(usages)
	unknown := []string{}
	for name := range a.Flags {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	return unknown
}

func knownFlags(usages []string) map[string]bool {
	known := map[string]bool{}
	for name := range alwaysKnown {
		known[name] = true
	}
	for _, usage := range usages {
		for name := range FlagNames(usage) {
			known[name] = true
		}
	}
	return known
}

// RejectUnknownFlags stops the program when a flag was given that no
// usage text describes.
//
// Accepting one silently is worse than refusing it. `policy test
// --policy other.yaml` reads as a request to evaluate that file and
// instead evaluates the configured one, so a check written that way in
// CI passes while checking nothing at all; the same is true of every
// misspelled flag that changes what a command would have done.
func RejectUnknownFlags(a *Args, program string, usages ...string) {
	unknown := UnknownFlags(a, usages...)
	if len(unknown) == 0 {
		return
	}
	known := knownFlags(usages)

	var said strings.Builder
	for i, name := range unknown {
		if i > 0 {
			said.WriteString("\n")
		}
		fmt.Fprintf(&said, "%s is not a flag of `%s`", dashed(name), program)
		if guess := closest(name, known); guess != "" {
			fmt.Fprintf(&said, "; did you mean %s?", dashed(guess))
		}
	}
	Fatalf("%s\n\nrun `%s --help` for the flags it does take", said.String(), program)
}

// dashed writes a flag the way it would be typed.
func dashed(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// closest finds the documented flag nearest a misspelling, so that a
// typo is answered with what was meant and not only with what was
// wrong. An empty answer means nothing was close enough to suggest.
func closest(name string, known map[string]bool) string {
	limit := 2
	if len(name) <= 3 {
		limit = 1
	}
	best, bestDistance := "", 0
	for candidate := range known {
		distance := editDistance(name, candidate)
		if distance > limit {
			continue
		}
		// Ties break on the name so that the suggestion does not depend
		// on map iteration order.
		if best == "" || distance < bestDistance ||
			(distance == bestDistance && candidate < best) {
			best, bestDistance = candidate, distance
		}
	}
	return best
}

// editDistance is Levenshtein, over bytes: flag names are ASCII.
func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, min(current[j-1]+1, previous[j-1]+cost))
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}
