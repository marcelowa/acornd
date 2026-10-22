//go:build linux

package main

import "syscall"

// credentialFor is the account a probe child runs as: its ids and its groups, so a probe sees
// exactly what a login would.
func credentialFor(uid, gid uint32, groups []uint32) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: groups},
	}
}
