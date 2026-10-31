package identity

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func request(header, value string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/api/state", nil)
	if header != "" {
		r.Header.Set(header, value)
	}
	return r
}

// An install whose admin runs the command line has a token, and that token is the only thing that
// makes a request the admin's.
func TestTheTokenDoorAnswersTheAdmin(t *testing.T) {
	dir := t.TempDir()
	token, minted, err := EnsureToken(dir)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	if !minted {
		t.Error("a first call must mint a token")
	}

	caller, err := Token{Dir: dir}.Prove(request("Authorization", "Bearer "+token))
	if err != nil {
		t.Fatalf("the install's own token was refused: %v", err)
	}
	if !caller.Admin {
		t.Error("the token door must answer the admin")
	}
	if caller.Email != "" {
		t.Errorf("an unclaimed admin has no email, got %q", caller.Email)
	}
}

// ADR 0012's limit is that a guest can reach the port. It must not also be reachable *as the admin*,
// so a request with no token is refused even from loopback — the address is not a door.
func TestALoopbackRequestWithoutProofIsRefused(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := EnsureToken(dir); err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	r := request("", "")
	// Explicitly the loopback address: the point of the test is that it makes no difference.
	r.RemoteAddr = "127.0.0.1:54321"
	if _, err := (Token{Dir: dir}).Prove(r); err == nil {
		t.Fatal("a request with no token was admitted")
	}
}

func TestAnotherTokenIsRefused(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := EnsureToken(dir); err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	_, err := Token{Dir: dir}.Prove(request("Authorization", "Bearer not-this-installs-token"))
	if !errors.Is(err, ErrWrongToken) {
		t.Fatalf("err = %v, want ErrWrongToken", err)
	}
}

// A token in a URL is a token written down: shell history, proxy logs, and the Referer of whatever the
// page loads next. The header is the only place it is read.
func TestTheTokenIsNeverReadFromTheQueryString(t *testing.T) {
	dir := t.TempDir()
	token, _, err := EnsureToken(dir)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/api/state?token="+token, nil)
	if _, err := (Token{Dir: dir}).Prove(r); err == nil {
		t.Fatal("a token in the query string was accepted")
	}
}

func TestARotatedTokenIsTheOnlyOneThatWorks(t *testing.T) {
	dir := t.TempDir()
	first, _, err := EnsureToken(dir)
	if err != nil {
		t.Fatalf("ensure token: %v", err)
	}
	second, err := RotateToken(dir)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if first == second {
		t.Fatal("rotation produced the same token")
	}
	if _, err := (Token{Dir: dir}).Prove(request("Authorization", "Bearer "+first)); err == nil {
		t.Error("the old token still works")
	}
	if _, err := (Token{Dir: dir}).Prove(request("Authorization", "Bearer "+second)); err != nil {
		t.Errorf("the new token does not work: %v", err)
	}
}

// The token is a credential on a shared filesystem; 0600 is the difference between a secret and a
// public file. Rotation rewrites it, so the mode is checked after one rather than only after the
// first mint.
func TestTheTokenFileIsNotReadableByAnyoneElse(t *testing.T) {
	dir := t.TempDir()
	if _, err := RotateToken(dir); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, TokenFile))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
}

// EnsureToken is called on every boot of a locked install, so it must be the same token each time and
// must never mint a second one over a working credential.
func TestEnsureTokenMintsOnceAndKeepsIt(t *testing.T) {
	dir := t.TempDir()
	first, minted, err := EnsureToken(dir)
	if err != nil || !minted {
		t.Fatalf("first: token=%q minted=%v err=%v", first, minted, err)
	}
	second, minted, err := EnsureToken(dir)
	if err != nil || minted {
		t.Fatalf("second: token=%q minted=%v err=%v", second, minted, err)
	}
	if first != second {
		t.Error("a second boot replaced the token")
	}
}

// A token file that exists and cannot be parsed is a problem to report, not a licence to mint over it:
// the running install may be using exactly that file.
func TestEnsureTokenDoesNotOverwriteAFileItCannotUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, TokenFile)
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := EnsureToken(dir); err == nil {
		t.Fatal("an empty token file was silently replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.TrimSpace(string(data)) != "" {
		t.Error("the file was rewritten")
	}
}

// A token path that exists and is not a readable file is reported as itself. Reading it as "no token
// yet" would mint over whatever is there and send the admin looking in the wrong place.
func TestATokenPathThatIsNotAFileIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, TokenFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := ReadToken(dir); err == nil || errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want a problem rather than a first run", err)
	}
	if _, _, err := EnsureToken(dir); err == nil {
		t.Error("EnsureToken minted over a directory")
	}
}

// An install folder that cannot be created is a refusal with a cause, not a panic: the folder is named
// by the caller, and a name under a file is a typo the tool can point at.
func TestAnInstallFolderThatCannotBeCreatedIsReported(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "acornd")
	if err := os.WriteFile(file, []byte("not a folder"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := RotateToken(file); err == nil {
		t.Fatal("a token was minted under a file")
	}
}
