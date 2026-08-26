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
