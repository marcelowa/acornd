package main

import (
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/identity"
)

// The door is off unless this run names a publisher. That is ADR 0020's install: one owner, no people,
// and no credential to present.
func TestADoorIsOffUnlessThisRunNamesOne(t *testing.T) {
	t.Setenv("ACORNFALL_IDENTITY", "")
	door, err := identityDoor(t.TempDir(), "127.0.0.1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if door != nil {
		t.Error("a door was built with nothing asking for one")
	}
}

// A console with a door listens on loopback only: the door is reached through a proxy on this machine,
// and a listener on every interface is reachable without one (ADR 0033).
func TestAConsoledWithADoorListensOnLoopbackOnly(t *testing.T) {
	t.Setenv("ACORNFALL_IDENTITY", "token")
	_, err := identityDoor(t.TempDir(), "0.0.0.0")
	if err == nil {
		t.Fatal("a door was built for a listener on every interface")
	}
	if !strings.Contains(err.Error(), "loopback only") {
		t.Errorf("err = %q, want it to say where the console may listen", err)
	}
}

// An assertion door with no audience accepts an assertion minted for anything else at the same edge.
// The refusal comes at boot, naming every missing value, rather than at the first browser.
func TestTheAccessDoorNeedsItsThreeValues(t *testing.T) {
	t.Setenv("ACORNFALL_IDENTITY", "access")
	t.Setenv("ACORNFALL_IDENTITY_AUDIENCE", "")
	t.Setenv("ACORNFALL_IDENTITY_ISSUER", "")
	t.Setenv("ACORNFALL_IDENTITY_KEYS", "")
	_, err := identityDoor(t.TempDir(), "127.0.0.1")
	if err == nil {
		t.Fatal("an assertion door was built with nothing to check against")
	}
	for _, name := range []string{
		"ACORNFALL_IDENTITY_AUDIENCE",
		"ACORNFALL_IDENTITY_ISSUER",
		"ACORNFALL_IDENTITY_KEYS",
	} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the refusal does not name %s: %v", name, err)
		}
	}
}

// A configured door mints the install's token, because a console with people is one the command line
// has to prove itself to. Idempotence is the identity package's test; this one is that the wiring asks
// for it at all.
func TestAConfiguredDoorMintsTheInstallToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ACORNFALL_IDENTITY", "token")
	door, err := identityDoor(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if door == nil {
		t.Fatal("no door was built for a run that asked for one")
	}
	token, err := identity.ReadToken(dir)
	if err != nil {
		t.Fatalf("the install has no token: %v", err)
	}
	if token == "" {
		t.Error("the token is empty")
	}
}

// A name this build does not have is a typo, and a typo that silently built the local door would be a
// console that looks locked and is not.
func TestADoorThisBuildDoesNotHaveIsRefused(t *testing.T) {
	t.Setenv("ACORNFALL_IDENTITY", "okta")
	_, err := identityDoor(t.TempDir(), "127.0.0.1")
	if err == nil || !strings.Contains(err.Error(), "okta") {
		t.Fatalf("err = %v, want a refusal naming the value", err)
	}
}
