package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goFilesHere lists this package's non-test sources, which is where a
// flag is parsed.
func goFilesHere(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			out = append(out, e.Name())
		}
	}
	return out
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
