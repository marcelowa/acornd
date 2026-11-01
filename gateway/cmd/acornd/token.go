// The token that proves the admin to their own console, and the door a run requires.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/marcelowa/acornd/gateway/internal/httpapi"
	"github.com/marcelowa/acornd/gateway/internal/identity"
	"github.com/marcelowa/acornd/gateway/internal/people"
	"github.com/marcelowa/acornd/gateway/internal/remote"
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
// the assertion door when this install has a publisher in front of it.
//
// The door is always there, because the console is closed by default now (ADR 0036 §3). Where the assertion
// door comes from is the one thing that moved: `remote.json` is the door, and the environment is an override
// for tests and for a machine behind somebody else's proxy (ADR 0036 §5). ACORND_IDENTITY is no longer what
// turns Access on; a file an apply wrote is.
func identityDoor(dir, host string) (identity.Door, error) {
	byEnvironment := identityFromEnvironment()
	setup := remote.Setup{}
	if !byEnvironment {
		loaded, err := remote.Load(dir)
		if err != nil {
			return nil, err
		}
		setup = loaded
	}
	if (byEnvironment || setup.Configured()) && !loopback(host) {
		// A console behind a publisher listens on loopback only. The door is reached through a proxy on this
		// machine (ADR 0033), and a listener on every interface would be reachable without one — a door on one
		// address and a hole on the others. Without a publisher there is nothing in front to reach it through,
		// so an install with no remote access keeps binding 0.0.0.0 (ADR 0036 §5).
		return nil, fmt.Errorf("a console behind a publisher listens on loopback only, and --host %q is not one: bind 127.0.0.1 and reach it through cloudflared", host)
	}
	// The session cookie is signed with a key derived from this token, so every install has one, door or
	// no door (ADR 0036 §4).
	if _, minted, err := identity.EnsureToken(dir); err != nil {
		return nil, err
	} else if minted {
		fmt.Fprintln(os.Stderr, "acornd: this install had no token; `acornd token` prints the new one")
	}

	publisher, err := publisherDoor(setup, byEnvironment)
	if err != nil {
		return nil, err
	}
	if publisher == nil {
		return identity.Any(identity.Token{Dir: dir}, identity.Session{Dir: dir}), nil
	}
	return identity.Any(identity.Token{Dir: dir}, publisher, identity.Session{Dir: dir}), nil
}

// identityEnvironment is the four variables that put a publisher in front of the console by hand. Any of them
// carrying a value makes the environment the door, and the file is not read at all: that is what a test and a
// machine behind somebody else's proxy need, and it is how every install was configured before remote.json was
// one (ADR 0036 §5).
var identityEnvironment = []string{
	"ACORND_IDENTITY",
	"ACORND_IDENTITY_AUDIENCE",
	"ACORND_IDENTITY_ISSUER",
	"ACORND_IDENTITY_KEYS",
}

func identityFromEnvironment() bool {
	for _, name := range identityEnvironment {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

// publisherDoor is the assertion door this run has, or nil when only the local proofs open the console: the
// environment's four values when it set any of them, and otherwise the two values an apply wrote down, with
// the issuer and keys URL derived from the team domain.
func publisherDoor(setup remote.Setup, byEnvironment bool) (identity.Door, error) {
	if byEnvironment {
		return remoteDoor(strings.TrimSpace(os.Getenv("ACORND_IDENTITY")))
	}
	if !setup.Configured() {
		return nil, nil
	}
	if missing := missingDoorValue(setup); missing != "" {
		// A configured file with no audience or no team domain is an apply that did not finish. The console is
		// closed to the install token and the owner's cookie, and the person who ran it is told once, here,
		// rather than at the first browser (ADR 0036 §5).
		log.Printf("acornd: remote access is configured but its apply is half-finished (%s): the door is the install token and the owner's cookie until `acornd remote apply` finishes", missing)
		return nil, nil
	}
	values := setup.Env()
	return identity.Assertion{
		Audience: values["ACORND_IDENTITY_AUDIENCE"],
		Issuer:   values["ACORND_IDENTITY_ISSUER"],
		Keys:     identityKeys(values["ACORND_IDENTITY_KEYS"]),
	}, nil
}

// missingDoorValue names what a half-finished apply did not record, in the words of the file's fields.
func missingDoorValue(setup remote.Setup) string {
	audience, domain := setup.State.Audience == "", setup.State.TeamDomain == ""
	switch {
	case audience && domain:
		return "no audience and no team domain"
	case audience:
		return "no audience"
	case domain:
		return "no team domain"
	}
	return ""
}

// identityKeys is the verifier's key source. A variable so a test can point a door built from a file at a
// publisher it runs, without the file having to name a URL a test server can answer.
var identityKeys = func(url string) *identity.Keys { return &identity.Keys{URL: url} }

// remoteDoor is the door the environment's ACORND_IDENTITY builds, or nil when the token and the cookie are the
// whole of it: `access` reads the other three values, and `token` — or nothing at all — means a console with no
// proxy in front, which is what a machine behind somebody else's wants (ADR 0036 §5).
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
