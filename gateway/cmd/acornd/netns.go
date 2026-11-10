package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"syscall"

	"github.com/marcelowa/acornd/gateway/internal/netns"
)

// netnsCommand configures the network namespace pasta made, then becomes the sandbox's own
// process. A sandbox calls this; a person does not.
//
// The configuration itself is netns.Command: the routes refusing private ranges are code rather
// than a script, so a machine without iproute2 cannot silently lose the boundary.
func netnsCommand(argv []string) {
	command, err := netns.Command(argv)
	if err != nil {
		if errors.Is(err, netns.ErrUsage) {
			fmt.Fprintln(os.Stderr, "acornd netns:", err)
			os.Exit(2)
		}
		log.Fatalf("acornd netns: %v", err)
	}
	// Exec, not spawn: this process is the sandbox's first, so its pid, its exit status, and
	// the signals sent to it belong to what runs next.
	if err := syscall.Exec(command[0], command, os.Environ()); err != nil {
		// %q, because the command comes from the argv and an escaped newline cannot start a log
		// record the caller did not write. Gosec's taint analysis follows the value, not the verb.
		// #nosec G706 -- %q escapes the control characters a log injection needs.
		log.Fatalf("acornd netns: cannot run %q: %v", command[0], err)
	}
}
