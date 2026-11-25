//go:build !unix

package main

import "testing"

func prepareWorkDir(t *testing.T, dir, name string) {
	t.Skip("an extension account is a Unix arrangement")
}
