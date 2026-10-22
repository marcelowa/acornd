//go:build !linux

package main

// credentialFor has no work to do on a platform where the probes cannot run as another account:
// everything a sandbox needs is a Linux feature, and setup there prints what is missing.
func credentialFor(uint32, uint32, []uint32) interface{ SetCredential() } { return nil }
