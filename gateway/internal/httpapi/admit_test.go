package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/acornfall/acornfall/gateway/internal/identity"
)

// stubDoor is a door that answers by rule: the composition with the routes is what these tests are
// about, not how a proof is obtained.
type stubDoor struct {
	caller identity.Caller
	err    error
}

func (d stubDoor) Prove(*http.Request) (identity.Caller, error) { return d.caller, d.err }

func admitted(t *testing.T, door identity.Door) http.Handler {
	t.Helper()
	return New(nil, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}, nil, door).Handler()
}

// A request nothing proved never reaches a handler. The route used here is one that answers without
// any state at all, so the 401 is the door's and not a nil dereference wearing a 500.
func TestARequestNoDoorProvedIsRefused(t *testing.T) {
	handler := admitted(t, stubDoor{err: errors.New("no install token in the Authorization header")})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "no install token") {
		t.Errorf("the refusal does not say why: %q", body)
	}
}

// Health is the one route in front of the door: a supervisor and the tunnel read it, and a liveness
// probe that needs a credential turns a working console into a restart loop.
func TestHealthAnswersWithoutAProof(t *testing.T) {
	handler := admitted(t, stubDoor{err: errors.New("no install token")})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", recorder.Code)
	}
}

// A proven request reaches the handler it was for. The absent update tier answers 503, which is a
// handler that ran: the door must not be the thing that answers every route.
func TestAProvenRequestReachesItsHandler(t *testing.T) {
	handler := admitted(t, stubDoor{caller: identity.Caller{Owner: true}})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", recorder.Code)
	}
}

// No door is the single owner ADR 0020 describes, and the console answers exactly as it did before
// there was a door. A door that exists but is switched off is how a guard nobody believes in comes
// back.
func TestNoDoorMeansTheConsoleIsOpen(t *testing.T) {
	handler := admitted(t, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 from the handler, not a refusal", recorder.Code)
	}
}

// The console sets no cookie of ours, so SameSite protects nothing: the edge's cookie authenticates a
// cross-site form POST perfectly well. Without this check the door opens for whoever wrote the page.
func TestACrossSitePostIsRefused(t *testing.T) {
	handler := admitted(t, stubDoor{caller: identity.Caller{Owner: true}})
	request := httptest.NewRequest(http.MethodPost, "http://console.example.com/api/update/check", strings.NewReader("{}"))
	request.Header.Set("Origin", "https://evil.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

// The page's own fetch sends an Origin that matches the host it was served from, and a check that
// refuses those is a console nobody can use.
func TestASameSitePostIsAllowed(t *testing.T) {
	handler := admitted(t, stubDoor{caller: identity.Caller{Owner: true}})
	request := httptest.NewRequest(http.MethodPost, "http://console.example.com/api/update/check", strings.NewReader("{}"))
	request.Header.Set("Origin", "http://console.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", recorder.Code)
	}
}

// A command line sends no Origin, and it is not the party this is aimed at. Refusing it would mean a
// door only a browser could open.
func TestARequestWithNoOriginIsAllowed(t *testing.T) {
	handler := admitted(t, stubDoor{caller: identity.Caller{Owner: true}})
	request := httptest.NewRequest(http.MethodPost, "http://console.example.com/api/update/check", strings.NewReader("{}"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", recorder.Code)
	}
}

// A read is not a change, and the stream and every page load are reads: a cross-site check that covered
// them would break the console's own navigation for no gain.
func TestACrossSiteReadIsAllowed(t *testing.T) {
	handler := admitted(t, stubDoor{caller: identity.Caller{Owner: true}})
	request := httptest.NewRequest(http.MethodGet, "http://console.example.com/api/update/status", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", recorder.Code)
	}
}
