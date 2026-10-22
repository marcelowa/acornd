//go:build unix

package sandbox

import (
	"os"
	"syscall"
)

// ownerOf is the account a folder belongs to, or nil where the platform does not say.
func ownerOf(info os.FileInfo) *int {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		uid := int(stat.Uid)
		return &uid
	}
	return nil
}
