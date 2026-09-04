//go:build !windows

package grains

import "os"

// isRunnable reports whether a file in grains.d is a program to run
// rather than a document to parse.
//
// The execute bit is the whole answer on unix.
func isRunnable(path string, info os.FileInfo) bool {
	return info.Mode()&0o111 != 0
}

// providerArgv is how a grain provider is invoked. On unix the file runs
// itself, through its shebang.
func providerArgv(path string) []string { return []string{path} }
