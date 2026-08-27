package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/value"
)

// NodeData is what the hub remembers about a node between connections:
// the grains it reported and when.
//
// Kept on disk so that targeting works immediately after a hub restart
// rather than only for nodes that have reconnected since. SPEC 8.3
// calls this the node data cache and requires that stale entries be
// annotated rather than hidden -- a job that silently misses half the
// fleet because the hub had forgotten it is worse than one that reports
// the doubt.
type NodeData struct {
	NodeID string `json:"node_id"`
	// Grains is the raw JSON the node sent, kept as it arrived so that
	// a 64-bit integer grain is not rounded through float64 on the way
	// to disk. SPEC 6.4.
	Grains   json.RawMessage `json:"grains,omitempty"`
	Version  string          `json:"version,omitempty"`
	LastSeen time.Time       `json:"last_seen"`
}

// errNoNodeCache marks a hub running without one. Targeting on a node
// ID still works; targeting on a grain matches nothing, which is the
// honest answer when nothing has been cached.
var errNoNodeCache = errors.New("this hub has no node data cache")

// NodeCache is the store of node data.
type NodeCache struct {
	dir string
	// StaleAfter is grain_stale_after: how old cached grains may be
	// before a match on them is annotated.
	StaleAfter time.Duration
	Now        func() time.Time
}

// OpenNodeCache prepares the store.
func OpenNodeCache(dir string) (*NodeCache, error) {
	if dir == "" {
		return nil, fmt.Errorf("the node cache needs a directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the node cache: %w", err)
	}
	// MkdirAll is satisfied by a directory that already exists, whoever
	// owns it. A cache left behind by a hand-run as root is then opened
	// without complaint by a hub running as its service account, which
	// can read nothing in it — and the symptom is every target matching
	// no node, because a node whose cached data cannot be read is
	// skipped. Checked here so it is one message at startup rather than
	// a puzzle at the first job.
	probe := filepath.Join(dir, ".writable")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return nil, fmt.Errorf("the node cache at %s is not usable by this process: %w", dir, err)
	}
	if err := os.Remove(probe); err != nil {
		return nil, fmt.Errorf("the node cache at %s is not usable by this process: %w", dir, err)
	}
	return &NodeCache{dir: dir}, nil
}

func (c *NodeCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *NodeCache) path(nodeID string) (string, error) {
	if c.dir == "" {
		// An empty directory would make filepath.Join produce a
		// relative path and read the working directory, which is how a
		// cache miss turns into reading whatever happens to be there.
		return "", errNoNodeCache
	}
	if err := pki.ValidateNodeID(nodeID); err != nil {
		return "", err
	}
	return filepath.Join(c.dir, nodeID+".json"), nil
}

// Put records what a node reported when it connected.
func (c *NodeCache) Put(data *NodeData) error {
	path, err := c.path(data.NodeID)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the node data for %s: %w", data.NodeID, err)
	}
	return writeAtomic(path, append(raw, '\n'), 0o600)
}

// Get reads one node's data.
func (c *NodeCache) Get(nodeID string) (*NodeData, error) {
	path, err := c.path(nodeID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the node data for %s: %w", nodeID, err)
	}
	var data NodeData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("the node data for %s is not readable: %w", nodeID, err)
	}
	return &data, nil
}

// Delete forgets a node, for one that has been removed from the estate.
func (c *NodeCache) Delete(nodeID string) error {
	path, err := c.path(nodeID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("forgetting %s: %w", nodeID, err)
	}
	return nil
}

// Matchable builds the value a target expression is evaluated against.
//
// A node with no cached grains still matches on its ID, because a glob
// or a list target needs nothing else, and refusing to consider it
// would mean a freshly enrolled node silently missing from every job
// until it happened to connect.
func (c *NodeCache) Matchable(nodeID string) (target.Node, error) {
	n := target.Node{ID: nodeID, Grains: value.NewMap(0), Pillar: value.NewMap(0)}
	data, err := c.Get(nodeID)
	if errors.Is(err, errNoNodeCache) {
		return n, nil
	}
	if err != nil || data == nil {
		return n, err
	}
	if len(data.Grains) > 0 {
		decoded, err := value.DecodeJSON(data.Grains)
		if err != nil {
			return n, fmt.Errorf("the cached grains for %s are not readable: %w", nodeID, err)
		}
		if m, ok := decoded.(*value.Map); ok {
			n.Grains = m
		}
	}
	if c.StaleAfter > 0 && !data.LastSeen.IsZero() && c.now().Sub(data.LastSeen) > c.StaleAfter {
		n.GrainsStale = true
	}
	return n, nil
}

// Known lists every node the hub has data for, in ID order.
func (c *NodeCache) Known() ([]string, error) {
	if c.dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, fmt.Errorf("reading the node cache at %s: %w", c.dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		out = append(out, e.Name()[:len(e.Name())-len(".json")])
	}
	sort.Strings(out)
	return out, nil
}

// writeAtomic writes through a temporary file in the same directory.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return os.Rename(name, path)
}

// NodeData is what the hub recorded about one node.
//
// Exported for a relay, which reports its subordinates' grains upstream
// so that targeting works on a relayed node exactly as on a directly
// connected one.
func (s *Server) NodeData(nodeID string) (*NodeData, error) {
	return s.nodes().Get(nodeID)
}
