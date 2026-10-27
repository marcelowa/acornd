// The local door: the install's token file, which the command line reads and a browser never has.
//
// It is a file rather than an address because of ADR 0012: a workspace's sandbox can reach the
// gateway's own API, so "this request came from loopback" proves nothing about who sent it. A guest
// can reach the port and still not hold the token, which ADR 0014 already keeps out of every
// sandbox mount.
package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// TokenFile is the file inside the install folder that holds the owner's token. ADR 0014 names it
// when it says the install folder is never mounted into a sandbox, so this name is not free to
// change quietly.
const TokenFile = "token"

// tokenBytes is 32 bytes of randomness: a token nobody guesses, in 43 base64url characters.
const tokenBytes = 32

// Token proves the install's owner by possession of the token file.
//
// Prove reads the file on every request rather than caching it, so a rotation takes effect without a
// restart. It is a page-cache read of 43 bytes, and an attached client holds one request open for
// its stream, so the cost this trades against is the one thing a rotating credential cannot afford:
// a window where the old token still works.
type Token struct {
	// Dir is the install folder, the one passed as --data-dir.
	Dir string
}

// Prove answers the owner when the request carries the install's token, in
// `Authorization: Bearer <token>`. It is read from the header and never from the query string: a URL
// is written down in shell history, in proxy logs, and in the Referer of whatever the page loads.
func (t Token) Prove(r *http.Request) (Caller, error) {
	got, ok := bearer(r)
	if !ok {
		return Caller{}, fmt.Errorf("no install token in the Authorization header")
	}
	want, err := ReadToken(t.Dir)
	if err != nil {
		// No token file is not a proof of anything. It is also not a reason to refuse the whole
		// install: the remote door may still prove someone, so the reason is the answer.
		return Caller{}, fmt.Errorf("this install has no token file yet: %w", err)
	}
	// Constant time, because a token compared with == leaks its prefix to anyone who can time a
	// response.
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return Caller{}, ErrWrongToken
	}
	return Caller{Owner: true}, nil
}

// bearer reads the header's token, without pretending to parse the scheme case-sensitively: RFC 6750
// says it is case-insensitive and clients do disagree.
func bearer(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

// ErrNoToken is the install having no token file, which is the ordinary state of an install that
// never turned people on. It is separate from a file that exists and cannot be read, because the
// first is a first run and the second is a problem to report.
var ErrNoToken = errors.New("this install has no token yet")

// ReadToken is the install's token. A missing file is ErrNoToken, which the caller can print as it
// is.
func ReadToken(dir string) (string, error) {
	path := filepath.Join(dir, TokenFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: run `acornfall token --rotate`", ErrNoToken)
		}
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return token, nil
}

// EnsureToken returns the install's token, minting one when there is none, and reports whether it
// minted one so the caller can say where it went instead of printing a secret nobody asked for.
//
// An install that never turns people on never calls this: ADR 0020's single owner has no token, and
// creating a credential nothing checks is how a switched-off guard comes back.
func EnsureToken(dir string) (token string, minted bool, err error) {
	token, err = ReadToken(dir)
	if err == nil {
		return token, false, nil
	}
	if !errors.Is(err, ErrNoToken) {
		// A file that exists and cannot be read is not a reason to mint over it: that would destroy
		// a token somebody may be using, to fix a permission problem.
		return "", false, err
	}
	if token, err = RotateToken(dir); err != nil {
		return "", false, err
	}
	return token, true, nil
}

// RotateToken replaces the install's token and returns the new one. The file is written beside its
// destination and renamed onto it, so a reader never sees a half-written token and the mode is never
// looser than 0600 — a token created world-readable for one syscall is a token that leaked.
func RotateToken(dir string) (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("minting a token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "."+TokenFile+"-")
	if err != nil {
		return "", err
	}
	// CreateTemp creates 0600 by design; the explicit chmod is for the umask on a filesystem that
	// ignores it, which is the difference between a secret and a public file.
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", err
	}
	_, writeErr := file.WriteString(token + "\n")
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(file.Name())
		return "", errors.Join(writeErr, closeErr)
	}
	if err := os.Rename(file.Name(), filepath.Join(dir, TokenFile)); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return token, nil
}
