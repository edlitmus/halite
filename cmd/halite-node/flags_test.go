package main

import (
	"regexp"
	"strings"
	"testing"
)

// A flag in the usage that nothing parses is the same defect as a
// setting nothing reads, on the surface an operator meets first. So is
// one that is parsed and never documented: it works, nobody knows, and
// it changes without anyone noticing.
//
// Exceptions are listed with the reason. The check is deliberately
// strict — it looks for the flag name beside the accessor — and an
// exception with a reason beats a looser rule that lets a real one
// through.
var flagExceptions = map[string]string{
	"--file-root":   "read through repeatedFlag, which takes the name as an argument",
	"--pillar-root": "read through repeatedFlag, which takes the name as an argument",
	"--local":       "consumed by the argument parser, which knows the no-value flags",
}

func TestEveryFlagIsDocumentedAndParsed(t *testing.T) {
	documented := documentedFlags(usage)
	parsed := parsedFlags(t)

	if len(documented) == 0 || len(parsed) == 0 {
		t.Fatal("no flags were found; this test has stopped checking anything")
	}
	for f := range documented {
		if parsed[f] {
			if why, ok := flagExceptions[f]; ok {
				t.Errorf("%s is parsed directly now and is still excepted (%q); remove its row", f, why)
			}
			continue
		}
		if _, ok := flagExceptions[f]; ok {
			continue
		}
		t.Errorf("%s is in the usage and nothing parses it", f)
	}
	for f := range parsed {
		if !documented[f] {
			t.Errorf("%s is parsed and is in no usage text; an operator cannot find it", f)
		}
	}
}

// documentedFlags reads every backtick-quoted chunk, so a usage built by
// concatenation is read whole.
func documentedFlags(text string) map[string]bool {
	out := map[string]bool{}
	for _, f := range regexp.MustCompile(`(?m)^\s+(--[a-z-]+)`).FindAllStringSubmatch(text, -1) {
		out[f[1]] = true
	}
	return out
}

func parsedFlags(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, f := range regexp.MustCompile(`args\.(?:Flag|Bool)\("([a-z-]+)"`).
		FindAllStringSubmatch(sourceOfThisPackage(t), -1) {
		out["--"+f[1]] = true
	}
	return out
}

func sourceOfThisPackage(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, name := range goFilesHere(t) {
		b.WriteString(readFile(t, name))
	}
	return b.String()
}
