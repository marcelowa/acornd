package identity

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// minted is a session over a temporary install folder, with the token the key is derived from.
func minted(t *testing.T, email string, admin bool) (Session, *http.Cookie) {
	t.Helper()
	dir := t.TempDir()
	if _, err := RotateToken(dir); err != nil {
		t.Fatalf("token: %v", err)
	}
	session := Session{Dir: dir}
	cookie, err := session.Mint(email, admin)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return session, cookie
}

// withCookie is the request a browser sends once it holds one.
func withCookie(cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/api/state", nil)
	r.AddCookie(cookie)
	return r
}

// The cookie answers what it was minted with: the address for a person, and the role that decides what
// the door lets them do. The role is in the signed part because the token's cookie has no address to
// carry it.
func TestASessionCookieAnswersWhoItWasMintedFor(t *testing.T) {
	cases := []struct {
		name  string
		email string
		admin bool
		want  Caller
	}{
		{"the owner", "marcelo@example.com", true, Caller{Admin: true, Email: "marcelo@example.com"}},
		{"a browser holding the token", "", true, Caller{Admin: true}},
		{"somebody who is not the admin", "ana@example.com", false, Caller{Email: "ana@example.com"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			session, cookie := minted(t, test.email, test.admin)
			caller, err := session.Prove(withCookie(cookie))
			if err != nil {
				t.Fatalf("a cookie this install minted was refused: %v", err)
			}
			if caller != test.want {
				t.Errorf("caller = %+v, want %+v", caller, test.want)
			}
		})
	}
}

// A month is the promise, and it ends: at the expiry the cookie stops proving anybody. The clock is a
// seam so the test does not wait thirty days for the answer.
func TestASessionCookieExpires(t *testing.T) {
	dir := t.TempDir()
	if _, err := RotateToken(dir); err != nil {
		t.Fatalf("token: %v", err)
	}
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	session := Session{Dir: dir, Now: func() time.Time { return start }}
	cookie, err := session.Mint("marcelo@example.com", true)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if cookie.MaxAge != int(sessionLifetime/time.Second) {
		t.Errorf("MaxAge = %d, want %d", cookie.MaxAge, int(sessionLifetime/time.Second))
	}

	session.Now = func() time.Time { return start.Add(sessionLifetime - time.Minute) }
	if _, err := session.Prove(withCookie(cookie)); err != nil {
		t.Fatalf("the cookie expired inside its month: %v", err)
	}
	session.Now = func() time.Time { return start.Add(sessionLifetime) }
	if _, err := session.Prove(withCookie(cookie)); !errors.Is(err, ErrExpiredSession) {
		t.Fatalf("err = %v, want ErrExpiredSession", err)
	}
}

// A cookie is a value in a browser, and a browser is a place values get edited. Every field is inside
// the signature, so changing any of them is a refusal rather than a different session.
func TestATamperedSessionCookieIsRefused(t *testing.T) {
	session, cookie := minted(t, "marcelo@example.com", true)
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 {
		t.Fatalf("the cookie is not three parts: %q", cookie.Value)
	}
	cases := []struct {
		name  string
		value string
	}{
		{"the address changed", flipFirst(parts[0]) + "." + parts[1] + "." + parts[2]},
		{"the expiry moved", parts[0] + "." + flipFirst(parts[1]) + "." + parts[2]},
		{"the signature changed", parts[0] + "." + parts[1] + "." + flipFirst(parts[2])},
		{"a part removed", parts[0] + "." + parts[1]},
		{"an empty part", parts[0] + "." + parts[1] + "."},
		{"not a signed value at all", "not-a-cookie"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// The session that minted it is the one asked, so a refusal is the edit's and not a second
			// install's key.
			tampered := &http.Cookie{Name: SessionCookie, Value: test.value}
			if _, err := session.Prove(withCookie(tampered)); !errors.Is(err, ErrBadSession) {
				t.Fatalf("err = %v, want ErrBadSession", err)
			}
		})
	}
}

// A cookie signed by another install is not a cookie here: the key comes from the token, so rotating the
// token is a sign-out everywhere, which is the answer to a leaked token.
func TestASessionSignedByAnotherTokenIsRefused(t *testing.T) {
	session, cookie := minted(t, "marcelo@example.com", true)
	if _, err := RotateToken(session.Dir); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := session.Prove(withCookie(cookie)); !errors.Is(err, ErrBadSession) {
		t.Fatalf("err = %v, want ErrBadSession", err)
	}
}

// A valid signature over a role this build does not have is a cookie from another version, and reading it
// as a member would quietly grant less than it promised. It is refused instead.
func TestASessionWithAnUnknownRoleIsRefused(t *testing.T) {
	dir := t.TempDir()
	if _, err := RotateToken(dir); err != nil {
		t.Fatalf("token: %v", err)
	}
	session := Session{Dir: dir}
	key, err := session.key()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	expiry := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	payload := encodePart("x:ana@example.com") + "." + encodePart(expiry)
	value := payload + "." + encodePart(string(session.sign(key, payload)))
	if _, err := session.verify(value); !errors.Is(err, ErrBadSession) {
		t.Fatalf("err = %v, want ErrBadSession", err)
	}
}

