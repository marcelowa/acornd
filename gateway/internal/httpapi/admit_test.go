package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/identity"
	"github.com/acornfall/acornfall/gateway/internal/people"
)

// stubDoor is a door that answers by rule: the composition with the routes is what these tests are
// about, not how a proof is obtained.
type stubDoor struct {
	caller identity.Caller
	err    error
}

func (d stubDoor) Prove(*http.Request) (identity.Caller, error) { return d.caller, d.err }

func admitted(t *testing.T, gate Gate) http.Handler {
	t.Helper()
	return New(nil, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}, nil, gate).Handler()
}

// withDoor is the gate with a door and no book: the single owner of ADR 0020 behind a proof.
func withDoor(door identity.Door) Gate { return Gate{Door: door} }

// book is a book of people in a temporary install folder.
func book(t *testing.T) *people.Book {
	t.Helper()
	opened, err := people.Open(filepath.Join(t.TempDir(), people.File))
	if err != nil {
		t.Fatalf("open book: %v", err)
	}
	return opened
}

// A request nothing proved never reaches a handler. The route used here is one that answers without any
// state at all, so the 401 is the door's and not a nil dereference wearing a 500.
func TestARequestNoDoorProvedIsRefused(t *testing.T) {
	handler := admitted(t, withDoor(stubDoor{err: errors.New("no install token in the Authorization header")}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "no install token") {
		t.Errorf("the refusal does not say why: %q", body)
	}
}

// Health is the one route in front of the door: a supervisor and the tunnel read it, and a liveness probe
// that needs a credential turns a working console into a restart loop.
func TestHealthAnswersWithoutAProof(t *testing.T) {
	handler := admitted(t, withDoor(stubDoor{err: errors.New("no install token")}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", recorder.Code)
	}
}

// A proven request reaches the handler it was for. The absent update tier answers 503, which is a handler
// that ran: the door must not be the thing that answers every route.
func TestAProvenRequestReachesItsHandler(t *testing.T) {
	handler := admitted(t, withDoor(stubDoor{caller: identity.Caller{Owner: true}}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", recorder.Code)
	}
}

// No gate is the single owner ADR 0020 describes, and the console answers exactly as it did before there
// was a door. A door that exists but is switched off is how a guard nobody believes in comes back.
func TestNoGateMeansTheConsoleIsOpen(t *testing.T) {
	handler := admitted(t, Gate{})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 from the handler, not a refusal", recorder.Code)
	}
}

// A proof of an address is not permission to use the install. Someone the owner never named is refused
// with 403 — the proof was good, so signing in again would not help — and nothing is written.
func TestAnEmailTheOwnerNeverNamedIsRefused(t *testing.T) {
	opened := book(t)
	handler := admitted(t, Gate{Door: stubDoor{caller: identity.Caller{Email: "stranger@example.com"}}, People: opened})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "no record of") {
		t.Errorf("the refusal does not say what is wrong: %q", body)
	}
	list, err := opened.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("a refused request wrote %v", list)
	}
}

// An invited person becomes active on their first real request, which is the moment their personal
// workspace is created (ADR 0032 §3). One request is one visit, and the visit is recorded.
func TestAnInvitedPersonIsActiveAfterTheirFirstRequest(t *testing.T) {
	opened := book(t)
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	handler := admitted(t, Gate{Door: stubDoor{caller: identity.Caller{Email: "ana@example.com"}}, People: opened})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want the client the handler serves", recorder.Code)
	}
	list, err := opened.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if list[0].State != people.Active {
		t.Errorf("state = %q, want active", list[0].State)
	}
}

// Someone the owner removed is refused from their next request on, with the reason rather than a 401: a
// door that says "sign in" to a person who was revoked sends them round a loop they cannot leave.
func TestARevokedPersonIsRefused(t *testing.T) {
	opened := book(t)
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := opened.Revoke("ana@example.com"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	handler := admitted(t, Gate{Door: stubDoor{caller: identity.Caller{Email: "ana@example.com"}}, People: opened})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "no longer has access") {
		t.Errorf("the refusal does not say what happened: %q", body)
	}
}

// The owner is proven by the token, and needs no record to be let in: an install with a door and no owner
// claimed must still be reachable from the machine it runs on, or there is no way to claim it.
func TestTheOwnerNeedsNoRecord(t *testing.T) {
	handler := admitted(t, Gate{Door: stubDoor{caller: identity.Caller{Owner: true}}, People: book(t)})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", recorder.Code)
	}
}

