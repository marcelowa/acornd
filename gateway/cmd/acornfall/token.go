// The token that proves the owner to their own console, and the door a run requires.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/identity"
)

// tokenCommand prints the install's token, minting one on first use.
//
// The value goes to stdout so it can be piped, and everything else goes to stderr: a command whose
// output is a credential must not mix a note into it.
func tokenCommand(argv []string) {
	flags := flag.NewFlagSet("token", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder (default: ~/.acornfall)")
	rotate := flags.Bool("rotate", false, "mint a new token; the old one stops working at once")
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		logFatal(err)
	}
	if *rotate {
		token, err := identity.RotateToken(dir)
		if err != nil {
			logFatal(err)
		}
		fmt.Fprintln(os.Stderr, "acornfall: the previous token no longer works")
		fmt.Println(token)
		return
	}
	token, minted, err := identity.EnsureToken(dir)
	if err != nil {
		logFatal(err)
	}
	if minted {
		// Said out loud, because the file appearing is otherwise a credential nobody asked for.
		fmt.Fprintln(os.Stderr, "acornfall: this install had no token; a new one is in the install folder")
	}
	fmt.Println(token)
}

// identityDoor builds the proof this run requires, or nil for the single-owner install ADR 0020
// describes. The door is off unless this run names a publisher, so an install that never turns people
// on behaves exactly as it did before there was a door.
//
// ACORNFALL_IDENTITY=access means a browser arrives through Cloudflare Access, with the assertion it
// signs; `token` means only the command line, which is what a machine behind somebody else's proxy
// wants. The token door is in the set either way: the command line is how the owner reaches a console
// that has people, and how they get back in when the proxy is what broke.
func identityDoor(dir, host string) (identity.Door, error) {
	kind := strings.TrimSpace(os.Getenv("ACORNFALL_IDENTITY"))
	if kind == "" {
		return nil, nil
	}
	// A console with a door listens on loopback only. The door is reached through a proxy on this
	// machine (ADR 0033), and a listener on every interface would be reachable without one — a door on
	// one address and a hole on the others.
	if !loopback(host) {
		return nil, fmt.Errorf("a console with people listens on loopback only, and --host %q is not one: reach it through cloudflared, or unset ACORNFALL_IDENTITY for a single-owner install", host)
	}
	if _, minted, err := identity.EnsureToken(dir); err != nil {
		return nil, err
	} else if minted {
		fmt.Fprintln(os.Stderr, "acornfall: this install had no token; `acornfall token` prints the new one")
	}

	doors := []identity.Door{identity.Token{Dir: dir}}
	switch kind {
	case "token":
		// The command line only.
	case "access":
		audience := os.Getenv("ACORNFALL_IDENTITY_AUDIENCE")
		issuer := os.Getenv("ACORNFALL_IDENTITY_ISSUER")
		keys := os.Getenv("ACORNFALL_IDENTITY_KEYS")
		var missing []string
		if audience == "" {
			missing = append(missing, "ACORNFALL_IDENTITY_AUDIENCE")
		}
		if issuer == "" {
			missing = append(missing, "ACORNFALL_IDENTITY_ISSUER")
		}
		if keys == "" {
			missing = append(missing, "ACORNFALL_IDENTITY_KEYS")
		}
		if len(missing) > 0 {
			// Loud at startup rather than at the first browser: an assertion door with no audience
			// accepts an assertion minted for anything at the same edge.
			return nil, fmt.Errorf("ACORNFALL_IDENTITY=access needs %s", strings.Join(missing, ", "))
		}
		doors = append(doors, identity.Assertion{
			Audience: audience,
			Issuer:   issuer,
			Keys:     &identity.Keys{URL: keys},
		})
	default:
		return nil, fmt.Errorf("ACORNFALL_IDENTITY=%q is not a door this build has: access or token", kind)
	}
	return identity.Any(doors...), nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
