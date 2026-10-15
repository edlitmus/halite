package cli

import (
	"errors"
	"net"
	"syscall"
)

// ListenMayRecover reports whether a service's listener failed for a
// reason that a restart a few seconds later can fix, and so should exit 1
// (with Exitf) rather than ExitConfig.
//
// Three: the address is not on this host yet (EADDRNOTAVAIL), which is
// a `listen` naming an address that DHCP or a secondary interface has
// not brought up when the unit starts; the port is still held
// (EADDRINUSE), by a previous process that has not finished exiting or
// by one that will let it go; and a host name in `listen` that did not
// resolve, because the resolver is network too. Anything else -- a port
// below 1024 for an account without the capability, an address that
// does not parse -- is what the operator wrote, and stays wrong however
// often it is retried.
//
// Shared by `halite-hub serve` and `halite-api serve`, whose units stop
// on ExitConfig and restart on 1. It was the hub's own until the API was
// found exiting 1 for the same three failures under a unit that stopped
// on 1, so that an API whose address came up late stayed down
// (DIVERGENCE 5.287, 5.289). One function, so that the two services
// cannot come to disagree about what a restart fixes.
//
// The errno comparisons match on Unix only. On Windows the socket error
// is a WSA code that syscall.EADDRINUSE is not, so there a busy port is
// classed with the configuration faults; nothing on Windows reads the
// distinction.
func ListenMayRecover(err error) bool {
	var dns *net.DNSError
	return errors.Is(err, syscall.EADDRNOTAVAIL) ||
		errors.Is(err, syscall.EADDRINUSE) ||
		errors.As(err, &dns)
}
