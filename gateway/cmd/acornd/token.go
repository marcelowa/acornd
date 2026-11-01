// The token that proves the admin to their own console, and the door a run requires.
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/marcelowa/acornd/gateway/internal/httpapi"
	"github.com/marcelowa/acornd/gateway/internal/identity"
	"github.com/marcelowa/acornd/gateway/internal/people"
)

// tokenCommand prints the install's token, minting one on first use.
//
// The value goes to stdout so it can be piped, and everything else goes to stderr: a command whose
// output is a credential must not mix a note into it.
func tokenCommand(argv []string) {
	flags := flag.NewFlagSet("token", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornd folder (default: ~/.acornd)")
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
		fmt.Fprintln(notes, "acornd: the previous token no longer works")
		fmt.Fprintln(out, token)
		return nil
	}
	token, minted, err := identity.EnsureToken(dir)
	if err != nil {
		return err
	}
	if minted {
		// Said out loud, because the file appearing is otherwise a credential nobody asked for.
		fmt.Fprintln(notes, "acornd: this install had no token; a new one is in the install folder")
	}
	fmt.Fprintln(out, token)
	return nil
}

// openGate builds what this run admits by: the door that proves who is calling, and the book of people
// it must then know.
//
// The book is opened on every install now, not only one with a door: the console configures its own
// remote access, and that needs a book to name people in before the door exists (ADR 0036 §5). The mode
// follows from it — no admin means setup, an admin means the door — and it is read per request, so the
// claim that ends setup takes effect without a restart (ADR 0036 §3).
func openGate(dir, host string) (httpapi.Gate, error) {
	door, err := identityDoor(dir, host)
	if err != nil {
		return httpapi.Gate{}, err
	}
	book, err := people.Open(filepath.Join(dir, people.File))
	if err != nil {
		return httpapi.Gate{}, err
	}
	if _, claimed, err := book.Admin(); err != nil {
		return httpapi.Gate{}, err
	} else if !claimed {
		// A fresh install generates the code it is claimed with on first boot (ADR 0036 §3). Failing here
		// is the boot being unable to write its own folder, which every other file in it would also fail.
		if _, _, err := identity.EnsureSetupCode(dir); err != nil {
			return httpapi.Gate{}, err
		}
	}
	return httpapi.Gate{Door: door, People: book, Dir: dir}, nil
}

// identityDoor builds the proof this run requires: the install token, the owner's session cookie, and
// the assertion door when this run names a publisher.
//
// The door is always there, because the console is closed by default now (ADR 0036 §3). ACORND_IDENTITY
// only decides whether a proxy's assertion is the third proof: `access` means a browser may arrive
// through Cloudflare Access with the assertion it signs; unset means the token and the cookie are the
// whole of it, which is what a machine behind somebody else's proxy wants.
func identityDoor(dir, host string) (identity.Door, error) {
	kind := strings.TrimSpace(os.Getenv("ACORND_IDENTITY"))
	if kind != "" && !loopback(host) {
		// A console behind a publisher listens on loopback only. The door is reached through a proxy on
		// this machine (ADR 0033), and a listener on every interface would be reachable without one — a
		// door on one address and a hole on the others. Without a publisher there is nothing in front to
		// reach it through, so the default bind stays 0.0.0.0 (ADR 0036 §5).
		return nil, fmt.Errorf("a console behind a publisher listens on loopback only, and --host %q is not one: bind 127.0.0.1 and reach it through cloudflared", host)
	}
	// The session cookie is signed with a key derived from this token, so every install has one, door or
	// no door (ADR 0036 §4).
	if _, minted, err := identity.EnsureToken(dir); err != nil {
		return nil, err
	} else if minted {
		fmt.Fprintln(os.Stderr, "acornd: this install had no token; `acornd token` prints the new one")
	}

	remote, err := remoteDoor(kind)
	if err != nil {
		return nil, err
	}
	if remote == nil {
		return identity.Any(identity.Token{Dir: dir}, identity.Session{Dir: dir}), nil
	}
	return identity.Any(identity.Token{Dir: dir}, remote, identity.Session{Dir: dir}), nil
}

// remoteDoor is the door a publisher's name builds, or nil for a console only the local proofs open.
func remoteDoor(kind string) (identity.Door, error) {
	switch kind {
	case "", "token":
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
		return nil, fmt.Errorf("ACORND_IDENTITY=%q is not a door this build has: access or token", kind)
	}
}

// accessValues reads the three values the assertion door checks. All three are required and the refusal
// is at boot rather than at the first browser: an assertion door with no audience accepts an assertion
// minted for anything else at the same edge.
func accessValues() (audience, issuer, keys string, err error) {
	audience = os.Getenv("ACORND_IDENTITY_AUDIENCE")
	issuer = os.Getenv("ACORND_IDENTITY_ISSUER")
	keys = os.Getenv("ACORND_IDENTITY_KEYS")
	var missing []string
	for _, value := range []struct{ name, value string }{
		{"ACORND_IDENTITY_AUDIENCE", audience},
		{"ACORND_IDENTITY_ISSUER", issuer},
		{"ACORND_IDENTITY_KEYS", keys},
	} {
		if value.value == "" {
			missing = append(missing, value.name)
		}
	}
	if len(missing) > 0 {
		return "", "", "", fmt.Errorf("ACORND_IDENTITY=access needs %s", strings.Join(missing, ", "))
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

// ownerEmail is the install admin's address, or empty when nobody has claimed it. It is the one answer
// control needs out of the book: whose the workspaces that predate people are.
func ownerEmail(book *people.Book) string {
	if book == nil {
		return ""
	}
	admin, ok, err := book.Admin()
	if err != nil || !ok {
		return ""
	}
	return admin.Email
}
