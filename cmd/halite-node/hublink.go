package main

import (
	"context"
	"sync/atomic"

	"github.com/edlitmus/halite/internal/transport"
)

// hubLink is the hub client this node's pillar and file fetches use, held
// so that it can be replaced under them.
//
// Both used to be given the client of the first connection and keep it.
// A client carries the certificate it was built with, so once a renewal
// revoked the old serial -- by hand with `renew --force`, or by the agent
// itself at half its certificate's life, which every node does -- each
// highstate was refused as "this node's enrollment is revoked: superseded
// by a renewal" until the agent was restarted, and the refusal deleted the
// last known good pillar on the way. The subscribe stream was rebuilt on
// every reconnect and so came back on the new certificate, which is why
// the node looked connected the whole time. Found driving the release
// binaries through a rolling upgrade (issue 766).
//
// attachToHub replaces it on every connection, and keepRenewed as soon as
// a renewal is installed, so neither fetch outlives the identity it was
// made with. One per node, shared by every job's copy as sandboxHolder is.
type hubLink struct {
	cur atomic.Pointer[transport.Client]
}

func (h *hubLink) set(c *transport.Client) { h.cur.Store(c) }

func (h *hubLink) client() *transport.Client { return h.cur.Load() }

// FileManifest and FetchFile make it the file server's fileserver.Fetch.
func (h *hubLink) FileManifest(ctx context.Context, env, prefix string) ([]byte, error) {
	return h.client().FileManifest(ctx, env, prefix)
}

func (h *hubLink) FetchFile(ctx context.Context, env, path, etag string) ([]byte, string, bool, error) {
	return h.client().FetchFile(ctx, env, path, etag)
}

// link is this node's hubLink, made on first use for a node built without
// setup, which only a test does.
func (n *node) link() *hubLink {
	if n.hub == nil {
		n.hub = &hubLink{}
	}
	return n.hub
}
