//go:build unix

package main

import (
	"os"
	"syscall"
)

// statOwner is the numeric owner of a file, which names the account an install belongs to.
func statOwner(info os.FileInfo) uint32 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Uid
	}
	return 0
}
