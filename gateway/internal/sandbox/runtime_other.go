//go:build !unix

package sandbox

import "os"

// ownerOf has nothing to say on a platform without Unix ownership.
func ownerOf(os.FileInfo) *int { return nil }
