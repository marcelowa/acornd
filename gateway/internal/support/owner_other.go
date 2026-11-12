//go:build !unix

package support

import "os"

// ownerOf is -1 on a platform with no uid to name.
func ownerOf(os.FileInfo) (int, int) {
	return -1, -1
}
