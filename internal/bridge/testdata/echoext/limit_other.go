//go:build !unix

package main

func currentNoFile() uint64 { return 0 }
