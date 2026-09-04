//go:build !windows

package builtin

import "path/filepath"

// scriptSuffix is what the temporary copy of a script is named with.
//
// Nothing, on unix: what a file is run by is its shebang line, and the
// name has no part in it.
func scriptSuffix(source string) string { return "" }

// scriptInterpreter is the program that runs a script of this kind, and
// its arguments before the script's own path. Empty means the kernel
// runs the file itself, which is what a shebang is for.
func scriptInterpreter(path string) []string { return nil }

// scriptMode is the mode a temporary script is written with: readable
// and executable by its owner alone, because many carry a credential.
func scriptMode() uint32 { return 0o700 }

var _ = filepath.Ext
