//go:build !unix

package transport

import "net"

// pendingSocketError is unix's SO_ERROR check. Elsewhere it reports nothing:
// on Windows a read already fails when a reset arrives, which is what the
// observer's control test showed in CI (DIVERGENCE 5.680).
func pendingSocketError(*net.TCPConn) error { return nil }
