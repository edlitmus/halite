package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/redact"
)

// The files the hub sent as base64 are written as the bytes they decode to.
// DIVERGENCE 5.538.
func TestAnAgentlessRunWritesABinaryFileUnaltered(t *testing.T) {
	n := &node{secrets: redact.New(), env: "base"}
	binary := []byte{0x89, 'P', 'N', 'G', 0xff, 0xfe, 0x00, 0x80}
	tree, err := applyOneshotContent(n, OneshotRequest{
		Fun:         "state.apply",
		Files:       map[string]string{"motd": "hello\n"},
		FilesBase64: map[string]string{"img/logo.png": base64.StdEncoding.EncodeToString(binary)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tree.remove(n) })
	got, err := os.ReadFile(filepath.Join(tree.dir, "img", "logo.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Errorf("the file on the target is % x, want % x", got, binary)
	}
	if _, err := applyOneshotContent(n, OneshotRequest{
		Fun: "state.apply", FilesBase64: map[string]string{"x": "!!not base64"},
	}); err == nil {
		t.Error("a file that is not base64 was accepted")
	}
}
