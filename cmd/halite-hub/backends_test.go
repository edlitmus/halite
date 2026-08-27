package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The list `serve` validates `fileserver_backend` against and the names
// the backends themselves key off have to be the same list.
//
// They were not: the validator accepted roots, git, and gitfs, while
// s3fs.go enables itself on s3 or s3fs. A hub configured for s3 was
// warned that this build did not serve it, and then started the s3 file
// server on the next line — so the one operator who read their startup
// log carefully was the one misled.
func TestEveryBackendNameTheCodeAcceptsIsAlsoValidated(t *testing.T) {
	validated := acceptedBackendNames(t)

	// What the backends actually enable themselves on.
	enabling := map[string][]string{
		"gitfs.go": nil,
		"s3fs.go":  nil,
	}
	call := regexp.MustCompile(`namesBackend\(h\.cfg, "([a-z0-9]+)"\)`)
	for file := range enabling {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			enabling[file] = append(enabling[file], m[1])
			if !validated[m[1]] {
				t.Errorf("%s enables on %q, which `serve` warns about as unsupported",
					file, m[1])
			}
		}
	}
	for file, names := range enabling {
		if len(names) == 0 {
			t.Errorf("no backend names were read from %s; this check has stopped checking", file)
		}
	}
	if !validated["roots"] {
		t.Error("the default backend is not in the validated list")
	}
}

// acceptedBackendNames reads the case labels of the switch in serve.go
// rather than duplicating them, so the check cannot pass by agreeing
// with a copy of the bug.
func acceptedBackendNames(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	marker := `for _, b := range h.cfg.StringSlice("fileserver_backend") {`
	i := strings.Index(text, marker)
	if i < 0 {
		t.Fatal("the fileserver_backend validation has moved; this check is reading nothing")
	}
	rest := text[i:]
	end := strings.Index(rest, "default:")
	if end < 0 {
		t.Fatal("the fileserver_backend switch has no default branch")
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z0-9]+)"`).FindAllStringSubmatch(rest[:end], -1) {
		if m[1] != "fileserver_backend" {
			out[m[1]] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("no accepted backend names were found")
	}
	return out
}

// SPEC 23.5 names the functions a wildcard must never grant. Each is
// named there because it is a way to run whatever the caller likes, and
// the control is worth nothing if one of them is missing: a role
// deliberately refused `cmd.run` simply asks for whichever one was left
// out and gets the same thing.
//
// `cmd.shell`, `file.write`, and `file.replace` were all missing, so
// `functions: ['*']` granted them. Found by writing the example policy
// and asking `policy test` what it actually decided.
func TestEveryFunctionSpecNamesIsNeverGrantedByAWildcard(t *testing.T) {
	// The list is SPEC 23.5's own, written out rather than derived, so
	// that dropping a declaration in the code cannot also drop it from
	// what this checks against.
	named := []string{
		"cmd.run", "cmd.script", "cmd.shell", "module.run",
		"file.write", "file.replace",
	}
	arbitrary := arbitraryCodeFunctions()
	if len(arbitrary) == 0 {
		t.Fatal("no function declares arbitrary_code; this check has stopped checking")
	}
	for _, fn := range named {
		if !arbitrary[fn] {
			t.Errorf("SPEC 23.5 names %s, but it does not declare arbitrary_code, "+
				"so functions: ['*'] grants it", fn)
		}
	}
}
