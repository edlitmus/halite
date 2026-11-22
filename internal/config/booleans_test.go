package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A boolean the loader cannot read is said at load, with the value that
// applies instead. It used to be silent and become the default; for
// `quiesce` that was a node running everything it was asked to stop
// (issue 783).
func TestAnUnreadableBooleanIsWarnedAboutWithTheValueThatApplies(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string // empty: no warning
	}{
		{"a word ParseBool rejects", "metrics: maybe\n", `"metrics" is set to maybe, which is neither true nor false, so it is ignored and true applies`},
		{"an integer, which YAML does not read as a boolean", "failhard: 1\n", `"failhard" is set to 1, which is neither true nor false, so it is ignored and false applies`},
		{"quiesce fails closed", "quiesce: maybe\n", `"quiesce" is set to maybe, which is neither true nor false, so it is ignored and true applies`},
		{"a YAML boolean", "metrics: false\n", ""},
		{"a quoted word ParseBool accepts", "failhard: \"true\"\n", ""},
		{"a non-boolean key is left alone", "hub_tries: 5\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "node.yaml"), c.yaml)

			cfg, err := Load(Node, LoadOptions{Root: root})
			if err != nil {
				t.Fatal(err)
			}

			var got []string
			for _, w := range cfg.Warnings {
				if strings.Contains(w, "neither true nor false") {
					got = append(got, w)
				}
			}
			switch {
			case c.want == "" && len(got) > 0:
				t.Errorf("warned about a readable value: %q", got)
			case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
				t.Errorf("warnings = %q, want one containing %q", got, c.want)
			}
		})
	}
}

// The loader decides a key is boolean by its default being the word
// "true" or "false". A cfg.Bool read of a key whose default is anything
// else -- "1", "", "yes" -- would be a boolean the check never looks at,
// and the defaults audit would not notice, because it compares with
// strconv.ParseBool, which takes "1".
func TestEveryBooleanReadIsCheckedByTheLoader(t *testing.T) {
	defaults := map[string]string{}
	for _, k := range Keys {
		defaults[k.Name] = k.Default
	}
	read := regexp.MustCompile(`\b(?:cfg|config)\.Bool\("([a-z][a-z0-9_]*)"`)

	reads := 0
	var unchecked []string
	for _, dir := range []string{"../../cmd", "../../internal"} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			for _, m := range read.FindAllStringSubmatch(string(src), -1) {
				reads++
				if d := defaults[m[1]]; d != "true" && d != "false" {
					unchecked = append(unchecked, m[1]+" (default "+`"`+d+`"`+", "+filepath.ToSlash(path)+")")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if reads < 30 {
		// The issue counted 36 reads outside tests. Far fewer means the
		// pattern stopped matching them, and the test passes on nothing.
		t.Fatalf("found only %d cfg.Bool reads; the pattern has stopped matching", reads)
	}
	if len(unchecked) > 0 {
		t.Errorf("boolean settings the loader's check cannot see:\n  %s", strings.Join(unchecked, "\n  "))
	}
}
