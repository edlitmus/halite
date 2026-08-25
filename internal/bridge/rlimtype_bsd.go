//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package bridge

// rlimType is what this platform's syscall.Rlimit holds.
type rlimType = int64
