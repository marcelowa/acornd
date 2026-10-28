// The token that proves the owner to their own console, and the door a run requires.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/httpapi"
	"github.com/acornfall/acornfall/gateway/internal/identity"
	"github.com/acornfall/acornfall/gateway/internal/people"
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
	if err := printToken(os.Stdout, os.Stderr, dir, *rotate); err != nil {
		logFatal(err)
	}
}

// printToken is the command's whole job in a function a test can call: the token on stdout, so it can be
// piped, and every note on stderr, so a pipe never becomes part of a credential.
func printToken(out, notes io.Writer, dir string, rotate bool) error {
	if rotate {
		token, err := identity.RotateToken(dir)
		if err != nil {
			return err
		}
		fmt.Fprintln(notes, "acornfall: the previous token no longer works")
		fmt.Fprintln(out, token)
		return nil
	}
	token, minted, err := identity.EnsureToken(dir)
	if err != nil {
		return err
	}
	if minted {
		// Said out loud, because the file appearing is otherwise a credential nobody asked for.
		fmt.Fprintln(notes, "acornfall: this install had no token; a new one is in the install folder")
	}
	fmt.Fprintln(out, token)
	return nil
}

// openGate builds what this run admits by: the door that proves who is calling, and the book of people
// it must then know. Both are nil for the single-owner install ADR 0020 describes, because a door
// nothing configured is how a switched-off guard comes back.
func openGate(dir, host string) (httpapi.Gate, error) {
	door, err := identityDoor(dir, host)
	if err != nil {
		return httpapi.Gate{}, err
	}
	if door == nil {
		return httpapi.Gate{}, nil
	}
	book, err := people.Open(filepath.Join(dir, people.File))
	if err != nil {
		return httpapi.Gate{}, err
	}
	if _, claimed, err := book.Owner(); err != nil {
		return httpapi.Gate{}, err
	} else if !claimed {
		// Loud at boot rather than at the first browser: a console with a door and no owner is one only
		// the machine can get into, and the fix is one command.
		fmt.Fprintln(os.Stderr, "acornfall: this install has people but no owner yet; `acornfall people claim you@example.com` names one")
	}
	return httpapi.Gate{Door: door, People: book, Dir: dir}, nil
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

	remote, err := remoteDoor(kind)
	if err != nil {
		return nil, err
	}
	if remote == nil {
		return identity.Any(identity.Token{Dir: dir}), nil
	}
	return identity.Any(identity.Token{Dir: dir}, remote), nil
}

// remoteDoor is the door a publisher's name builds, or nil for a console only the command line opens.
func remoteDoor(kind string) (identity.Door, error) {
	switch kind {
	case "token":
		return nil, nil
	case "access":
		audience, issuer, keys, err := accessValues()
		if err != nil {
			return nil, err
		}
		return identity.Assertion{
			Audience: audience,
			Issuer:   issuer,
			Keys:     &identity.Keys{URL: keys},
		}, nil
	default:
		return nil, fmt.Errorf("ACORNFALL_IDENTITY=%q is not a door this build has: access or token", kind)
	}
}

// accessValues reads the three values the assertion door checks. All three are required and the refusal
// is at boot rather than at the first browser: an assertion door with no audience accepts an assertion
// minted for anything else at the same edge.
func accessValues() (audience, issuer, keys string, err error) {
	audience = os.Getenv("ACORNFALL_IDENTITY_AUDIENCE")
	issuer = os.Getenv("ACORNFALL_IDENTITY_ISSUER")
	keys = os.Getenv("ACORNFALL_IDENTITY_KEYS")
	var missing []string
	for _, value := range []struct{ name, value string }{
		{"ACORNFALL_IDENTITY_AUDIENCE", audience},
		{"ACORNFALL_IDENTITY_ISSUER", issuer},
		{"ACORNFALL_IDENTITY_KEYS", keys},
	} {
		if value.value == "" {
			missing = append(missing, value.name)
		}
	}
	if len(missing) > 0 {
		return "", "", "", fmt.Errorf("ACORNFALL_IDENTITY=access needs %s", strings.Join(missing, ", "))
	}
	return audience, issuer, keys, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

// ownerEmail is the install owner's address, or empty when nobody has claimed it. It is the one answer
// control needs out of the book: whose the workspaces that predate people are.
func ownerEmail(book *people.Book) string {
	if book == nil {
		return ""
	}
	owner, ok, err := book.Owner()
	if err != nil || !ok {
		return ""
	}
	return owner.Email
}