// The console sets no cookie of ours, so SameSite protects nothing: the edge's cookie authenticates a
// cross-site form POST perfectly well. Without this check the door opens for whoever wrote the page.
func TestACrossSitePostIsRefused(t *testing.T) {
	handler := admitted(t, withDoor(stubDoor{caller: identity.Caller{Owner: true}}))
	request := httptest.NewRequest(http.MethodPost, "http://console.example.com/api/update/check", strings.NewReader("{}"))
	request.Header.Set("Origin", "https://evil.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

// The page's own fetch sends an Origin that matches the host it was served from, and a check that refuses
// those is a console nobody can use.
func TestASameSitePostIsAllowed(t *testing.T) {
	handler := admitted(t, withDoor(stubDoor{caller: identity.Caller{Owner: true}}))
	request := httptest.NewRequest(http.MethodPost, "http://console.example.com/api/update/check", strings.NewReader("{}"))
	request.Header.Set("Origin", "http://console.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", recorder.Code)
	}
}

// A command line sends no Origin, and it is not the party this is aimed at. Refusing it would mean a door
// only a browser could open.
func TestARequestWithNoOriginIsAllowed(t *testing.T) {
	handler := admitted(t, withDoor(stubDoor{caller: identity.Caller{Owner: true}}))
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
	handler := admitted(t, withDoor(stubDoor{caller: identity.Caller{Owner: true}}))
	request := httptest.NewRequest(http.MethodGet, "http://console.example.com/api/update/status", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", recorder.Code)
	}
}

// The install's own routes — its workspaces' lifecycle, its credentials, its updates — are the owner's.
// A person gets 403 rather than the 404 a workspace answers: these routes are not about a workspace they
// might be a member of, they are about an install they do not own.
func TestAnInstallRouteIsTheOwnersOnly(t *testing.T) {
	opened := book(t)
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	cases := []struct {
		name   string
		caller identity.Caller
		want   int
	}{
		{"a person", identity.Caller{Email: "ana@example.com"}, http.StatusForbidden},
		// The owner passes the check and reaches the handler, which answers 503 because this build has no
		// update tier.
		{"the owner", identity.Caller{Owner: true}, http.StatusServiceUnavailable},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			handler := admitted(t, Gate{Door: stubDoor{caller: test.caller}, People: opened})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/update/check", strings.NewReader("{}")))
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d (%s)", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

// A person may run the models the owner gave them and no others, and the refusal says which ones those are:
// the person is family, and "forbidden" is not a sentence anybody can act on (ADR 0032 §6).
func TestAModelOutsideTheAllowlistIsRefusedOnAssignment(t *testing.T) {
	opened := book(t)
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := opened.SetModels("ana@example.com", []string{"faux/faux-1"}); err != nil {
		t.Fatalf("set models: %v", err)
	}

	server := New(nil, nil, nil, Gate{People: opened})
	as := func(email string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/agent", nil)
		return r.WithContext(withCaller(r.Context(), control.Caller{Email: email}))
	}

	if recorder := httptest.NewRecorder(); !server.mayRun(recorder, as("ana@example.com"), "faux", "faux-1") {
		t.Fatalf("a listed model was refused: %s", recorder.Body.String())
	}
	recorder := httptest.NewRecorder()
	if server.mayRun(recorder, as("ana@example.com"), "faux", "faux-2") {
		t.Fatal("a model outside the allowlist was allowed")
	}
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", recorder.Code)
	}
	body := recorder.Body.String()
	// The sentence names the models the person may run, in the form they pick them, and says which list is which:
	// the install's for the provider, then theirs on top.
	if !strings.Contains(body, "faux/faux-1") || !strings.Contains(body, "for you") {
		t.Errorf("the refusal does not say what they may run: %q", body)
	}

	// The owner has no list, and neither does an install with nobody in it.
	owner := httptest.NewRequest(http.MethodPost, "/api/agent", nil)
	owner = owner.WithContext(withCaller(owner.Context(), control.Caller{Owner: true}))
	if recorder := httptest.NewRecorder(); !server.mayRun(recorder, owner, "faux", "anything") {
		t.Error("the owner was refused a model")
	}
	if recorder := httptest.NewRecorder(); !server.mayRun(recorder, as("ana@example.com"), "", "") {
		t.Error("a request that named no model was checked against the list")
	}
}

// The owner arrives through the edge like everybody else: with a door in front, a browser proves an address
// and the book says what that address may do. The token proves ownership by itself, and that is the only
// caller it ever describes — a build where the book is never asked is an install whose owner is a person
// behind their own console, refused with a sentence telling them only its owner may do it.
func TestTheOwnersAddressMayDoTheInstallsBusiness(t *testing.T) {
	opened := book(t)
	if _, err := opened.Claim("marcelo@example.com", "Marcelo"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	handler := admitted(t, Gate{Door: stubDoor{caller: identity.Caller{Email: "marcelo@example.com"}}, People: opened})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if recorder.Code == http.StatusForbidden {
		t.Fatalf("the owner was refused their own install: %q", recorder.Body.String())
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", recorder.Code)
	}

	// Anybody else, proven by the same door, is refused there — the difference is the book, not the door.
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	other := admitted(t, Gate{Door: stubDoor{caller: identity.Caller{Email: "ana@example.com"}}, People: opened})
	recorder = httptest.NewRecorder()
	other.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("ana reached the install's business: %d", recorder.Code)
	}
}
