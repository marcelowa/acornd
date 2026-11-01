package main

// The door comes from the file an apply wrote, and the environment is an override. These are the two halves,
// with a real assertion signed by a publisher this test runs: a door built from `remote.json` admits the
// person an assertion names, and refuses one minted for another application at the same edge (ADR 0036 §5).

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/identity"
	"github.com/marcelowa/acornd/gateway/internal/remote"
)

// testTeam is the login domain the test's apply recorded. The issuer and the keys URL are derived from it, so
// it is the only value the file has to carry.
const testTeam = "team.cloudflareaccess.com"

// publisher is a Cloudflare Access team the test runs: one RSA key, the key set a door fetches, and the
// assertions it signs.
type publisher struct {
	key    *rsa.PrivateKey
	kid    string
	server *httptest.Server
}

func newPublisher(t *testing.T) publisher {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	kid := "test-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		document := map[string]any{"keys": []map[string]any{{
			"kid": kid,
			"kty": "RSA",
			"alg": "RS256",
			"use": "sig",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}}
		_ = json.NewEncoder(w).Encode(document)
	}))
	t.Cleanup(server.Close)
	return publisher{key: key, kid: kid, server: server}
}

// assertion signs one claim set, in the shape Cloudflare Access issues.
func (p publisher) assertion(t *testing.T, audience, issuer string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": p.kid})
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"aud":   audience,
		"iss":   issuer,
		"email": "ana@example.com",
		"exp":   time.Now().Add(10 * time.Minute).Unix(),
		"iat":   time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// keysFrom makes a door built from a file fetch its keys from the test's publisher, and checks the URL it asked
// for: that URL is the half of Env() a signature cannot prove.
func keysFrom(t *testing.T, world publisher, wantURL string) {
	t.Helper()
	previous := identityKeys
	identityKeys = func(url string) *identity.Keys {
		if url != wantURL {
			t.Errorf("the door asked for keys at %q, want %q", url, wantURL)
		}
		return &identity.Keys{URL: world.server.URL}
	}
	t.Cleanup(func() { identityKeys = previous })
}

// clearIdentityEnvironment is the normal install: no ACORND_IDENTITY* anywhere, so the file is the door.
func clearIdentityEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range identityEnvironment {
		t.Setenv(name, "")
	}
}

// saveDoor writes the file an apply leaves behind.
func saveDoor(t *testing.T, dir, audience, domain string) {
	t.Helper()
	setup := remote.Setup{
		Desired: remote.Desired{Hostname: "console.example.com", ZoneID: "zone", AccountID: "account"},
		State:   remote.State{TunnelID: "t", TunnelToken: "tunnel-token", AppID: "app", Audience: audience, TeamDomain: domain},
	}
	if err := remote.Save(dir, setup, time.Now()); err != nil {
		t.Fatalf("save the door: %v", err)
	}
}

// withAssertion is a browser arriving through the edge.
func withAssertion(raw string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://console.example.com/api/state", nil)
	request.Header.Set(identity.AssertionHeader, raw)
	return request
}

func TestTheDoorComesFromTheFile(t *testing.T) {
	dir := t.TempDir()
	clearIdentityEnvironment(t)
	world := newPublisher(t)
	keysFrom(t, world, "https://"+testTeam+"/cdn-cgi/access/certs")
	saveDoor(t, dir, "aud-file", testTeam)

	door, _, err := identityDoor(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("door: %v", err)
	}
	caller, err := door.Prove(withAssertion(world.assertion(t, "aud-file", "https://"+testTeam)))
	if err != nil {
		t.Fatalf("an assertion from the recorded door was refused: %v", err)
	}
	if caller.Email != "ana@example.com" {
		t.Errorf("the door named %q", caller.Email)
	}

	// An assertion minted for another application at the same edge is not a way in.
	_, err = door.Prove(withAssertion(world.assertion(t, "someone-elses-app", "https://"+testTeam)))
	if err == nil || !strings.Contains(err.Error(), "not for this console") {
		t.Errorf("an assertion for another application was admitted: %v", err)
	}
}

// An install with remote.json configured and no audience, or no team domain, is a half-finished apply. The file
// is real, so the console is not open to the world; the door is the install token and the owner's cookie, and
// the boot says so once rather than at the first browser (ADR 0036 §5).
func TestAHalfFinishedApplyLeavesTheLocalDoor(t *testing.T) {
	cases := []struct{ name, audience, domain, want string }{
		{"no audience", "", testTeam, "no audience"},
		{"no team domain", "aud-file", "", "no team domain"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			clearIdentityEnvironment(t)
			saveDoor(t, dir, test.audience, test.domain)
			out := logs(t, func() {
				door, _, err := identityDoor(dir, "127.0.0.1")
				if err != nil {
					t.Fatalf("door: %v", err)
				}
				token, err := identity.ReadToken(dir)
				if err != nil {
					t.Fatalf("the install has no token: %v", err)
				}
				commandLine := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/api/state", nil)
				commandLine.Header.Set("Authorization", "Bearer "+token)
				if _, err := door.Prove(commandLine); err != nil {
					t.Errorf("the install token was refused: %v", err)
				}
				if _, err := door.Prove(withAssertion("anything")); err == nil {
					t.Error("an assertion was admitted by a door that has no audience")
				}
			})
			if !strings.Contains(out, "half-finished") || !strings.Contains(out, test.want) {
				t.Errorf("the boot said %q, want one line naming %s", out, test.want)
			}
		})
	}
}

