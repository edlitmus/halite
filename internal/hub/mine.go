package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/transport"
)

// MineEntry is one function's published data for one node.
type MineEntry struct {
	// Data is the return of the mine function, already encoded, for the
	// reason job.Return.Return is: the ordered model does not survive a
	// round trip through the standard encoder.
	Data json.RawMessage `json:"data"`
	// Updated is when the node last published it, which is what makes a
	// stale entry visible rather than indistinguishable from a fresh
	// one.
	Updated time.Time `json:"updated"`
	// AllowTgt restricts which nodes may read this entry, as the node
	// that published it asked. SPEC 19.5.
	//
	// Declared by the publisher rather than by the reader: a node
	// publishing its database credentials decides who may see them, and
	// a policy on the hub is the second gate rather than the only one.
	AllowTgt  string `json:"allow_tgt,omitempty"`
	AllowKind string `json:"allow_tgt_type,omitempty"`
}

// MineData is everything one node has published.
type MineData struct {
	NodeID    string                `json:"node_id"`
	Functions map[string]*MineEntry `json:"functions,omitempty"`
	Updated   time.Time             `json:"updated"`
}

// MineStore keeps what nodes publish.
//
// On the hub rather than passed between nodes: a node asking another
// node directly is a second authorization surface and a second network
// path, and SPEC 5.1 has nodes connect outward only.
type MineStore struct {
	dir string
	Now func() time.Time
}

var errNoMineStore = errors.New("this hub keeps no mine")

// ErrNoMineData is returned for a node that has published nothing.
var ErrNoMineData = errors.New("no mine data for that node")

// OpenMineStore prepares the store.
func OpenMineStore(dir string) (*MineStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("the mine needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the mine: %w", err)
	}
	return &MineStore{dir: dir}, nil
}

// Dir is where the mine lives.
func (m *MineStore) Dir() string { return m.dir }

