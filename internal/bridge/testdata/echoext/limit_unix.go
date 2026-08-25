//go:build unix

package main

import "syscall"

func currentNoFile() uint64 {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return 0
	}
	return uint64(limit.Cur)
}
