package netns

import (
	"errors"
	"flag"
	"fmt"
)

// ErrUsage is a flag the command does not know, or no command to become: the entry point exits
// 2 for it, so a person sees a usage error rather than a configuration failure.
var ErrUsage = errors.New("usage")

// Command configures the network namespace pasta made from the flags and returns the command
// to exec. It is the whole of what a sandbox's first process does before it becomes the
// sandbox's own process: the routes refusing private ranges are code rather than a script, so a
// machine without iproute2 cannot silently lose the boundary (ADR 0025, ADR 0026).
func Command(argv []string) ([]string, error) {
	flags := flag.NewFlagSet("netns", flag.ContinueOnError)
	address := flags.String("address", "", "the address this namespace's interface gets, with its prefix")
	gateway := flags.String("gateway", "", "the default route's gateway")
	refusePrivate := flags.Bool("refuse-private", false, "refuse every private, link-local, and overlay range: the default for a workspace that was not granted them")
	if err := flags.Parse(argv); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUsage, err)
	}
	command := flags.Args()
	if len(command) == 0 {
		return nil, fmt.Errorf("%w: no command to run", ErrUsage)
	}
	if err := Configure(*address, *gateway, *refusePrivate); err != nil {
		return nil, err
	}
	return command, nil
}

// Configure brings the two interfaces up, gives the tap its address and default route, and —
// when the workspace has no grant — refuses the ranges that mean "inside this machine".
func Configure(address, gateway string, refusePrivate bool) error {
	if address == "" || gateway == "" {
		return fmt.Errorf("no address or gateway: recreate the sandbox")
	}
	for _, name := range []string{"lo", "eth0"} {
		if err := Up(name); err != nil {
			return fmt.Errorf("cannot bring %s up: %w", name, err)
		}
	}
	if err := Address("eth0", address); err != nil {
		return fmt.Errorf("cannot give eth0 %s: %w", address, err)
	}
	if err := Gateway("eth0", gateway); err != nil {
		return fmt.Errorf("cannot route through %s: %w", gateway, err)
	}
	if !refusePrivate {
		return nil
	}
	for _, refused := range refusedRanges {
		if err := Unreachable(refused); err != nil {
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
