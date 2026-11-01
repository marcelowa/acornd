package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/identity"
	"github.com/marcelowa/acornd/gateway/internal/people"
)

// The command is what a script pipes, so what it prints is the code and nothing else: a note about
// minting one goes to stderr, where it cannot become part of a credential.
func TestTheSetupCodeCommandPrintsTheCodeAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	var out, notes strings.Builder
	if err := printSetupCode(&out, &notes, dir, false, false); err != nil {
		t.Fatalf("print: %v", err)
	}
	fromFile, err := identity.ReadSetupCode(dir)
	if err != nil {
		t.Fatalf("the command printed no code: %v", err)
	}
	if strings.TrimSpace(out.String()) != fromFile {
		t.Errorf("printed %q, want the code the install holds", out.String())
	}
	if !strings.Contains(notes.String(), "had no setup code") {
		t.Errorf("minting a credential said nothing about it: %q", notes.String())
	}
}

// Rotation is the one operation on a code that has to be observable: the printed value is the new one,
// the old one stops matching, and the second run does not announce a mint.
func TestTheSetupCodeCommandRotates(t *testing.T) {
	dir := t.TempDir()
	var first, notes strings.Builder
	if err := printSetupCode(&first, &notes, dir, false, false); err != nil {
		t.Fatalf("print: %v", err)
	}
	var second strings.Builder
	if err := printSetupCode(&second, &notes, dir, true, false); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if strings.TrimSpace(first.String()) == strings.TrimSpace(second.String()) {
		t.Fatal("rotation printed the same code")
	}
	fromFile, err := identity.ReadSetupCode(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if fromFile != strings.TrimSpace(second.String()) {
		t.Errorf("the install holds %q, want the printed new code", fromFile)
	}
	if !strings.Contains(notes.String(), "previous setup code no longer works") {
		t.Errorf("rotation did not say the old code stopped: %q", notes.String())
	}
}

// Disabling says where the way in went instead: the install token is on the same form, so a person who
// removed the code is not locked out (ADR 0036 §3).
func TestTheSetupCodeCommandDisables(t *testing.T) {
	dir := t.TempDir()
	if _, err := identity.RotateSetupCode(dir); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	var out, notes strings.Builder
	if err := printSetupCode(&out, &notes, dir, false, true); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if out.String() != "" {
		t.Errorf("disabling printed a code: %q", out.String())
	}
	if _, err := identity.ReadSetupCode(dir); !errors.Is(err, identity.ErrNoSetupCode) {
		t.Fatalf("the code survived: %v", err)
	}
	if !strings.Contains(notes.String(), "acornd token") {
		t.Errorf("disabling did not say how to sign in: %q", notes.String())
	}
}

// Two answers to one command is a typo, and a command that silently picked one would hide it.
func TestTheSetupCodeCommandRefusesBothFlags(t *testing.T) {
	if err := printSetupCode(&strings.Builder{}, &strings.Builder{}, t.TempDir(), true, true); err == nil {
		t.Fatal("rotate and disable together were accepted")
	}
}

// claimBook is a book in dir with an admin, so the install has an owner.
func claimBook(t *testing.T, dir string) {
	t.Helper()
	book, err := people.Open(filepath.Join(dir, people.File))
	if err != nil {
		t.Fatalf("open book: %v", err)
	}
	if _, err := book.Claim("marcelo@example.com", "Marcelo"); err != nil {
		t.Fatalf("claim: %v", err)
	}
}

// The line a person needs: where to claim this install, with the code that proves they may. It is empty
// once somebody owns the install or the code is gone, because the URL would then lead to a form that
// refuses them (ADR 0036 §3).
func TestSetupLine(t *testing.T) {
	fresh := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		if _, err := identity.RotateSetupCode(dir); err != nil {
			t.Fatalf("rotate: %v", err)
		}
		return dir
	}
	claimedDir := func(t *testing.T) string {
		t.Helper()
		dir := fresh(t)
		claimBook(t, dir)
		return dir
	}
	cases := []struct {
		name    string
		dir     func(*testing.T) string
		address string
		want    string
	}{
		{
			name:    "a wildcard bind is shown on loopback",
			dir:     fresh,
			address: "0.0.0.0:7777",
			want:    "http://127.0.0.1:7777/setup?code=",
		},
		{
			name:    "an address a person can open is shown as itself",
			dir:     fresh,
			address: "192.0.2.10:7777",
			want:    "http://192.0.2.10:7777/setup?code=",
		},
		{
			name:    "an install somebody owns has nothing to claim",
			dir:     claimedDir,
			address: "127.0.0.1:7777",
			want:    "",
		},
		{
			name:    "no code is nothing to print",
			dir:     func(t *testing.T) string { return t.TempDir() },
			address: "127.0.0.1:7777",
			want:    "",
		},
		{
			name:    "an address that is not one prints nothing",
			dir:     fresh,
			address: "not-an-address",
			want:    "",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := test.dir(t)
			got := setupLine(dir, test.address)
			if test.want == "" {
				if got != "" {
					t.Fatalf("setupLine = %q, want no line", got)
				}
				return
			}
			if !strings.Contains(got, test.want) {
				t.Fatalf("setupLine = %q, want it to contain %q", got, test.want)
			}
			stored, err := identity.ReadSetupCode(dir)
			if err != nil {
				t.Fatalf("read code: %v", err)
			}
			if !strings.HasSuffix(got, stored) {
				t.Errorf("the URL does not carry the install's code: %q", got)
			}
		})
	}
}

// The parent of a detached run mints the code before the child can, and an install somebody owns gets
// none: the code is refused the moment an admin exists (ADR 0036 §3).
func TestEnsureSetupCode(t *testing.T) {
	fresh := t.TempDir()
	ensureSetupCode(fresh)
	if _, err := identity.ReadSetupCode(fresh); err != nil {
		t.Fatalf("a fresh install got no code: %v", err)
	}
	claimed := t.TempDir()
	claimBook(t, claimed)
	ensureSetupCode(claimed)
	if _, err := identity.ReadSetupCode(claimed); !errors.Is(err, identity.ErrNoSetupCode) {
		t.Fatalf("a claimed install got a code nobody can use: %v", err)
	}
}
