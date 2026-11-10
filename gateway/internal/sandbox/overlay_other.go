//go:build !linux

package sandbox

import "errors"

// MountOverlay is Linux-only: the youki engine is the one that uses it, and youki is Linux-only.
func MountOverlay(_, _, _, _ string) error {
	return errors.New("sandbox: an overlay root is a Linux mount")
}

func OverlayCommand([]string) error { return MountOverlay("", "", "", "") }

func OverlayRunCommand([]string) error { return MountOverlay("", "", "", "") }
