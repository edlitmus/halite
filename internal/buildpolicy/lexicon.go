// Package buildpolicy enforces the parts of the specification that are
// about the source tree rather than about behaviour: the lexicon policy of
// SPEC section 2.3 and the dependency allowlist of section 4.2.
//
// These are tests rather than a separate linter so that `go test ./...`
// fails on a violation, which is what "CI fails the build on a match"
// means in practice.
package buildpolicy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Term is a prohibited word and what to use instead.
type Term struct {
	Prohibited string
	Required   string
}

// Terms is the table from SPEC section 2.3.
var Terms = buildTerms()

func buildTerms() []Term {
	raw := []struct{ prohibited, required string }{
		{"master", "hub, node, primary, replica, source, target"},
		{"slave", "node, replica, target"},
		{"minion", "node"},
		{"whitelist", "allowlist"},
		{"blacklist", "denylist"},
		{"sanity check", "validity check, precondition"},
		{"dummy", "placeholder, stub"},
		{"grandfathered", "legacy-exempt"},
		{"man hours", "person hours"},
	}
	out := make([]Term, 0, len(raw))
	for _, r := range raw {
		out = append(out, Term{Prohibited: r.prohibited, Required: r.required})
	}
	return out
}

// MatchString reports whether the text uses the term as a word.
//
// A word here is what an identifier or a filename makes of one: the term
// separated by punctuation, standing alone, or as a component of a
// CamelCase name. It is permitted only inside a longer lowercase word,
// where it is a different word — `mastered`, `masterless`, `dominion`.
//
// This was a regular expression, `\bmaster\b`, whose comment claimed to
// catch `master_port` and `MasterKey`. It caught the first and neither
// `MasterKey` nor `halite_master`: an underscore is a word character, so
// the boundary the pattern needs is not there, and a capital letter is
// not a boundary at all. RE2 has no lookaround to express "not inside a
// longer lowercase word" in one pass, and a matcher that says what it
// means reads better than the alternation that would.
func (t Term) MatchString(s string) bool {
	term := []rune(strings.ToLower(t.Prohibited))
	text := []rune(s)
	lower := []rune(strings.ToLower(s))

	for i := 0; i+len(term) <= len(lower); i++ {
		if !runesEqual(lower[i:i+len(term)], term) {
			continue
		}
		if startsWord(text, i) && endsWord(text, i+len(term)) {
			return true
		}
	}
	return false
}

func runesEqual(a, b []rune) bool {
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// startsWord reports whether the term at i begins a word: the start of
// the text, after anything that is not a letter or a digit, or at the
// capital of a CamelCase component.
func startsWord(text []rune, i int) bool {
	if i == 0 {
		return true
	}
	prev := text[i-1]
	if !unicode.IsLetter(prev) && !unicode.IsNumber(prev) {
		return true
	}
	return unicode.IsUpper(text[i]) && !unicode.IsUpper(prev)
}

// endsWord reports whether the term ending at i ends a word: the end of
// the text, before anything that is not a letter or a digit, or before
// the capital that begins the next CamelCase component.
func endsWord(text []rune, i int) bool {
	if i == len(text) {
		return true
	}
	next := text[i]
	if !unicode.IsLetter(next) && !unicode.IsNumber(next) {
		return true
	}
	return unicode.IsUpper(next)
}

// Finding is one prohibited term in one place.
type Finding struct {
	File string
	Line int
	Term Term
	Text string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %q is prohibited; use %s\n    %s",
		f.File, f.Line, f.Term.Prohibited, f.Term.Required, strings.TrimSpace(f.Text))
}

// ExemptPaths are the files permitted to name the prohibited terms,
// because their entire purpose is to translate them. The exemption is a
// defined, dated, removable surface, not a hole in the policy.
var ExemptPaths = []string{
	// The specification itself quotes Salt's vocabulary throughout.
	"SPEC.md",
	"SPEC-README.md",
	// The changelog records the history of a project that used to use the
	// old words.
	"CHANGELOG.md",
	// The compatibility shim of SPEC section 28.3 and its test are the
	// translation table.
	"internal/config/shim.go",
	"internal/config/config_test.go",
	// This package is the policy, and names the terms in order to ban
	// them and to prove the scanner still matches.
	"internal/buildpolicy/lexicon.go",
	"internal/buildpolicy/policy_test.go",
	"internal/buildpolicy/lexicon_terms_test.go",
	// Vendored allowlist code is not ours to reword.
	"vendor/",
	".git/",
	// Build output is not source.
	"bin/",
	"dist/",
	// A developer's editor and agent configuration is not the project's
	// vocabulary. It is untracked, ships in nothing, and quotes whatever
	// commands that developer has run — including the old names of files
	// this project has since renamed.
	".claude/",
}

// IsExempt reports whether a repository-relative path is outside the
// policy.
func IsExempt(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, e := range ExemptPaths {
		if strings.HasSuffix(e, "/") {
			if strings.HasPrefix(rel, e) {
				return true
			}
			continue
		}
		if rel == e {
			return true
		}
	}
	return false
}

// scannedExtensions are the file kinds the policy covers: source,
// configuration, documentation, and test fixtures.
var scannedExtensions = map[string]bool{
	".go": true, ".md": true, ".yaml": true, ".yml": true, ".sls": true,
	".json": true, ".toml": true, ".sh": true, ".service": true, ".conf": true,
	"": true, // Makefile, LICENSE, rc.d scripts
}

// Scan walks a tree and reports every prohibited term outside the
// exemptions.
func Scan(root string) ([]Finding, error) {
	var findings []Finding
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			if IsExempt(filepath.ToSlash(rel) + "/") {
				return filepath.SkipDir
			}
			return nil
		}
		if IsExempt(rel) {
			return nil
		}
		if !scannedExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		found, err := scanFile(path, rel)
		if err != nil {
			return err
		}
		findings = append(findings, found...)
		return nil
	})
	return findings, err
}

func scanFile(path, rel string) ([]Finding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// An extensionless file is scanned so that the Makefile and the
	// rc.d scripts are covered, which also means a compiled binary
	// left in the tree is opened. `go build ./cmd/halite-hub` writes
	// one into the working directory, and scanning it produced half a
	// megabyte of machine code quoted back as policy violations. A NUL
	// byte is not source.
	binary, err := looksBinary(f)
	if err != nil {
		return nil, err
	}
	if binary {
		return nil, nil
	}

	var findings []Finding
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if strings.Contains(text, lexiconAllowMarker) {
			continue
		}
		for _, term := range Terms {
			if term.MatchString(text) {
				findings = append(findings, Finding{File: rel, Line: line, Term: term, Text: text})
			}
		}
	}
	return findings, sc.Err()
}

// looksBinary reports whether a file's first block contains a NUL, and
// rewinds either way.
func looksBinary(f *os.File) (bool, error) {
	head := make([]byte, 8192)
	n, err := f.Read(head)
	if err != nil && err != io.EOF {
		return false, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	return bytes.IndexByte(head[:n], 0) >= 0, nil
}

// lexiconAllowMarker lets one line name a prohibited term where quoting
// Salt is unavoidable, such as an error message that tells an operator
// which Salt key they wrote. Using it is a decision a reviewer can see.
//
// The constant is assembled rather than written out so that this line does
// not itself contain the marker and exempt the whole file.
var lexiconAllowMarker = "lexicon" + ":allow"
