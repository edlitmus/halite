//go:build go1.26

package fips

import "crypto/fips140"

func moduleVersion() string { return fips140.Version() }
