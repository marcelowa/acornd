//go:build !linux

// Package netns configures a sandbox's network namespace. Netlink is the Linux kernel's own
// interface, so this file is what the other platforms build: acornfall still runs on them — the
// host engine, the console, and the microVM engine care nothing for namespaces — and a bwrap
// workspace is refused with a sentence rather than a build that does not exist.
package netns

import "errors"

// errNotLinux is the refusal every function here gives: a network namespace is a Linux feature,
// and nothing above this package should have to know that to say so.
var errNotLinux = errors.New("netns: a sandbox network namespace is a Linux feature; this machine has no user namespaces to put one in")

func Up(string) error              { return errNotLinux }
func Address(string, string) error { return errNotLinux }
func Gateway(string, string) error { return errNotLinux }
func Unreachable(string) error     { return errNotLinux }
