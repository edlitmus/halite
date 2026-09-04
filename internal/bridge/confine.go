package bridge

import "os"

// NetworkDenied reports whether the host declined to grant the network.
//
// An extension built with this package checks it and does not dial. It
// is a declaration honoured rather than a boundary enforced, which is
// the difference `Sandbox.Describe` spells out.
//
// It reads an environment variable and nothing else, so it belongs to no
// platform. It used to live in the unix file and return a bare false
// everywhere else, which meant an extension on Windows was never told
// the network was denied — the one part of the sandbox that needs no
// kernel mechanism at all, silently absent on the platform with the
// least of the rest.
func NetworkDenied() bool { return os.Getenv("HALITE_EXT_NETWORK") == "deny" }
