package main

// The kvm group, without making a person log out.
//
// /dev/kvm is root:kvm 0660. `setup` adds the account to kvm, but a session that was already open keeps the
// group list it was given at login — so the console started from that session cannot open the device, and the
// probe run from it says "not available" about a machine that was just fixed. `sg kvm` lends the group to one
// command, for a member, without a new login (ADR 0026, ADR 0036 §1). It is used only when an actual open of
// the device fails and the account is entitled to the group, and only once.

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strings"
)

// kvmGroupNeeded reports whether this process cannot open /dev/kvm while its account is in the kvm group. The
// open is the question: a device that exists but cannot be opened is exactly the case this fixes, and one that
// opens needs nothing.
func kvmGroupNeeded() bool {
	if os.Getenv("ACORND_KVM_SG") != "" {
		return false
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return false
	}
	device, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err == nil {
		_ = device.Close()
		return false
	}
	account, err := user.Current()
	if err != nil {
		return false
	}
	group, err := user.LookupGroup("kvm")
	if err != nil {
		return false
	}
	ids, err := account.GroupIds()
	if err != nil {
		return false
	}
	return slices.Contains(ids, group.Gid)
}

// startUnderKVMGroup runs this process again under `sg kvm`, once, and returns its exit status. It is called
// before anything starts, so the sandbox tier, the worker and the connector all see the group. The marker keeps
// a machine that still cannot open the device from trying again in a loop.
func startUnderKVMGroup(argv []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	line := append([]string{sgQuote(executable)}, sgQuoteAll(argv)...)
	command := exec.Command("sg", "kvm", "-c", strings.Join(line, " "))
	command.Env = append(os.Environ(), "ACORND_KVM_SG=1")
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			os.Exit(exit.ExitCode())
		}
		return fmt.Errorf("sg kvm: %w", err)
	}
	return nil
}

// sgQuote is one argument for `sg -c`, which takes a shell line: single quotes around the whole value, with
// an embedded quote closed, escaped and reopened.
func sgQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func sgQuoteAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, sgQuote(value))
	}
	return out
}

// startWithKVMGroup is the one line `serve` needs: try the normal start, and if the session is missing a group
// the account already has, start again through sg. A machine without the device, or with it open, does nothing.
func startWithKVMGroup(argv []string) {
	if !kvmGroupNeeded() {
		return
	}
	log.Printf("kvm: /dev/kvm is there and this account is in kvm, but this session predates the group; starting through `sg kvm`")
	if err := startUnderKVMGroup(argv); err != nil {
		log.Printf("kvm: %v; starting without it", err)
		return
	}
	os.Exit(0)
}
