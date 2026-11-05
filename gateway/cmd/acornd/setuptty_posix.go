//go:build unix

package main

import (
	"os"
	"syscall"
)

// statOwner is the numbers that own a file, which name the account an install belongs to.
func statOwner(info os.FileInfo) (uid, gid uint32) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Uid, stat.Gid
	}
	return 0, 0
}
