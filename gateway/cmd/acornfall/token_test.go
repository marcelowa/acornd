package main

import (
	"net/http"
	"net/http/httptest"
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

// The command is what a script pipes, so what it prints is the token and nothing else: a note about
// minting one goes to stderr, where it cannot become part of a credential.
func TestTheTokenCommandPrintsTheTokenAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	var out, notes strings.Builder
	if err := printToken(&out, &notes, dir, false); err != nil {
		t.Fatalf("print: %v", err)
	}
	fromFile, err := identity.ReadToken(dir)
	if err != nil {
		t.Fatalf("the command printed no token: %v", err)
	}
	if strings.TrimSpace(out.String()) != fromFile {
		t.Errorf("printed %q, want the token the install holds", out.String())
	}
	if !strings.Contains(notes.String(), "had no token") {
		t.Errorf("minting a credential said nothing about it: %q", notes.String())
	}
}

// Rotation is the one operation on a credential that has to be observable: the printed value must be
// the new one, the old one must stop working at once, and the second run must not announce a mint.
func TestTheTokenCommandRotates(t *testing.T) {
	dir := t.TempDir()
	var first, notes strings.Builder
	if err := printToken(&first, &notes, dir, false); err != nil {
		t.Fatalf("print: %v", err)
	}
	var second strings.Builder
	if err := printToken(&second, &notes, dir, true); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if strings.TrimSpace(first.String()) == strings.TrimSpace(second.String()) {
		t.Fatal("rotation printed the same token")
	}
	fromFile, err := identity.ReadToken(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if fromFile != strings.TrimSpace(second.String()) {
		t.Errorf("the install holds %q, want the printed new token", fromFile)
	}
	if !strings.Contains(notes.String(), "previous token no longer works") {
		t.Errorf("rotation did not say the old token stopped: %q", notes.String())
	}
}

// A run that names Access gets both doors: the assertion door for the browser, and the token door for
// the command line that has to get in when the proxy is what broke.
func TestARunThatNamesAccessGetsBothDoors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ACORNFALL_IDENTITY", "access")
	t.Setenv("ACORNFALL_IDENTITY_AUDIENCE", "aud-tag")
	t.Setenv("ACORNFALL_IDENTITY_ISSUER", "https://team.cloudflareaccess.com")
	t.Setenv("ACORNFALL_IDENTITY_KEYS", "https://team.cloudflareaccess.com/cdn-cgi/access/certs")
	door, err := identityDoor(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	browser := httptest.NewRequest(http.MethodGet, "http://console.example.com/api/state", nil)
	if _, err := door.Prove(browser); err == nil || !strings.Contains(err.Error(), "identity assertion") {
		t.Errorf("a browser with no assertion was not sent to the edge: %v", err)
	}
	token, err := identity.ReadToken(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	commandLine := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/api/state", nil)
	commandLine.Header.Set("Authorization", "Bearer "+token)
	if _, err := door.Prove(commandLine); err != nil {
		t.Errorf("the command line could not get in: %v", err)
	}
}

// Which addresses count as this machine. A door on one address and a hole on the others is the failure
// this list exists to prevent, so the list is a table rather than a claim.
func TestLoopbackIsTheMachineAndNothingElse(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":   true,
		"::1":         true,
		"localhost":   true,
		"0.0.0.0":     false,
		"192.0.2.1":   false,
		"example.com": false,
		"":            false,
	}
	for host, want := range cases {
		if got := loopback(host); got != want {
			t.Errorf("loopback(%q) = %v, want %v", host, got, want)
		}
	}
}

// The command line proves itself to the gateway it is talking to, and sends nothing when the install has
// no token, which is the single-owner install ADR 0020 describes.
func TestProveSendsTheInstallTokenWhenThereIsOne(t *testing.T) {
	dir := t.TempDir()
	token, err := identity.RotateToken(dir)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7777/api/update/check", nil)
	prove(request, dir)
	if got := request.Header.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization = %q, want the install's token", got)
	}

	for _, empty := range []string{"", t.TempDir()} {
		bare := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:7777/api/update/check", nil)
		prove(bare, empty)
		if got := bare.Header.Get("Authorization"); got != "" {
			t.Errorf("a request to an install with no token sent %q", got)
		}
	}
}
