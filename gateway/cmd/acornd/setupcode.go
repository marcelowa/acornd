// The setup code, from the command line: the credential that claims a fresh install, and the one line
// that says where to claim it.
//
// ADR 0036 §3. It prints the same way the token does — the value on stdout, so it can be piped, and
// everything else on stderr, so a pipe never becomes part of a credential.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"

	"github.com/marcelowa/acornd/gateway/internal/identity"
	"github.com/marcelowa/acornd/gateway/internal/people"
)

// setupCodeCommand prints the code, replaces it, or removes it. All three are the same question asked
// differently: what proves a person may claim this install before it has an owner.
func setupCodeCommand(argv []string) {
	flags := flag.NewFlagSet("setup-code", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornd folder (default: ~/.acornd)")
	rotate := flags.Bool("rotate", false, "mint a new code; the old one stops working at once")
	disable := flags.Bool("disable", false, "remove the code; the install token still signs in")
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		logFatal(err)
	}
	if err := printSetupCode(os.Stdout, os.Stderr, dir, *rotate, *disable); err != nil {
		logFatal(err)
	}
}

// printSetupCode is the command's whole job in a function a test can call, in printToken's shape: the
// code on stdout and every note on stderr.
func printSetupCode(out, notes io.Writer, dir string, rotate, disable bool) error {
	if rotate && disable {
		return errors.New("--rotate replaces the code and --disable removes it: choose one")
	}
	if disable {
		if err := identity.DisableSetupCode(dir); err != nil {
			return err
		}
		fmt.Fprintln(notes, "acornd: the setup code is gone; `acornd token` still signs in")
		return nil
	}
	if rotate {
		code, err := identity.RotateSetupCode(dir)
		if err != nil {
			return err
		}
		fmt.Fprintln(notes, "acornd: the previous setup code no longer works")
		fmt.Fprintln(out, code)
		return nil
	}
	code, minted, err := identity.EnsureSetupCode(dir)
	if err != nil {
		return err
	}
	if minted {
		// Said out loud, because the file appearing is otherwise a credential nobody asked for.
		fmt.Fprintln(notes, "acornd: this install had no setup code; a new one is in the install folder")
	}
	fmt.Fprintln(out, code)
	return nil
}

// ensureSetupCode mints a code for an install nobody owns yet, and does nothing for one somebody does:
// the code is refused the moment an admin exists, and a credential nothing accepts is one more secret
// to wonder about (ADR 0036 §3).
//
// Best effort, because this is the parent of a detached run: the child opens the gate and reports a
// folder it cannot write loudly, and a failure here would only replace the line this command is about
// to print.
func ensureSetupCode(dir string) {
	book, err := people.Open(filepath.Join(dir, people.File))
	if err != nil {
		return
	}
	if _, claimed, err := book.Admin(); err != nil || claimed {
		return
	}
	_, _, _ = identity.EnsureSetupCode(dir)
}

// setupLine is the one line a fresh install owes the person who started it: where to claim it, and the
// code that proves they may (ADR 0036 §3). It is empty once somebody owns the install or the code was
// disabled, and it reads the two files rather than trusting a child that is still booting, because
// `start -d` answers before the console does.
func setupLine(dir, address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	book, err := people.Open(filepath.Join(dir, people.File))
	if err != nil {
		return ""
	}
	if _, claimed, err := book.Admin(); err != nil || claimed {
		return ""
	}
	code, err := identity.ReadSetupCode(dir)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("acornd: claim this install at http://%s/setup?code=%s",
		net.JoinHostPort(displayHost(host), port), url.QueryEscape(code))
}
