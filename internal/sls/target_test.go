package sls

import (
	"testing"

	"github.com/edlitmus/halite/internal/yamlite"
)

func parseTop(t *testing.T, src string) any {
	t.Helper()
	tree, err := yamlite.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

var web1 = map[string]any{
	"id":        "web1.example.com",
	"host":      "web1.example.com",
	"os_family": "FreeBSD",
	"osrelease": "14.1-RELEASE",
	"role":      "web",
	"roles":     []any{"web", "cache"},
}

func TestTargetPatterns(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"everything", "*", true},
		{"id glob", "web*", true},
		{"id glob miss", "db*", false},
		{"grain", "os_family:FreeBSD", true},
		{"grain glob", "osrelease:14.*", true},
		{"grain miss", "os_family:Debian", false},
		{"absent grain", "datacenter:*", false},
		{"list grain", "roles:cache", true},
		{"list grain miss", "roles:db", false},
		{"salt grain matcher", "G@os_family:FreeBSD", true},
		{"salt list matcher", "L@web1.example.com,db1.example.com", true},
		{"salt list miss", "L@db1.example.com,db2.example.com", false},
		{"regex on id", `E@^web[0-9]+\.`, true},
		{"regex on id miss", `E@^db[0-9]+\.`, false},
		{"regex on grain", `P@osrelease:^14\.`, true},
		{"regex on grain miss", `P@osrelease:^13\.`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchTarget(tc.target, web1)
			if err != nil {
				t.Fatalf("%q: %v", tc.target, err)
			}
			if got != tc.want {
				t.Fatalf("%q: want %v", tc.target, got)
			}
		})
	}
}

func TestCompoundTargets(t *testing.T) {
	cases := []struct {
		name   string
		target string
		want   bool
	}{
		{"and", "web* and os_family:FreeBSD", true},
		{"and with a miss", "web* and os_family:Debian", false},
		{"or", "db* or web*", true},
		{"or with both missing", "db* or cache*", false},
		{"not", "not db*", true},
		{"not the host itself", "not web*", false},
		{"exclude one host", "web* and not L@web1.example.com", false},
		{"exclude another host", "web* and not L@web9.example.com", true},
		{"parentheses group the or", "(db* or web*) and os_family:FreeBSD", true},
		{"parentheses change the answer", "db* or web* and os_family:Debian", false},
		{"not applies to the group", "not (db* or cache*)", true},
		{"case insensitive operators", "web* AND NOT db*", true},
		{"tight parentheses", "not(db*)", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchTarget(tc.target, web1)
			if err != nil {
				t.Fatalf("%q: %v", tc.target, err)
			}
			if got != tc.want {
				t.Fatalf("%q: want %v, got %v", tc.target, tc.want, got)
			}
		})
	}
}

func TestMalformedTargetsAreReported(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"empty", ""},
		{"dangling and", "web* and"},
		{"dangling not", "not"},
		{"unbalanced open", "(web* and db*"},
		{"unbalanced close", "web*)"},
		{"two patterns", "web* db*"},
		{"unimplemented matcher", "I@role:web"},
		{"bad regex", "E@^web[0-9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := MatchTarget(tc.target, web1); err == nil {
				t.Fatalf("%q should be reported as malformed", tc.target)
			}
			if TargetMatch(tc.target, web1) {
				t.Fatalf("%q must not select a host", tc.target)
			}
		})
	}
}

func TestHostIdFallsBackToHostname(t *testing.T) {
	grains := map[string]any{"host": "db1.example.com"}
	if !TargetMatch("db*", grains) {
		t.Fatal("without an enrolled id, a bare pattern globs the hostname")
	}
}

func TestTopFileReportsABadTarget(t *testing.T) {
	tree := parseTop(t, "base:\n  'I@role:web':\n    - web\n")
	if _, err := MatchTop(tree, web1); err == nil {
		t.Fatal("a top file with an unimplemented matcher should fail, not select nothing")
	}
}

func TestTopFileTakesCompoundTargets(t *testing.T) {
	tree := parseTop(t, "base:\n  'web* and not db*':\n    - web\n  'db*':\n    - db\n")
	names, err := MatchTop(tree, web1)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "web" {
		t.Fatalf("want [web], got %v", names)
	}
}
