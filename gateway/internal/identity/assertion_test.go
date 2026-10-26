package identity

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
	"strings"
	"testing"
	"time"
)

const (
	testIssuer   = "https://acornfall.cloudflareaccess.com"
	testAudience = "0123456789abcdef"
	testEmail    = "ana@example.com"
)

// signer is a publisher: one RSA key, its key id, and the assertions it signs.
type signer struct {
	key *rsa.PrivateKey
	kid string
}

func newSigner(t *testing.T, kid string) signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return signer{key: key, kid: kid}
}

// jwks is what the publisher serves: the public half, in the shape every verifier reads.
func (s signer) jwks() []byte {
	exponent := big.NewInt(int64(s.key.PublicKey.E)).Bytes()
	document := map[string]any{"keys": []map[string]any{{
		"kid": s.kid,
		"kty": "RSA",
		"alg": "RS256",
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(s.key.PublicKey.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(exponent),
	}}}
	encoded, _ := json.Marshal(document)
	return encoded
}

// assertion signs a payload. The header's algorithm is a parameter because one test signs a token that
// asks for "none", which is the attack the verifier exists to refuse.
func (s signer) assertion(t *testing.T, algorithm string, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": algorithm, "kid": s.kid})
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	if algorithm != "RS256" {
		return signing + "."
	}
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// served is a publisher answering JWKS over HTTP, which is what the door fetches.
func served(t *testing.T, s signer) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write(s.jwks())
	}))
	t.Cleanup(server.Close)
	return server
}

// claims is a valid assertion's payload, with one field replaced per test.
func claims(overrides map[string]any) map[string]any {
	base := map[string]any{
		"aud":   testAudience,
		"iss":   testIssuer,
		"email": testEmail,
		"exp":   time.Now().Add(15 * time.Minute).Unix(),
		"iat":   time.Now().Add(-time.Minute).Unix(),
	}
	for key, value := range overrides {
		base[key] = value
	}
	return base
}

// door is the real door pointed at a test publisher. Nothing is faked but the keys and the clock.
func door(server *httptest.Server) Assertion {
	return Assertion{
		Audience: testAudience,
		Issuer:   testIssuer,
		Keys:     &Keys{URL: server.URL},
	}
}

func withAssertion(raw string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/api/state", nil)
	r.Header.Set(AssertionHeader, raw)
	return r
}

func TestAValidAssertionNamesItsEmail(t *testing.T) {
	s := newSigner(t, "k1")
	server := served(t, s)
	caller, err := door(server).Prove(withAssertion(s.assertion(t, "RS256", claims(nil))))
	if err != nil {
		t.Fatalf("a valid assertion was refused: %v", err)
	}
	if caller.Email != testEmail {
		t.Errorf("email = %q, want %q", caller.Email, testEmail)
	}
	if caller.Owner {
		t.Error("a person from the edge is not the install owner")
	}
}

// The header every one of these designs adds is a plain string. If it were enough, the signature, the
// audience, and the whole door would be decoration.
func TestThePlaintextIdentityHeaderIsNotEnough(t *testing.T) {
	s := newSigner(t, "k1")
	server := served(t, s)
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7777/api/state", nil)
	r.Header.Set("Cf-Access-Authenticated-User-Email", testEmail)
	if _, err := door(server).Prove(r); err == nil {
		t.Fatal("an unsigned identity header was accepted")
	}
}

func TestTheRefusals(t *testing.T) {
	s := newSigner(t, "k1")
	other := newSigner(t, "k1")
	server := served(t, s)
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "wrong audience", want: "not for this console",
			raw: s.assertion(t, "RS256", claims(map[string]any{"aud": "someone-elses-app"})),
		},
		{
			name: "wrong issuer", want: "was issued by",
			raw: s.assertion(t, "RS256", claims(map[string]any{"iss": "https://evil.example.com"})),
		},
		{
			name: "expired", want: "expired",
			raw: s.assertion(t, "RS256", claims(map[string]any{"exp": time.Now().Add(-2 * time.Minute).Unix()})),
		},
		{
			name: "issued in the future", want: "in the future",
			raw: s.assertion(t, "RS256", claims(map[string]any{"iat": time.Now().Add(time.Hour).Unix()})),
		},
		{
			name: "no expiry", want: "no expiry",
			raw: s.assertion(t, "RS256", claims(map[string]any{"exp": int64(0)})),
		},
		{
			name: "not signed at all", want: "RS256",
			raw: s.assertion(t, "none", claims(nil)),
		},
		{
			name: "signed by another key", want: "signature",
			raw: other.assertion(t, "RS256", claims(nil)),
		},
		{
			name: "not a jwt", want: "not a signed JWT",
			raw: "hello",
		},
		{
			name: "unverified email", want: "not verified",
			raw: s.assertion(t, "RS256", claims(map[string]any{"email_verified": false})),
		},
		{
			name: "no email", want: "names no email",
			raw: s.assertion(t, "RS256", claims(map[string]any{"email": ""})),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := door(server).Prove(withAssertion(test.raw))
			if err == nil {
				t.Fatalf("the assertion was accepted")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("err = %q, want it to mention %q", err, test.want)
			}
		})
	}
}

