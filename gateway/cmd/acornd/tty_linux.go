//go:build linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminal asks the kernel whether this is a terminal, which is the only answer that matters:
// a stat says "character device" for /dev/null too, and sudo needs a real terminal to read a
// password from.
func isTerminal(file *os.File) bool {
	var settings syscall.Termios
	// #nosec G103 -- the third argument is a pointer to a local this call fills in, which is what
	// TCGETS is defined as; the kernel writes the terminal's settings there and nothing is read
	// through the pointer afterwards beyond that struct.
	_, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL, file.Fd(), uintptr(syscall.TCGETS),
		uintptr(unsafe.Pointer(&settings)), 0, 0, 0)
	return errno == 0
}
