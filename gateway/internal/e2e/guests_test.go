package e2e

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
	"testing"
	"time"
)

// accessDoor is a publisher the gateway can verify: an RSA key, the JWKS that publishes it, and tokens
// signed with it. It is the access door without Cloudflare, which is what proves a guest at the edge (ADR 0033 §6).
type accessDoor struct {
	url      string
	issuer   string
	audience string
	key      *rsa.PrivateKey
}

const accessKid = "acornfall-test"

func newAccessDoor(t *testing.T) *accessDoor {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	encode := base64.RawURLEncoding
	document := map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": accessKid, "use": "sig", "alg": "RS256",
		"n": encode.EncodeToString(key.N.Bytes()),
		"e": encode.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(document)
	}))
	t.Cleanup(server.Close)
	return &accessDoor{
		url:      server.URL,
		issuer:   "https://test.cloudflareaccess.com",
		audience: "acornfall-test",
		key:      key,
	}
}

// token signs one person's assertion the way Cloudflare Access does: RS256, the audience this console
// expects, the issuer it expects, and an address it verified.
func (d *accessDoor) token(t *testing.T, email string) string {
	t.Helper()
	encode := base64.RawURLEncoding
	part := func(value any) string {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return encode.EncodeToString(payload)
	}
	now := time.Now()
	signing := part(map[string]string{"alg": "RS256", "typ": "JWT", "kid": accessKid}) + "." + part(map[string]any{
		"aud":            d.audience,
		"iss":            d.issuer,
		"exp":            now.Add(5 * time.Minute).Unix(),
		"iat":            now.Add(-time.Minute).Unix(),
		"email":          email,
		"email_verified": true,
	})
	digest := sha256.Sum256([]byte(signing))
	signature, err := rsa.SignPKCS1v15(rand.Reader, d.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signing + "." + encode.EncodeToString(signature)
}

// A guest is a member of one room and not its owner: they speak in the conversations it already has, and the
// routes that change the room or its conversations refuse them. The table is tested in control; this is the
// proof that every handler asks it (ADR 0034 §4).
func TestAGuestSpeaksAndCannotChangeTheRoom(t *testing.T) {
	t.Parallel()
	door := newAccessDoor(t)
	s := start(t,
		withEnv(
			"ACORNFALL_IDENTITY=access",
			"ACORNFALL_IDENTITY_AUDIENCE="+door.audience,
			"ACORNFALL_IDENTITY_ISSUER="+door.issuer,
			"ACORNFALL_IDENTITY_KEYS="+door.url,
		),
		withSetup(func(t *testing.T, s *system) {
			book := `{"people":[
  {"email":"admin@example.com","name":"Admin","role":"admin","state":"active"},
  {"email":"ana@example.com","name":"Ana","role":"member","state":"active","can_create":true}]}`
			if err := os.WriteFile(filepath.Join(s.DataDir, "people.json"), []byte(book), 0o600); err != nil {
				t.Fatalf("write the book: %v", err)
			}
		}),
	)

	admin := s.client()
	admin.identity = door.token(t, "admin@example.com")
	admin.createWorkspace("house")
	if status, body := admin.postStatus("/api/workspaces/house/members", map[string]string{"email": "ana@example.com"}); status != http.StatusOK {
		t.Fatalf("the owner invited the guest: %d %s", status, body)
	}
	conversation := admin.create("house")

	guest := s.client()
	guest.identity = door.token(t, "ana@example.com")
	base := "/api/workspaces/house"
	refused := []struct {
		name string
		path string
		body any
	}{
		{"create a conversation", base + "/conversations", nil},
		{"fork the conversation", base + "/conversations/" + conversation.ID + "/fork", map[string]string{"at": "1"}},
		{"hide the conversation", base + "/conversations/" + conversation.ID + "/hide", map[string]bool{"hidden": true}},
		{"rename the conversation", base + "/conversations/" + conversation.ID + "/rename", map[string]string{"title": "mine now"}},
		{"change the model", base + "/conversations/" + conversation.ID + "/agent", map[string]string{"provider": "faux", "modelId": "faux-1"}},
	}
	for _, test := range refused {
		t.Run(test.name, func(t *testing.T) {
			if status, body := guest.postStatus(test.path, test.body); status != http.StatusForbidden {
				t.Errorf("a guest was allowed to %s: %d %s", test.name, status, body)
			}
		})
	}

	// And the part a guest is for: speaking in the conversation the room already has.
	if status, body := guest.postStatus(base+"/conversations/"+conversation.ID+"/messages", map[string]string{"text": "hello"}); status != http.StatusAccepted {
		t.Errorf("a guest could not speak: %d %s", status, body)
	}
}
