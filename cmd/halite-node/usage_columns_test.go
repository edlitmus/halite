package main

import (
	"regexp"
	"strings"
	"testing"
)

// The command list in the usage text is a two-column table, and the second
// column is read down the page. Two lines were one space out, and the page
// that copies this text into the handbook carried them (issue 884).
func TestUsageCommandDescriptionsShareAColumn(t *testing.T) {
	line := regexp.MustCompile(`^  halite-node \S.*?\S {2,}(\S)`)
	columnOf := map[int][]string{}
	for _, text := range strings.Split(usage, "\n") {
		m := line.FindStringSubmatchIndex(text)
		if m == nil {
			continue
		}
		column := m[2]
		columnOf[column] = append(columnOf[column], strings.TrimSpace(text[:30]))
	}
	if len(columnOf) == 0 {
		t.Fatal("no command line was found in the usage text; this test has stopped checking anything")
	}
	if len(columnOf) > 1 {
		t.Errorf("the descriptions start in %d different columns: %v", len(columnOf), columnOf)
	}
}