// The environment wins over the file, exactly as it did before the file existed: an assertion for the
// environment's audience is admitted, and the one the file records is not (ADR 0036 §5).
func TestTheEnvironmentWinsOverTheFile(t *testing.T) {
	dir := t.TempDir()
	world := newPublisher(t)
	saveDoor(t, dir, "aud-file", testTeam)
	t.Setenv("ACORND_IDENTITY", "access")
	t.Setenv("ACORND_IDENTITY_AUDIENCE", "aud-env")
	t.Setenv("ACORND_IDENTITY_ISSUER", "https://env.cloudflareaccess.com")
	t.Setenv("ACORND_IDENTITY_KEYS", world.server.URL)

	door, _, err := identityDoor(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("door: %v", err)
	}
	if _, err := door.Prove(withAssertion(world.assertion(t, "aud-env", "https://env.cloudflareaccess.com"))); err != nil {
		t.Fatalf("the environment's assertion was refused: %v", err)
	}
	if _, err := door.Prove(withAssertion(world.assertion(t, "aud-file", "https://"+testTeam))); err == nil {
		t.Error("an assertion for the file's audience was admitted, so the file was the door")
	}
}

// A configured file is a door install, and a door install listens on loopback only, whatever the environment
// says: the loopback rule follows the file now (ADR 0036 §5, ADR 0032 §8).
func TestAFileWithADoorRefusesEveryInterface(t *testing.T) {
	dir := t.TempDir()
	clearIdentityEnvironment(t)
	saveDoor(t, dir, "aud-file", testTeam)
	_, _, err := identityDoor(dir, "0.0.0.0")
	if err == nil || !strings.Contains(err.Error(), "loopback only") {
		t.Fatalf("err = %v, want the loopback refusal", err)
	}
}

// The environment is an override, not a merge: the door does not read the file at all when any of the four is
// set, which is why a machine behind somebody else's proxy never has to keep a record it does not use.
func TestTheEnvironmentIsNotMergedWithTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, remote.File), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	world := newPublisher(t)
	t.Setenv("ACORND_IDENTITY", "access")
	t.Setenv("ACORND_IDENTITY_AUDIENCE", "aud-env")
	t.Setenv("ACORND_IDENTITY_ISSUER", "https://env.cloudflareaccess.com")
	t.Setenv("ACORND_IDENTITY_KEYS", world.server.URL)
	door, _, err := identityDoor(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("the environment was not enough: %v", err)
	}
	if _, err := door.Prove(withAssertion(world.assertion(t, "aud-env", "https://env.cloudflareaccess.com"))); err != nil {
		t.Errorf("a request from the environment's publisher was refused: %v", err)
	}
}

// Whether this boot built the door is reported rather than asked for a second time: it is the one fact a restart is
// the fix for, and the console says "no publisher" from it (ADR 0036 §6).
func TestTheDoorSaysWhetherThisBootBuiltThePublisher(t *testing.T) {
	dir := t.TempDir()
	clearIdentityEnvironment(t)
	if _, publisher, err := identityDoor(dir, "127.0.0.1"); err != nil || publisher {
		t.Fatalf("publisher = %v, err = %v; want the local door and no publisher", publisher, err)
	}
	// A file that records an audience and a team domain is a door, and the boot that reads it built it.
	saveDoor(t, dir, "aud-file", testTeam)
	if _, publisher, err := identityDoor(dir, "127.0.0.1"); err != nil || !publisher {
		t.Fatalf("publisher = %v, err = %v; want the door the file records", publisher, err)
	}
	// An apply that died between the two writes records one and not the other: the local door is what this boot has,
	// so a restart would not change anything and it must not say it would.
	saveDoor(t, dir, "aud-file", "")
	if _, publisher, err := identityDoor(dir, "127.0.0.1"); err != nil || publisher {
		t.Fatalf("publisher = %v, err = %v; want no publisher for a half-finished apply", publisher, err)
	}
	// The environment's door is a publisher too, which is what a test and a machine behind somebody else's proxy use.
	world := newPublisher(t)
	t.Setenv("ACORND_IDENTITY", "access")
	t.Setenv("ACORND_IDENTITY_AUDIENCE", "aud-env")
	t.Setenv("ACORND_IDENTITY_ISSUER", "https://env.cloudflareaccess.com")
	t.Setenv("ACORND_IDENTITY_KEYS", world.server.URL)
	if _, publisher, err := identityDoor(dir, "127.0.0.1"); err != nil || !publisher {
		t.Fatalf("publisher = %v, err = %v; want the door the environment names", publisher, err)
	}
}

// The gate carries the two facts the remote screen cannot read for itself: whether this boot built the door, and the
// address the run was started with, which an exec would keep (ADR 0036 §5–§6).
func TestTheGateCarriesWhatARestartWouldChange(t *testing.T) {
	dir := t.TempDir()
	clearIdentityEnvironment(t)
	saveDoor(t, dir, "aud-file", testTeam)
	if err := writeRunRecord(dir, runRecord{PID: 1, Args: []string{"start", "--host", "0.0.0.0"}, Address: "0.0.0.0:7777"}); err != nil {
		t.Fatalf("record the run: %v", err)
	}
	gate, err := openGate(dir, "127.0.0.1")
	if err != nil {
		t.Fatalf("open gate: %v", err)
	}
	if !gate.Publisher {
		t.Error("the boot that read a finished door says it built no publisher")
	}
	if gate.RunHost != "0.0.0.0" {
		t.Errorf("RunHost = %q, want the --host the record names", gate.RunHost)
	}
}