// A list-valued audience is what the spec allows, and an assertion that names this console among
// others is one for this console.
func TestAudienceMayBeAList(t *testing.T) {
	s := newSigner(t, "k1")
	server := served(t, s)
	raw := s.assertion(t, "RS256", claims(map[string]any{"aud": []string{"another-app", testAudience}}))
	if _, err := door(server).Prove(withAssertion(raw)); err != nil {
		t.Fatalf("an audience list naming this console was refused: %v", err)
	}
}

// With nothing cached there is nothing to check the signature against, so the request is refused and
// the refusal says which publisher could not be reached. Admitting it would be admitting a signature
// nobody verified.
func TestKeysThatCannotBeFetchedFailClosed(t *testing.T) {
	s := newSigner(t, "k1")
	server := served(t, s)
	url := server.URL
	server.Close()
	d := Assertion{Audience: testAudience, Issuer: testIssuer, Keys: &Keys{URL: url}}
	_, err := d.Prove(withAssertion(s.assertion(t, "RS256", claims(nil))))
	if err == nil {
		t.Fatal("an assertion was accepted with no keys")
	}
	if !strings.Contains(err.Error(), "signing keys") {
		t.Errorf("err = %q, want it to say the keys could not be read", err)
	}
}

// A publisher that is briefly unreachable must not lock the household out: a key this process has
// already fetched still verifies a signature it can still check.
func TestAKnownKeySurvivesAPublisherOutage(t *testing.T) {
	s := newSigner(t, "k1")
	server := served(t, s)
	now := time.Now()
	keys := &Keys{URL: server.URL, Now: func() time.Time { return now }}
	d := Assertion{Audience: testAudience, Issuer: testIssuer, Keys: keys}
	raw := s.assertion(t, "RS256", claims(map[string]any{"exp": now.Add(time.Hour).Unix()}))
	if _, err := d.Prove(withAssertion(raw)); err != nil {
		t.Fatalf("the first assertion was refused: %v", err)
	}

	server.Close()
	now = now.Add(2 * time.Hour) // past the cache's own lifetime, so it tries to fetch again
	raw = s.assertion(t, "RS256", claims(map[string]any{"exp": now.Add(time.Hour).Unix()}))
	if _, err := d.Prove(withAssertion(raw)); err != nil {
		t.Fatalf("a cached key was not used during an outage: %v", err)
	}
}

// Rotation is the case the cache must not break: an assertion names a key this process has not seen,
// so it fetches again instead of refusing for an hour.
func TestAnUnknownKeyIDRefetches(t *testing.T) {
	first := newSigner(t, "k1")
	second := newSigner(t, "k2")
	current := first.jwks()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(current)
	}))
	defer server.Close()

	d := Assertion{Audience: testAudience, Issuer: testIssuer, Keys: &Keys{URL: server.URL}}
	if _, err := d.Prove(withAssertion(first.assertion(t, "RS256", claims(nil)))); err != nil {
		t.Fatalf("the first key's assertion was refused: %v", err)
	}
	current = second.jwks()
	if _, err := d.Prove(withAssertion(second.assertion(t, "RS256", claims(nil)))); err != nil {
		t.Fatalf("a rotated key was not fetched: %v", err)
	}
}

// A key set that is not a key set is reported as that, rather than as a signature failure: the two have
// different fixes, and one of them is not the owner's.
func TestAKeySetThatIsNotOneIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer server.Close()
	s := newSigner(t, "k1")
	d := Assertion{Audience: testAudience, Issuer: testIssuer, Keys: &Keys{URL: server.URL}}
	_, err := d.Prove(withAssertion(s.assertion(t, "RS256", claims(nil))))
	if err == nil || !strings.Contains(err.Error(), "no RSA signing key") {
		t.Fatalf("err = %v, want the empty key set reported", err)
	}
}

// A door with no keys configured is a misconfiguration, and it says so instead of failing on a nil
// dereference in a request path.
func TestADoorWithNoKeysSaysSo(t *testing.T) {
	s := newSigner(t, "k1")
	d := Assertion{Audience: testAudience, Issuer: testIssuer}
	_, err := d.Prove(withAssertion(s.assertion(t, "RS256", claims(nil))))
	if err == nil || !strings.Contains(err.Error(), "no signing keys") {
		t.Fatalf("err = %v, want it to name the missing configuration", err)
	}
}