// No cookie at all is not a broken cookie: the refusal says to sign in, which is what a person does.
func TestARequestWithNoSessionCookieIsRefused(t *testing.T) {
	session, _ := minted(t, "", true)
	_, err := session.Prove(httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/api/state", nil))
	if err == nil || !strings.Contains(err.Error(), "sign in") {
		t.Fatalf("err = %v, want a refusal that says to sign in", err)
	}
}

// The attributes are the promise ADR 0036 §4 makes: a cookie that a script cannot read, that the browser
// sends on a link and not on another site's form, and that lasts the month the constant says. Secure is
// decided by the request, so it is false until the caller sets it.
func TestASessionCookieCarriesItsAttributes(t *testing.T) {
	_, cookie := minted(t, "marcelo@example.com", true)
	if cookie.Name != SessionCookie {
		t.Errorf("name = %q, want %q", cookie.Name, SessionCookie)
	}
	if cookie.Path != "/" {
		t.Errorf("path = %q, want /", cookie.Path)
	}
	if !cookie.HttpOnly {
		t.Error("the cookie is readable by a script")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.MaxAge != int(sessionLifetime/time.Second) {
		t.Errorf("MaxAge = %d, want %d", cookie.MaxAge, int(sessionLifetime/time.Second))
	}
	if cookie.Secure {
		t.Error("a cookie minted with no request is Secure already")
	}
}

// Secure follows the way the request arrived: TLS here, or the proxy in front saying so. Behind
// cloudflared the connection is plain loopback and the header is the only party that knows.
func TestOverTLS(t *testing.T) {
	cases := []struct {
		name      string
		tls       bool
		forwarded string
		want      bool
	}{
		{"plain http", false, "", false},
		{"tls here", true, "", true},
		{"a proxy that says https", false, "https", true},
		{"a proxy that says https in a list", false, "https, http", true},
		{"a proxy that says http", false, "http", false},
		{"a proxy that says HTTPS", false, "HTTPS", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/api/session", nil)
			if test.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if test.forwarded != "" {
				r.Header.Set("X-Forwarded-Proto", test.forwarded)
			}
			if got := OverTLS(r); got != test.want {
				t.Errorf("OverTLS = %v, want %v", got, test.want)
			}
		})
	}
}

// Signing out is a cookie the browser drops: the same name and path, empty, and already over. It carries
// Secure on the same rule as the cookie it removes, or a browser that kept the secure one would keep
// sending it.
func TestClearSessionIsAnExpiredCookie(t *testing.T) {
	r := httptest.NewRequest(http.MethodDelete, "https://console.example.com/api/session", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	cookie := ClearSession(r)
	if cookie.Name != SessionCookie || cookie.Value != "" {
		t.Errorf("clearing cookie = %q = %q", cookie.Name, cookie.Value)
	}
	if cookie.MaxAge >= 0 {
		t.Errorf("MaxAge = %d, want a negative one", cookie.MaxAge)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" {
		t.Errorf("the clearing cookie does not match the one it removes: %+v", cookie)
	}
}

// flipFirst changes the first character of a base64url field, which is a different value of the same
// length, so the refusal is the signature's and not the decoder's.
func flipFirst(part string) string {
	if part == "" {
		return "A"
	}
	replacement := byte('A')
	if part[0] == 'A' {
		replacement = 'B'
	}
	return string(replacement) + part[1:]
}

// A cookie cannot be minted or read without the install token the key is derived from: an install whose
// token is gone has no sessions, rather than sessions signed by nothing.
func TestASessionNeedsAnInstallToken(t *testing.T) {
	dir := t.TempDir()
	if _, err := (Session{Dir: dir}).Mint("ana@example.com", true); err == nil {
		t.Fatal("a session was minted with no token to derive its key from")
	}
	stray := &http.Cookie{Name: SessionCookie, Value: "a.b.c"}
	if _, err := (Session{Dir: dir}).Prove(withCookie(stray)); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

// payloadOf is what the two signed fields mean: a role with an address, and an expiry. Anything else is a
// cookie this build did not write.
func TestPayloadOfRefusesWhatThisBuildDidNotWrite(t *testing.T) {
	valid := encodePart("a:marcelo@example.com")
	expiry := encodePart("1900000000")
	cases := []struct {
		name    string
		subject string
		expiry  string
	}{
		{"a subject that is not base64url", "!!!", expiry},
		{"a subject with no role", encodePart("marcelo@example.com"), expiry},
		{"an expiry that is not base64url", valid, "!!!"},
		{"an expiry that is not a number", valid, encodePart("next month")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := payloadOf(test.subject, test.expiry); !errors.Is(err, ErrBadSession) {
				t.Fatalf("err = %v, want ErrBadSession", err)
			}
		})
	}
	role, email, at, err := payloadOf(valid, expiry)
	if err != nil {
		t.Fatalf("a payload this build writes was refused: %v", err)
	}
	if role != sessionAdmin || email != "marcelo@example.com" || !at.Equal(time.Unix(1900000000, 0)) {
		t.Errorf("payloadOf = %q, %q, %v", role, email, at)
	}
}

// A signature that is not base64url is a malformed cookie, refused before anything is compared.
func TestASessionWithAMalformedSignatureIsRefused(t *testing.T) {
	dir := t.TempDir()
	if _, err := RotateToken(dir); err != nil {
		t.Fatalf("token: %v", err)
	}
	session := Session{Dir: dir}
	value := encodePart("a:") + "." + encodePart("1900000000") + "." + "!!!"
	if _, err := session.verify(value); !errors.Is(err, ErrBadSession) {
		t.Fatalf("err = %v, want ErrBadSession", err)
	}
}
