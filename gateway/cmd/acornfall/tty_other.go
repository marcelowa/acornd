//go:build !linux

package main

import "os"

// isTerminal answers no on a platform this product does not sandbox on: everything that needs a
// password here is a Linux machine's step, so the command prints the steps instead of guessing at
// a terminal.
func isTerminal(*os.File) bool { return false }
