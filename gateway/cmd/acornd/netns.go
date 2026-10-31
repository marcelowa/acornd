package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"syscall"

	"github.com/marcelowa/acornd/gateway/internal/netns"
)

// netnsCommand configures the network namespace pasta made, then becomes the sandbox's own
// process. A sandbox calls this; a person does not.
//
// It exists so that the routes refusing private ranges are code rather than a script: the
// script it replaces ran `ip route add unreachable ... 2>/dev/null`, which meant a machine
// without iproute2 lost the boundary and said nothing. Every step now fails loudly.
func netnsCommand(argv []string) {
	flags := flag.NewFlagSet("netns", flag.ContinueOnError)
	address := flags.String("address", "", "the address this namespace's interface gets, with its prefix")
	gateway := flags.String("gateway", "", "the default route's gateway")
	refusePrivate := flags.Bool("refuse-private", false, "refuse every private, link-local, and overlay range: the default for a workspace that was not granted them")
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	command := flags.Args()
	if len(command) == 0 {
		log.Fatal("acornd netns: no command to run")
	}
	if err := configureNamespace(*address, *gateway, *refusePrivate); err != nil {
		log.Fatalf("acornd netns: %v", err)
	}
	// Exec, not spawn: this process is the sandbox's first, so its pid, its exit status, and
	// the signals sent to it belong to what runs next.
	if err := syscall.Exec(command[0], command, os.Environ()); err != nil {
		log.Fatalf("acornd netns: cannot run %s: %v", command[0], err)
	}
}

// configureNamespace brings the two interfaces up, gives the tap its address and default route,
// and — when the workspace has no grant — refuses the ranges that mean "inside this machine".
func configureNamespace(address, gateway string, refusePrivate bool) error {
	if address == "" || gateway == "" {
		return fmt.Errorf("no address or gateway: recreate the sandbox")
	}
	for _, name := range []string{"lo", "eth0"} {
		if err := netns.Up(name); err != nil {
			return fmt.Errorf("cannot bring %s up: %w", name, err)
		}
	}
	if err := netns.Address("eth0", address); err != nil {
		return fmt.Errorf("cannot give eth0 %s: %w", address, err)
	}
	if err := netns.Gateway("eth0", gateway); err != nil {
		return fmt.Errorf("cannot route through %s: %w", gateway, err)
	}
	if !refusePrivate {
		return nil
	}
	for _, refused := range refusedRanges {
		if err := netns.Unreachable(refused); err != nil {
			return fmt.Errorf("cannot refuse %s: %w", refused, err)
		}
	}
	return nil
}

// refusedRanges are the ranges a workspace without a grant cannot reach. They are code, not
// configuration, so that a sandbox cannot be given a smaller list by editing a file: adding one
// here is a change a reviewer sees. See docs/adr/0025-sandbox-egress.md.
var refusedRanges = []string{
	"10.0.0.0/8",     // private, and the tailnet's neighbours
	"100.64.0.0/10",  // carrier-grade NAT: Tailscale's own range
	"169.254.0.0/16", // link-local, including cloud metadata endpoints
	"172.16.0.0/12",  // private
	"192.168.0.0/16", // private
	"fc00::/7",       // IPv6 unique local
	"fe80::/10",      // IPv6 link-local
}
