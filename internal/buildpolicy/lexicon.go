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
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Term is a prohibited word and what to use instead.
type Term struct {
	Prohibited string
	Required   string
	// Pattern matches the term as a whole word, case-insensitively.
	Pattern *regexp.Regexp
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
		// A word boundary keeps `mastered` from matching while still
		// catching `master_port`, `MasterKey`, and `salt-master`.
		pat := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(r.prohibited) + `\b`)
		out = append(out, Term{Prohibited: r.prohibited, Required: r.required, Pattern: pat})
	}
	return out
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
	// Vendored allowlist code is not ours to reword.
	"vendor/",
	".git/",
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
			if term.Pattern.MatchString(text) {
				findings = append(findings, Finding{File: rel, Line: line, Term: term, Text: text})
			}
		}
	}
	return findings, sc.Err()
}

// lexiconAllowMarker lets one line name a prohibited term where quoting
// Salt is unavoidable, such as an error message that tells an operator
// which Salt key they wrote. Using it is a decision a reviewer can see.
//
// The constant is assembled rather than written out so that this line does
// not itself contain the marker and exempt the whole file.
var lexiconAllowMarker = "lexicon" + ":allow"
