//go:build !linux

package youki

// overlaySupported is false where youki cannot run: the overlay is a Linux mount.
func (y *Youki) overlaySupported() bool { return false }
