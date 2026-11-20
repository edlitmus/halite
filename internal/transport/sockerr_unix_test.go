//go:build unix

package transport

import (
	"net"
	"syscall"
)

// pendingSocketError is the error the kernel holds for the socket and has
// not yet reported to a read: SO_ERROR, which reading clears.
//
// The observer needs it because a read is not guaranteed to report a reset.
// Linux and macOS answered every read with EOF when the reset arrived after
// the peer's FIN, in CI on the PR for issue 757, while FreeBSD's reads failed
// with ECONNRESET (DIVERGENCE 5.680).
func pendingSocketError(c *net.TCPConn) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil
	}
	var pending int
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		pending, sockErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_ERROR)
	}); err != nil || sockErr != nil || pending == 0 {
		return nil
	}
	return syscall.Errno(pending)
}
