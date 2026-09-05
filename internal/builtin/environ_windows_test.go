//go:build windows

package builtin

import "testing"

// redirectPersistentEnviron does nothing on Windows: the store is
// HKCU\Environment and the tests use it, under names no other program
// has and removing them again afterwards. A stand-in registry would
// prove nothing about the value types the session builder reads, which
// is the part of this that can actually be got wrong.
func redirectPersistentEnviron(t *testing.T) { t.Helper() }
