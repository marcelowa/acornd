//go:build unix

package support

import (
	"os"
	"syscall"
)

// ownerOf is the account a cgroup file belongs to, or -1 where the platform does not say.
func ownerOf(info os.FileInfo) (int, int) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid), int(stat.Gid)
	}
	return -1, -1
}
