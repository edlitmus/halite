//go:build !unix

package bridge

// Confine does nothing where there is no setrlimit.
//
// A job object is the mechanism on Windows, and unlike setrlimit it is
// set by the host on the child rather than by the child on itself, so
// there is nothing for the extension to do here. See sandbox_windows.go.
func Confine() {}
