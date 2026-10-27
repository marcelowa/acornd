// Signing keys for the remote door: fetched from the publisher once, cached, and refreshed when an
// assertion names a key this process has not seen.
package identity

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"
)

// keyTTL is how long a fetched key set is trusted before it is fetched again. It is an hour because
// the alternative is a fetch per request, and a publisher rotates keys on the order of weeks — an
// unknown key id is what triggers the refresh that matters, not the clock.
const keyTTL = time.Hour

// keysBodyLimit bounds what a keys endpoint may answer with: a key set is a few kilobytes, and a
// response that streams forever is a denial of service with a valid certificate.
const keysBodyLimit = 1 << 20

// Keys is one publisher's signing keys.
//
// The zero value is not usable: URL is required. Client is optional and defaults to a client with a
// ten-second timeout, because a key fetch that hangs holds every unproven request with it.
type Keys struct {
	URL    string
	Client *http.Client
	Now    func() time.Time

	mu      sync.Mutex
	set     map[string]*rsa.PublicKey
	fetched time.Time
}

// Key answers the public key for a key id, fetching the set when it has none or has not fetched
// recently.
//
// A failed fetch falls back to a key it already holds: a publisher that is briefly unreachable must
// not lock the household out of its own console, and refusing a *known* key is refusing a signature
// this process could still check. With nothing cached there is nothing to fall back to, and the
// request is refused with the reason.
func (k *Keys) Key(kid string) (*rsa.PublicKey, error) {
	k.mu.Lock()
	key, known := k.set[kid]
	// A zero fetched time is a set that has never been fetched, which is never fresh: that is what
	// makes the zero value of this struct safe to use rather than a trap.
	fresh := !k.fetched.IsZero() && k.now().Sub(k.fetched) < keyTTL
	k.mu.Unlock()
	if known && fresh {
		return key, nil
	}

	fetched, err := k.fetch()
	if err != nil {
		k.mu.Lock()
		fallback, have := k.set[kid]
		k.mu.Unlock()
		if have {
			return fallback, nil
		}
		return nil, err
	}
	k.mu.Lock()
	k.set, k.fetched = fetched, k.now()
	k.mu.Unlock()
	key, ok := fetched[kid]
	if !ok {
		return nil, fmt.Errorf("the assertion names key %q, which %s does not publish", kid, k.URL)
	}
	return key, nil
}

// fetch reads one key set. It is called without the mutex held: a lock held across a network read is
// a lock held for as long as someone else's server takes to answer (G4).
func (k *Keys) fetch() (map[string]*rsa.PublicKey, error) {
	if k.URL == "" {
		return nil, errors.New("no key URL: the remote door needs to know where the assertions are signed")
	}
	client := k.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Get(k.URL)
	if err != nil {
		return nil, fmt.Errorf("fetching signing keys from %s: %w", k.URL, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching signing keys from %s: %s", k.URL, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, keysBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("reading signing keys from %s: %w", k.URL, err)
	}
	return parseKeys(body, k.URL)
}

func (k *Keys) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}

// jwks is the part of a JSON Web Key Set this door uses: RSA signing keys with an id. Everything else
// in the document — other key types, other uses — is skipped rather than refused, because a publisher
// may legitimately publish more than this parser understands.
type jwks struct {
	Keys []struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func parseKeys(body []byte, source string) (map[string]*rsa.PublicKey, error) {
	var document jwks
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, fmt.Errorf("reading signing keys from %s: %w", source, err)
	}
	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, key := range document.Keys {
		if key.Kty != "RSA" || (key.Use != "" && key.Use != "sig") {
			continue
		}
		if public, ok := publicKey(key.N, key.E); ok {
			keys[key.Kid] = public
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("signing keys from %s: no RSA signing key with an id", source)
	}
	return keys, nil
}

// publicKey builds one RSA key from its modulus and exponent, base64url-encoded as a JWKS carries them.
// A key this build cannot read is skipped rather than refused: a publisher may legitimately publish more
// than one algorithm, and the refusal belongs to the key id the assertion names.
func publicKey(modulus, exponent string) (*rsa.PublicKey, bool) {
	n, err := base64.RawURLEncoding.DecodeString(modulus)
	if err != nil || modulus == "" {
		return nil, false
	}
	e, err := base64.RawURLEncoding.DecodeString(exponent)
	if err != nil || exponent == "" {
		return nil, false
	}
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(n),
		E: int(new(big.Int).SetBytes(e).Int64()),
	}, true
}
