//go:build !windows

package builtin

// platformServiceProviders is the init systems that only exist on one
// platform and can only be compiled for it.
//
// Empty here: systemd, the BSD rc scripts, sysvinit and launchd are all
// spoken to by running a binary, so they compile everywhere and live in
// the cross-platform list. The service control manager is an API, not a
// binary, so it cannot.
func platformServiceProviders() []serviceProvider { return nil }
