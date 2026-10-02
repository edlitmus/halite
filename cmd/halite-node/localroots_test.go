package main

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// fakeHub is the smallest hub a node will talk to: the transport's own
// TLS configuration, a CA that issued both ends, and a pillar endpoint
// that answers `where: hub`. Everything else is refused, which a node
// treats as "this hub serves no tree" and warns about.
//
// It records every path it was asked for. That is the half of the
// assertion that the pillar value alone cannot carry: a node that asked
// the hub and then happened to fall back would print the local value
// too, and "the hub was never asked" is what `--local` means.
type fakeHub struct {
	addr string
	mu   sync.Mutex
	seen []string
}

func (h *fakeHub) requests() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.seen...)
}

func (h *fakeHub) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = nil
}

// enrolledNode writes the key material an enrolled node has -- the
// pinned CA, its certificate, its key -- and starts a hub that trusts
// it. It returns the hub and the directory holding the node's pki.
func enrolledNode(t *testing.T, nodeID string) (*fakeHub, string) {
	t.Helper()
	ca, err := pki.NewCA(pki.ECDSAP256, "test CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	pkiDir := t.TempDir()
	files := pki.Files{Dir: pkiDir}
	nodeKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := pki.NewNodeCSR(nodeKey, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := pki.DecodeCSR(pki.EncodeCSR(csrDER))
	if err != nil {
		t.Fatal(err)
	}
	nodeDER, err := ca.IssueNode(csr, nodeID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteKey(pki.NodeKeyFile, nodeKey); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCert(pki.NodeCertFile, nodeDER); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCert(pki.CACertFile, ca.Cert.Raw); err != nil {
		t.Fatal(err)
	}

	hubKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	hubDER, err := ca.IssueHub(hubKey, []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	hubCert := tls.Certificate{Certificate: [][]byte{hubDER}, PrivateKey: hubKey}

	ln, err := tls.Listen("tcp", "127.0.0.1:0",
		transport.ServerConfig(hubCert, ca.Cert, transport.NewDenylist()))
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHub{addr: ln.Addr().(*net.TCPAddr).String()}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.seen = append(h.seen, r.URL.Path)
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == transport.PathPillar {
			_ = json.NewEncoder(w).Encode(transport.PillarResponse{
				NodeID: nodeID, Env: "base", SLS: []string{"hubdata"},
				Pillar: json.RawMessage(`{"where":"hub"}`),
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(transport.Error{Error: "not served by this test hub"})
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return h, pkiDir
}

func writeFiles(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestRootFlagsImplyLocalOnAnEnrolledNode is the help text's promise
// that `--file-root` and `--pillar-root` imply `--local`, held on the
// one kind of node where it can be false: one with a hub configured and
// a certificate to reach it with.
//
// It was false there. Only `--local` itself was consulted, so `pillar
// items --pillar-root <dir>` printed the hub's pillar and `state
// show_sls x --file-root <dir>` said `x` was not found -- an operator
// testing a tree on a real node was shown the hub's instead, with
// nothing to say so. A node with no hub, which is what every other test
// here is, works from its own roots whatever the flags say, which is how
// the claim went unchecked.
func TestRootFlagsImplyLocalOnAnEnrolledNode(t *testing.T) {
	const nodeID = "probe.example"
	hub, pkiDir := enrolledNode(t, nodeID)

	root := t.TempDir()
	emptyPillar := t.TempDir()
	localPillar := writeFiles(t, t.TempDir(), map[string]string{
		"top.sls":       "base:\n  '*':\n    - localdata\n",
		"localdata.sls": "where: local\n",
	})
	localState := writeFiles(t, t.TempDir(), map[string]string{
		"localonly.sls": "localonly:\n  test.nop: []\n",
	})
	// The configured pillar root is an empty directory, so that a run
	// naming only `--file-root` does not compile whatever pillar the
	// host running the test keeps under /srv.
	cfg := filepath.Join(root, "node.yaml")
	body := "node_id: " + nodeID + "\n" +
		"hub: " + hub.addr + "\n" +
		"pki_dir: " + pkiDir + "\n" +
		"cache_dir: " + filepath.Join(root, "cache") + "\n" +
		"state_dir: " + filepath.Join(root, "var") + "\n" +
		"hub_tries: 1\n" +
		"pillar_roots:\n  base:\n    - " + emptyPillar + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	common := []string{"--root", root, "--config", cfg, "--out", "json", "--reveal"}

	// The control: with no root flag the node asks the hub and prints
	// its pillar. Without this the rest would pass against a hub the
	// node could not reach at all.
	hub.reset()
	got := run(t, append([]string{"pillar", "items"}, common...)...)
	if got.code != 0 || !strings.Contains(got.stdout, `"where":"hub"`) {
		t.Fatalf("control: pillar items on an enrolled node did not use the hub: %+v", got)
	}
	if len(hub.requests()) == 0 {
		t.Fatal("control: the hub was never asked")
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"pillar items --pillar-root",
			[]string{"pillar", "items", "--pillar-root", localPillar}, `"where":"local"`},
		{"call pillar.get --pillar-root",
			[]string{"call", "pillar.get", "where", "--pillar-root", localPillar}, "local"},
		{"state show_sls --file-root",
			[]string{"state", "show_sls", "localonly", "--file-root", localState}, "localonly"},
		{"call state.show_sls --file-root",
			[]string{"call", "state.show_sls", "localonly", "--file-root", localState}, "localonly"},
		{"--local still means local",
			[]string{"pillar", "items", "--local", "--pillar-root", localPillar}, `"where":"local"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hub.reset()
			got := run(t, append(c.args, common...)...)
			if got.code != 0 || !strings.Contains(got.stdout, c.want) {
				t.Errorf("%v: want %q in the output, got %+v", c.args, c.want, got)
			}
			if seen := hub.requests(); len(seen) != 0 {
				t.Errorf("%v: the hub was asked for %v; a root flag implies --local", c.args, seen)
			}
		})
	}

	// `connect` is the exception, and says so. A connected agent is a
	// service, and a root flag left in its unit file has until now meant
	// "the tree to fall back on when the hub serves none"; turning that
	// agent local would change what a running fleet applies. It warns
	// instead, and it still asks the hub.
	t.Run("connect warns and stays on the hub", func(t *testing.T) {
		hub.reset()
		got := run(t, append([]string{"connect", "--file-root", localState,
			"--log-fmt", "console"}, common...)...)
		if !strings.Contains(got.stderr, "do not make a connected agent local") {
			t.Errorf("connect --file-root gave no warning: %+v", got)
		}
		// The subscribe stream reaches the hub even from a local agent,
		// so the request that shows it stayed on the hub's tree and
		// pillar is the pillar probe, which a local agent never makes.
		if seen := hub.requests(); !slices.Contains(seen, transport.PathPillar) {
			t.Errorf("connect --file-root did not attach to the hub's pillar (asked for %v): %+v", seen, got)
		}
	})
}
