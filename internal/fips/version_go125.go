//go:build !go1.26

package fips

// moduleVersion answers with the empty string: crypto/fips140.Version
// arrived in Go 1.26, and SPEC 4.1 puts the floor at 1.25.
func moduleVersion() string { return "" }
