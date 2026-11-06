package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// The hub sends no run timeout the target would ignore.
//
// The roster's `timeout` is ssh's ConnectTimeout. The hub used to send it
// on as `timeout_seconds`, which the target's OneshotRequest declared as
// "bounds the run on the target" and read nowhere (DIVERGENCE 5.590). A
// field sent and ignored is a promise nobody keeps, and honouring this one
// would stop every agentless run at the roster's 30-second default.
func TestAnAgentlessRequestCarriesNoTimeoutTheTargetIgnores(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hub.yaml"), []byte(
		"state_dir: "+filepath.Join(root, "state")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := &cli.Args{Flags: map[string]string{"root": root}}
	h := openHubForConfig(args)
	t.Cleanup(h.closeAgentlessPillar)

	body, err := sshRequest(h, roster.Target{ID: "web1", Timeout: 45 * time.Second},
		"1", "test.ping", nil, nil, args)
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if v, ok := req["timeout_seconds"]; ok {
		t.Errorf("the request carries timeout_seconds=%v, which the target does not read", v)
	}
}
