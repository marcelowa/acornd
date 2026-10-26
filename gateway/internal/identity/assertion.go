// The remote door: the signed assertion the edge attaches to every request it forwards.
//
// It is verified rather than read. Every identity-as-a-header design — IAP's
// X-Goog-Authenticated-User-Email, Cloudflare's Cf-Access-Authenticated-User-Email — puts a plain
// string on the request that anyone who can reach the port can write. Only the assertion is signed
// by the publisher, so only the assertion is evidence (ADR 0032 decision 2).
package identity

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AssertionHeader is where Cloudflare Access puts its assertion. The name is the publisher's, and it
// is the one header this door reads.
const AssertionHeader = "Cf-Access-Jwt-Assertion"

// clockLeeway is how far two clocks may disagree before a valid assertion is refused. A minute is
// less than the shortest lifetime these assertions carry, so it cannot turn an expired one into a
// usable one.
const clockLeeway = 60 * time.Second

// Assertion proves a person from the assertion an edge attaches: Cloudflare Access today, IAP with
// three values changed (ADR 0032 decision 2).
type Assertion struct {
	// Header is the request header the publisher's assertion arrives in.
	Header string
	// Audience is the tag the publisher issues the assertion for, and Issuer is who signed it. Both
	// are checked: the signature alone says "someone at the edge signed this", not "this console was
	// the target", and an assertion minted for another application at the same edge is otherwise a
	// valid way in.
	Audience string
	Issuer   string
	Keys     *Keys
	Now      func() time.Time
}

// Prove answers the person the assertion names.
func (a Assertion) Prove(r *http.Request) (Caller, error) {
	header := a.Header
	if header == "" {
		header = AssertionHeader
	}
	raw := strings.TrimSpace(r.Header.Get(header))
	if raw == "" {
		return Caller{}, fmt.Errorf("no signed identity assertion in the %s header", header)
	}
	claims, err := a.verify(raw)
	if err != nil {
		return Caller{}, err
	}
	email := strings.TrimSpace(claims.Email)
	if email == "" {
		return Caller{}, errors.New("the assertion names no email address")
	}
	// Present-and-false is the only refusal here. Cloudflare signs an assertion only for an address
	// it has verified, and does not carry this claim at all; IAP does, and it is meaningful there.
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		return Caller{}, fmt.Errorf("the assertion says %s is not verified", email)
	}
	return Caller{Email: strings.ToLower(email)}, nil
}

// assertionClaims is the part of the payload this door reads.
type assertionClaims struct {
	Audience      json.RawMessage `json:"aud"`
	Issuer        string          `json:"iss"`
	Expires       int64           `json:"exp"`
	IssuedAt      int64           `json:"iat"`
	NotBefore     int64           `json:"nbf"`
	Email         string          `json:"email"`
	EmailVerified *bool           `json:"email_verified"`
}

// assertionHeader is the part of the header this door reads.
type assertionHeaderClaims struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

// verify checks the signature and then the claims, in that order: a claim read out of an unverified
// payload is what the whole design exists to avoid.
func (a Assertion) verify(raw string) (assertionClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return assertionClaims{}, errors.New("the assertion is not a signed JWT")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return assertionClaims{}, errors.New("the assertion's header is not base64url")
	}
	var header assertionHeaderClaims
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return assertionClaims{}, errors.New("the assertion's header is not JSON")
	}
	// A fixed algorithm, never the one the token asks for: "alg: none" and "alg: HS256 signed with
	// the public key" are the two classic ways a verifier is talked into not verifying.
	if header.Algorithm != "RS256" {
		return assertionClaims{}, fmt.Errorf("the assertion is signed with %q, and only RS256 is accepted", header.Algorithm)
	}
	if a.Keys == nil {
		return assertionClaims{}, errors.New("this console has no signing keys configured for the assertion")
	}
	key, err := a.Keys.Key(header.KeyID)
	if err != nil {
		return assertionClaims{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return assertionClaims{}, errors.New("the assertion's signature is not base64url")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return assertionClaims{}, errors.New("the assertion's signature does not match the key it names")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return assertionClaims{}, errors.New("the assertion's payload is not base64url")
	}
	var claims assertionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return assertionClaims{}, errors.New("the assertion's payload is not JSON")
	}
	if err := a.checkClaims(claims); err != nil {
		return assertionClaims{}, err
	}
	return claims, nil
}

// checkClaims is the part that is the same for every publisher: who issued it, who it was issued
// for, and whether it is in its lifetime.
func (a Assertion) checkClaims(claims assertionClaims) error {
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	if a.Issuer != "" && claims.Issuer != a.Issuer {
		return fmt.Errorf("the assertion was issued by %q, not %q", claims.Issuer, a.Issuer)
	}
	if a.Audience != "" && !audienceContains(claims.Audience, a.Audience) {
		return fmt.Errorf("the assertion was issued for %s, not for this console", audienceText(claims.Audience))
	}
	if claims.Expires == 0 {
		return errors.New("the assertion has no expiry, so it is not one")
	}
	if now.After(time.Unix(claims.Expires, 0).Add(clockLeeway)) {
		return errors.New("the assertion has expired: sign in again")
	}
	// A clock far enough ahead to fail this is a clock that would also accept a replay tomorrow.
	if claims.IssuedAt != 0 && now.Add(clockLeeway).Before(time.Unix(claims.IssuedAt, 0)) {
		return errors.New("the assertion was issued in the future")
	}
	if claims.NotBefore != 0 && now.Add(clockLeeway).Before(time.Unix(claims.NotBefore, 0)) {
		return errors.New("the assertion is not valid yet")
	}
	return nil
}

// audienceContains reads the audience, which is a string in some assertions and a list in others.
func audienceContains(raw json.RawMessage, want string) bool {
	for _, audience := range audiences(raw) {
		if audience == want {
			return true
		}
	}
	return false
}

func audienceText(raw json.RawMessage) string {
	audiences := audiences(raw)
	if len(audiences) == 0 {
		return "nothing"
	}
	return strings.Join(audiences, ", ")
}

func audiences(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	return nil
}
