package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/pillarstore"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// removePillarStoreIfDisabled honours `pillar_cache_disk: false` for what is
// already there. An operator who sets it wants the secrets off the disk, and
// a copy that stops being read but stays is exactly what they were removing.
func (n *node) removePillarStoreIfDisabled() {
	if n.cfg.Bool("pillar_cache_disk", true) {
		return
	}
	dir := n.pillarStoreDir()
	if err := os.RemoveAll(dir); err != nil {
		n.log.Warn("could not remove the last known good pillar", "dir", dir, "error", err.Error())
	}
}

func (n *node) pillarStoreDir() string {
	return filepath.Join(n.cfg.String("state_dir", config.DefaultStateDir), "pillar-cache")
}

// pillarStore is this node's last known good pillar, or nil when
// `pillar_cache_disk: false` says it may not hold one.
func (n *node) pillarStore() *pillarstore.Store {
	if !n.cfg.Bool("pillar_cache_disk", true) {
		return nil
	}
	dir := n.pkiDir
	if dir == "" {
		dir = n.cfg.PathUnderRoot("pki_dir", "pki")
	}
	return &pillarstore.Store{
		Dir:    n.pillarStoreDir(),
		NodeID: n.nodeID,
		KeyDER: func() ([]byte, error) {
			files := pki.Files{Dir: dir}
			key, err := files.ReadKey(pki.NodeKeyFile)
			if err != nil {
				return nil, err
			}
			return pki.EncodeKey(key)
		},
	}
}

// keepPillar records what the hub just sent. A failure is a warning: the run
// in hand has its pillar, and what is lost is only the next outage's copy.
func (n *node) keepPillar(env string, all bool, res *transport.PillarResponse) {
	store := n.pillarStore()
	if store == nil {
		return
	}
	err := store.Save(env, all, pillarstore.Record{
		Pillar: res.Pillar, Secrets: res.Secrets, SavedAt: time.Now(),
	})
	if err != nil {
		n.log.Warn("could not keep the last known good pillar; "+
			"a hub outage will fail state runs that read pillar",
			"env", env, "error", err.Error())
	}
}

// lastKnownGoodPillar answers for a hub that did not answer.
//
// It says so on every use and says how old the copy is, because an operator
// reading a run that succeeded during an outage needs to be able to tell it
// converged on old values.
func (n *node) lastKnownGoodPillar(env string, all bool) (*value.Map, bool) {
	store := n.pillarStore()
	if store == nil {
		return nil, false
	}
	rec, err := store.Load(env, all)
	if err != nil {
		if !errors.Is(err, pillarstore.ErrNone) {
			n.log.Warn("the last known good pillar cannot be used", "env", env, "error", err.Error())
		}
		return nil, false
	}
	m, err := decodeCachedPillar(rec)
	if err != nil {
		n.log.Warn("the last known good pillar cannot be used", "env", env, "error", err.Error())
		return nil, false
	}
	n.seedPillarSecrets(m, rec.Secrets)
	n.log.Warn("the hub cannot be reached; using the last known good pillar",
		"env", env, "saved", rec.SavedAt.Format(time.RFC3339),
		"age", time.Since(rec.SavedAt).Round(time.Second).String())
	return m, true
}

func decodeCachedPillar(rec *pillarstore.Record) (*value.Map, error) {
	decoded, err := value.DecodeJSON(rec.Pillar)
	if err != nil {
		return nil, err
	}
	m, ok := decoded.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("the saved pillar is not a mapping")
	}
	return m, nil
}

// forgetPillarIfRefused removes the copy when the hub says this node may not
// have its pillar, in an answer or by rejecting its certificate. Without the
// second, a node revoked while it was off kept a copy that a later outage
// would have put back into use. An expired certificate lands here too, and the
// next pillar after a renewal restores it. A 500 or a malformed request says nothing about that and
// leaves the copy alone.
func (n *node) forgetPillarIfRefused(err error) {
	if transport.Rejected(err) {
		n.clearPillarStore()
		return
	}
	var refused *transport.RefusedError
	if !errors.As(err, &refused) {
		return
	}
	if refused.Status == http.StatusUnauthorized || refused.Status == http.StatusForbidden {
		n.clearPillarStore()
	}
}

func (n *node) clearPillarStore() {
	store := n.pillarStore()
	if store == nil {
		return
	}
	if err := store.Clear(); err != nil {
		n.log.Warn("could not remove the last known good pillar", "dir", store.Dir, "error", err.Error())
		return
	}
	n.log.Info("removed the last known good pillar", "dir", store.Dir)
}