func (m *MineStore) now() time.Time {
	if m != nil && m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// path refuses a node ID that is not one, so that a name cannot become
// a path.
func (m *MineStore) path(nodeID string) (string, error) {
	if m == nil || m.dir == "" {
		return "", errNoMineStore
	}
	if nodeID == "" || strings.ContainsAny(nodeID, `/\`) || nodeID == "." || nodeID == ".." {
		return "", fmt.Errorf("%q is not a node identifier", nodeID)
	}
	return filepath.Join(m.dir, nodeID+".json"), nil
}

// Get reads one node's published data.
func (m *MineStore) Get(nodeID string) (*MineData, error) {
	path, err := m.path(nodeID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", nodeID, ErrNoMineData)
	}
	if err != nil {
		return nil, err
	}
	var data MineData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("the mine entry for %s is unreadable: %w", nodeID, err)
	}
	return &data, nil
}

// Put replaces what a node has published.
//
// Replaces rather than merges: what a node publishes is the whole of
// what it publishes, and a function removed from `mine_functions`
// should stop being served rather than linger for ever.
func (m *MineStore) Put(data *MineData) error {
	path, err := m.path(data.NodeID)
	if err != nil {
		return err
	}
	now := m.now()
	data.Updated = now
	// Every entry carries its own timestamp, because a node that
	// republishes one function should not make the other look fresh.
	// A replacing publication arrives with none set, and an entry
	// whose age reads as the year zero is a stale answer nobody can
	// spot.
	for _, entry := range data.Functions {
		if entry.Updated.IsZero() {
			entry.Updated = now
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return writeAtomic(path, raw, 0o600)
}

// Update merges one function's data into what a node has published,
// which is what `mine.send` does for a single value.
func (m *MineStore) Update(nodeID string, functions map[string]*MineEntry) error {
	data, err := m.Get(nodeID)
	if errors.Is(err, ErrNoMineData) {
		data = &MineData{NodeID: nodeID, Functions: map[string]*MineEntry{}}
	} else if err != nil {
		return err
	}
	if data.Functions == nil {
		data.Functions = map[string]*MineEntry{}
	}
	for name, entry := range functions {
		entry.Updated = m.now()
		data.Functions[name] = entry
	}
	return m.Put(data)
}

// Delete drops one function, or the whole node when no function is
// named.
func (m *MineStore) Delete(nodeID, function string) error {
	if function == "" {
		path, err := m.path(nodeID)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := m.Get(nodeID)
	if errors.Is(err, ErrNoMineData) {
		return nil
	}
	if err != nil {
		return err
	}
	delete(data.Functions, function)
	return m.Put(data)
}

// Nodes lists every node with published data, in ID order.
func (m *MineStore) Nodes() ([]string, error) {
	if m == nil || m.dir == "" {
		return nil, errNoMineStore
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out, nil
}

// mine is the store, or an empty one that reports it keeps nothing
// rather than silently answering with nothing.
func (s *Server) mine() *MineStore {
	s.mineOnce.Do(func() {
		if s.Mine == nil {
			s.Mine = &MineStore{}
		}
	})
	return s.Mine
}

// minePublish is PUT /v1/mine: a node publishing what it has computed.
//
// The identity is the certificate's. A node may publish only its own
// data, which is the whole reason the mine is worth having: a load
// balancer's state reads the backend list and has to be able to believe
// the backends said it.
func (s *Server) minePublish(w http.ResponseWriter, r *http.Request, nodeID string) {
	var req transport.MineRequest
	if err := transport.ReadJSON(w, r, transport.MaxGrainsPayload, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if s.Mine == nil {
		transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeInternal,
			errNoMineStore)
		return
	}

	functions := map[string]*MineEntry{}
	for name, published := range req.Functions {
		functions[name] = &MineEntry{
			Data:      published.Data,
			AllowTgt:  published.AllowTgt,
			AllowKind: published.AllowTgtType,
		}
	}

	var err error
	if req.Replace {
		err = s.mine().Put(&MineData{NodeID: nodeID, Functions: functions})
	} else {
		err = s.mine().Update(nodeID, functions)
	}
	if err != nil {
		transport.WriteError(w, http.StatusInternalServerError, transport.CodeInternal, err)
		return
	}
	s.info("mine updated", "node_id", nodeID, "functions", len(functions), "replace", req.Replace)
	s.emit("halite/mine/"+nodeID+"/update", nodeID, map[string]any{
		"functions": int64(len(functions)),
	})
	transport.WriteJSON(w, http.StatusOK, transport.MineResponse{NodeID: nodeID})
}

// mineFetch is POST /v1/mine/get: one node reading what others
// published.
//
// This is the peer interface of SPEC 19.5, and it is deny-by-default:
// the calling node is a `node:` principal in the RBAC policy, and the
// mine function it asks for is the function that grant names. Salt
// expresses the same thing in a separate `peer` configuration dialect,
// which is a second place to get authorization wrong.
func (s *Server) mineFetch(w http.ResponseWriter, r *http.Request, nodeID string) {
	var req transport.MineGetRequest
	if err := transport.ReadJSON(w, r, transport.MaxRequestBody, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	if req.Function == "" {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed,
			fmt.Errorf("a mine read names the function it wants"))
		return
	}
	principal := "node:" + nodeID
	decision := s.Policy.Authorize(
		policyRequestFor(principal, req.Function, req.Target, nil, nil, false))
	s.countDecision(decision)
	if !decision.Allowed {
		s.warn("mine read refused by policy",
			"principal", principal, "target", req.Target, "fun", req.Function,
			"reason", decision.Reason)
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			fmt.Errorf("%s", decision.Reason))
		return
	}
	s.info("mine read authorized",
		"principal", principal, "target", req.Target, "fun", req.Function,
		"role", decision.Role, "rule", decision.RuleIndex)

	out, err := s.MineGet(nodeID, req.Target, req.TargetKind, req.Function)
	if err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	transport.WriteJSON(w, http.StatusOK, transport.MineGetResponse{Data: out})
}

// MineGet collects one function's data from every node matching the
// target that permits the reader to see it.
func (s *Server) MineGet(reader, tgt, kind, function string) (map[string]json.RawMessage, error) {
	matcher, err := target.Compile(kindOrGlob(kind), tgt, s.nodegroups())
	if err != nil {
		return nil, err
	}
	nodes, err := s.mine().Nodes()
	if err != nil {
		return nil, err
	}

	out := map[string]json.RawMessage{}
	for _, id := range nodes {
		node, err := s.nodes().Matchable(id)
		if err != nil {
			s.warn("skipping a node whose cached data is unreadable",
				"node_id", id, "error", err.Error())
			continue
		}
		if !matcher.Match(node) {
			continue
		}
		data, err := s.mine().Get(id)
		if err != nil {
			continue
		}
		entry, ok := data.Functions[function]
		if !ok {
			continue
		}
		allowed, err := s.mineAllows(entry, reader)
		if err != nil {
			s.warn("a mine entry's allow_tgt will not compile",
				"node_id", id, "function", function, "error", err.Error())
			continue
		}
		if !allowed {
			continue
		}
		out[id] = entry.Data
	}
	return out, nil
}

// mineAllows applies the publisher's own restriction on who may read.
//
// An empty reader is the hub asking on an operator's behalf, which
// `allow_tgt` does not restrict: it names which *nodes* may read, and
// an operator is already through the policy.
func (s *Server) mineAllows(entry *MineEntry, reader string) (bool, error) {
	if entry.AllowTgt == "" || reader == "" {
		return true, nil
	}
	matcher, err := target.Compile(kindOrGlob(entry.AllowKind), entry.AllowTgt, s.nodegroups())
	if err != nil {
		return false, err
	}
	node, err := s.nodes().Matchable(reader)
	if err != nil {
		return false, err
	}
	return matcher.Match(node), nil
}

// kindOrGlob reads a target kind flag, defaulting to a glob.
func kindOrGlob(flag string) target.Kind {
	kind, ok := target.KindFromFlag(flag)
	if !ok {
		return target.Glob
	}
	return kind
}
