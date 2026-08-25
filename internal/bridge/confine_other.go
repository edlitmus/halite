//go:build !unix

package bridge

// Confine does nothing on a platform without setrlimit.
func Confine() {}

// NetworkDenied reports whether the host declined to grant the network.
func NetworkDenied() bool { return false }
