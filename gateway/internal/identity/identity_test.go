package identity

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A door that answers by rule, so the composition can be tested without a file or a signature.
type stubDoor struct {
	caller Caller
	err    error
}

func (d stubDoor) Prove(*http.Request) (Caller, error) { return d.caller, d.err }

// Any admits when one door proves, and the order is the order it was given: the token door is first
// because it is a local compare and the assertion door may fetch keys.
func TestAnyAdmitsTheFirstDoorThatProves(t *testing.T) {
	door := Any(
		stubDoor{err: errors.New("no install token")},
		stubDoor{caller: Caller{Email: "ana@example.com"}},
	)
	caller, err := door.Prove(httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if err != nil {
		t.Fatalf("no door proved a request one door can: %v", err)
	}
	if caller.Email != "ana@example.com" {
		t.Errorf("caller = %+v, want the second door's", caller)
	}
}

// One refusal is not enough information: "denied" on a console reached through a tunnel with no
// assertion is the sentence that tells the admin the door is misconfigured, and dropping the reasons
// is how that becomes a support thread.
func TestAnyReportsEveryDoorsReason(t *testing.T) {
	door := Any(
		stubDoor{err: errors.New("no install token in the Authorization header")},
		stubDoor{err: errors.New("no signed identity assertion in the Cf-Access-Jwt-Assertion header")},
	)
	_, err := door.Prove(httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if !errors.Is(err, ErrNoProof) {
		t.Fatalf("err = %v, want ErrNoProof", err)
	}
	for _, want := range []string{"Authorization header", "Cf-Access-Jwt-Assertion"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// No doors at all is not "admit everyone": an empty Any is a misconfiguration, and the safe reading of
// a misconfiguration is a refusal.
func TestAnyWithNoDoorsRefuses(t *testing.T) {
	if _, err := Any().Prove(httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, ErrNoProof) {
		t.Fatalf("err = %v, want ErrNoProof", err)
	}
}
