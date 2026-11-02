// The owner's session: a cookie that says who this browser is, signed with a key derived from the
// install token.
//
// ADR 0036 §4. Every request carries a proof or it does not, and this one is the proof a person has
// after they claim the install, so the console stays signed in for a month without a session table.
// The key is derived rather than stored: the install token is already the credential that proves
// ownership, and this is the browser's copy of it with a name attached. Rotating the token therefore
// signs every browser out, which is the right answer to "the token leaked".
package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SessionCookie is the cookie's name. It is named in ADR 0036 §4 because the interface's sign-in and
// the door have to agree on it, and a rename is a sign-out for every browser.
const SessionCookie = "acornd_session"

// sessionLifetime is how long a browser stays signed in: a month, Cloudflare Access's own ceiling, so
// the two sessions expire together instead of one surprising the other (ADR 0036 §6).
const sessionLifetime = 30 * 24 * time.Hour

// sessionKeyLabel is the message the signing key is derived from — HMAC(install token,
// "acornd-session-v1"). The version lives in it so a change to the payload is a new key rather than a
// rewrite of every cookie already in a browser.
const sessionKeyLabel = "acornd-session-v1"

// The two roles a cookie can carry, as the payload's first field. A letter rather than a bool because
// the payload's first half is read as text, and it is the role — not the address — that decides what
// the door answers.
const (
	sessionAdmin  = "a"
	sessionMember = "m"
)

// The refusals. Each is a sentence the person reads at a console they are not signed in to, in the
// same register as the token door's: a refusal that does not say what to do is a support case.
var (
	// ErrBadSession is a cookie this install did not sign, or one edited in the browser.
	ErrBadSession = errors.New("this browser's session cookie is not one this install signed: sign in again")
	// ErrExpiredSession is a cookie that was real and is now too old.
	ErrExpiredSession = errors.New("this browser's session has expired: sign in again")
)

// Session proves a person from the cookie the claim minted. The zero value is not usable: Dir is the
// install folder the signing key comes from.
type Session struct {
	// Dir is the install folder. The token file in it is read on every request, so a rotated token
	// stops the cookies it signed at once rather than at the next boot.
	Dir string
	// Now is the clock, so a test can watch a cookie expire without waiting a month. Nil means time.Now.
	Now func() time.Time
}

// Mint is the cookie a sign-in hands back: the address, the role, and when it stops being valid, all
// inside the signature so a browser cannot edit one into another.
//
// Secure is not decided here, because only the request knows whether the browser arrived over TLS;
// the caller sets it from OverTLS.
func (s Session) Mint(email string, admin bool) (*http.Cookie, error) {
	key, err := s.key()
	if err != nil {
		return nil, err
	}
	role := sessionMember
	if admin {
		role = sessionAdmin
	}
	payload := encodePart(role+":"+email) + "." +
		encodePart(strconv.FormatInt(s.now().Add(sessionLifetime).Unix(), 10))
	// #nosec G124 -- Secure is set by the caller from the request, which is the only party that knows whether
	// the browser arrived over TLS (identity.OverTLS); HttpOnly and SameSite are here.
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    payload + "." + encodePart(string(s.sign(key, payload))),
		Path:     "/",
		MaxAge:   int(sessionLifetime / time.Second),
		HttpOnly: true,
		// Lax rather than None: the cookie rides a link into the console and not a form posted from
		// another site, which is the same rule sameSite enforces on the door.
		SameSite: http.SameSiteLaxMode,
	}, nil
}

// Prove answers who this browser is, or refuses with a sentence saying what to do about it.
func (s Session) Prove(r *http.Request) (Caller, error) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil {
		return Caller{}, fmt.Errorf("no %s cookie on this request: sign in with this install's token", SessionCookie)
	}
	return s.verify(cookie.Value)
}

// verify reads and checks the cookie's value.
func (s Session) verify(value string) (Caller, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Caller{}, ErrBadSession
	}
	key, err := s.key()
	if err != nil {
		return Caller{}, err
	}
	signed, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Caller{}, ErrBadSession
	}
	// hmac.Equal rather than ==: the third field is a secret, and comparing it byte by byte with an
	// early exit is a signature oracle.
	if !hmac.Equal(signed, s.sign(key, parts[0]+"."+parts[1])) {
		return Caller{}, ErrBadSession
	}
	role, email, expiry, err := payloadOf(parts[0], parts[1])
	if err != nil {
		return Caller{}, err
	}
	if !s.now().Before(expiry) {
		return Caller{}, ErrExpiredSession
	}
	return callerOfRole(role, email)
}

// payloadOf decodes the two signed fields: the role with the address, and when the cookie stops proving
// anybody. A value this build did not write is refused rather than guessed at.
func payloadOf(subject, expiry string) (role, email string, at time.Time, err error) {
	decoded, err := base64.RawURLEncoding.DecodeString(subject)
	if err != nil {
		return "", "", time.Time{}, ErrBadSession
	}
	role, email, found := strings.Cut(string(decoded), ":")
	if !found {
		return "", "", time.Time{}, ErrBadSession
	}
	raw, err := base64.RawURLEncoding.DecodeString(expiry)
	if err != nil {
		return "", "", time.Time{}, ErrBadSession
	}
	unix, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return "", "", time.Time{}, ErrBadSession
	}
	return role, email, time.Unix(unix, 0), nil
}

// callerOfRole is what a signed role means to the door. An unknown role is a cookie from a version this
// build is not, and reading it as a member would quietly grant less than the cookie promised.
func callerOfRole(role, email string) (Caller, error) {
	switch role {
	case sessionAdmin:
		return Caller{Admin: true, Email: email}, nil
	case sessionMember:
		return Caller{Email: email}, nil
	}
	return Caller{}, ErrBadSession
}

// sign is the MAC over the two fields the payload carries: the address with its role, and the
// expiry.
func (s Session) sign(key []byte, payload string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// key derives the signing key from the install token, so nothing new is stored and a rotation is a
// sign-out everywhere.
func (s Session) key() ([]byte, error) {
	token, err := ReadToken(s.Dir)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(sessionKeyLabel))
	return mac.Sum(nil), nil
}

func (s Session) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ClearSession is the cookie that removes a session: the same name, path and flags as a minted one,
// empty and already expired, so a browser drops it rather than keeping a copy under another path.
func ClearSession(r *http.Request) *http.Cookie {
	// #nosec G124 -- Secure comes from the request on the line below; HttpOnly and SameSite are here.
	return &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   OverTLS(r),
	}
}

// OverTLS reports whether the browser reached this run over HTTPS, which is what the cookie's Secure
// attribute is set from. Behind cloudflared the connection here is plain loopback and the proxy is
// the only party that knows, so its X-Forwarded-Proto is read — a client that sets it itself only
// makes its own cookie stricter.
func OverTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	forwarded, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(forwarded), "https")
}

// encodePart is one field of the cookie's value: base64url with no padding, because a dot separates
// the fields and padding is the one character base64 can produce that a URL or a cookie dislikes.
func encodePart(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}
