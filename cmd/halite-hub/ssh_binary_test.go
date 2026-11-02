package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/roster"
)

// A file in the tree that is not valid UTF-8 reaches an agentless target
// byte for byte. It went in a JSON string, which cannot hold such a byte,
// and each one arrived as U+FFFD. DIVERGENCE 5.538.
func TestAnAgentlessRunSendsABinaryFileUnaltered(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "salt")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := []byte{0x89, 'P', 'N', 'G', 0xff, 0xfe, 0x00, 0x80}
	text := []byte("plain text, and ünïcode that is valid\n")
	for name, body := range map[string][]byte{"logo.png": binary, "motd": text} {
		if err := os.WriteFile(filepath.Join(tree, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "hub.yaml"), []byte(
		"file_roots:\n  base:\n    - "+tree+"\nstate_dir: "+filepath.Join(root, "state")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := &cli.Args{Flags: map[string]string{"root": root}}
	h := openHubForConfig(args)
	t.Cleanup(h.closeAgentlessPillar)

	body, err := sshRequest(h, roster.Target{ID: "web1"}, "1", "state.apply", nil, nil, args)
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Files       map[string]string `json:"files"`
		FilesBase64 map[string]string `json:"files_b64"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if got := req.Files["motd"]; got != string(text) {
		t.Errorf("the text file was altered: %q", got)
	}
	if _, inText := req.Files["logo.png"]; inText {
		t.Errorf("the binary file was sent as text, which alters it")
	}
	raw, err := base64.StdEncoding.DecodeString(req.FilesBase64["logo.png"])
	if err != nil || !bytes.Equal(raw, binary) {
		t.Errorf("the binary file arrived as % x (%v), want % x", raw, err, binary)
	}
}
