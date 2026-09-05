//go:build !windows

package builtin

import (
	"path/filepath"
	"testing"
)

// redirectPersistentEnviron points the unix store at a file of its own,
// so that a test cannot write the environment of the machine it runs on.
func redirectPersistentEnviron(t *testing.T) {
	t.Helper()
	old := EtcEnvironmentPath
	EtcEnvironmentPath = filepath.Join(t.TempDir(), "environment")
	t.Cleanup(func() { EtcEnvironmentPath = old })
}
