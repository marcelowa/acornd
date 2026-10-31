package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/identity"
	"github.com/acornfall/acornfall/gateway/internal/people"
)

// allowlisted is a console whose handler serves the three allowlist routes behind the door.
func allowlisted(t *testing.T, callerIs identity.Caller) (http.Handler, *people.Book, string) {
	t.Helper()
	dir := t.TempDir()
	opened, err := people.Open(dir + "/" + people.File)
	if err != nil {
		t.Fatalf("open book: %v", err)
	}
	if _, err := opened.Claim("admin@example.com", "Admin"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	server := &Server{gate: Gate{Door: stubDoor{caller: callerIs}, People: opened, Dir: dir}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/providers/allowlist", server.admin(server.providerAllowlist))
	mux.HandleFunc("PUT /api/providers/allowlist", server.admin(server.setProviderAllowlist))
	// The door puts the caller in the context in front of every route; a request that skips it would be the default
	// admin ADR 0020 describes, which is the one thing these tests must not silently become.
	proven := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(withCaller(r.Context(), control.Caller{Admin: callerIs.Admin, Email: callerIs.Email})))
	})
	return proven, opened, dir
}

// The admin sets what a provider's key may be spent on, and reads it back: empty means every model it has, so the
// only way to say "none" is to say so with a list that leaves nothing out.
func TestTheAdminSetsWhatAProviderMaySpendOn(t *testing.T) {
	handler, _, dir := allowlisted(t, identity.Caller{Admin: true})

	body := strings.NewReader(`{"provider":"openai","models":["gpt-5","gpt-5-mini"]}`)
	request := httptest.NewRequest(http.MethodPut, "/api/providers/allowlist", body)
	request.Header.Set("content-type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	allowlist, err := control.InstallAllowlist(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := allowlist["openai"]; len(got) != 2 || got[0] != "gpt-5" {
		t.Errorf("the list is %v", got)
	}

	// Clearing it is the way back to "every model it has".
	body = strings.NewReader(`{"provider":"openai","models":[]}`)
	request = httptest.NewRequest(http.MethodPut, "/api/providers/allowlist", body)
	request.Header.Set("content-type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	allowlist, _ = control.InstallAllowlist(dir)
	if _, present := allowlist["openai"]; present {
		t.Errorf("clearing left an entry: %v", allowlist)
	}
}

// Reading it is the admin's too: the list says what the install spends its keys on.
func TestAPersonCannotReadTheAllowlist(t *testing.T) {
	handler, _, _ := allowlisted(t, identity.Caller{Email: "ana@example.com"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/providers/allowlist", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

// The list binds the admin as well: a whitelist with a hole for its author is one nobody has tested.
func TestTheAllowlistBindsTheAdmin(t *testing.T) {
	_, opened, dir := allowlisted(t, identity.Caller{Admin: true})
	if _, err := control.SetProviderModels(dir, "openai", []string{"gpt-5"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	server := &Server{gate: Gate{People: opened, Dir: dir}}
	state := server.offerable(stateWith(), control.Caller{Admin: true, Email: "admin@example.com"})
	// stateWith offers openai/cheap and openai/expensive; neither is gpt-5, and the install has allowed no other
	// provider, so the admin is offered nothing. The list binds the owner too.
	if got := offered(state); len(got) != 0 {
		t.Errorf("the admin was offered %v", got)
	}
}

// A denial names the provider and what it may use, because that is the sentence a person can act on: ask the admin,
// or pick one of these.
func TestARefusalNamesWhatTheProviderAllows(t *testing.T) {
	_, opened, dir := allowlisted(t, identity.Caller{Admin: true})
	if _, err := control.SetProviderModels(dir, "openai", []string{"gpt-5"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	server := &Server{gate: Gate{People: opened, Dir: dir}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/agents", nil)
	if server.mayRun(recorder, request, "openai", "gpt-5-pro") {
		t.Fatal("a model outside the allowlist was allowed")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "gpt-5") {
		t.Errorf("the refusal does not name what is allowed: %q", body)
	}
}

// The catalog is unfiltered on purpose: narrowing it by the list would make the list impossible to widen from the
// screen that writes it.
func TestTheCatalogIsNotNarrowed(t *testing.T) {
	handler, _, dir := allowlisted(t, identity.Caller{Admin: true})
	if _, err := control.SetProviderModels(dir, "openai", []string{"gpt-5"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// The catalog asks control, which needs a workspace: with none, it refuses rather than narrowing silently.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/providers/allowlist", nil))
	var answer struct {
		Providers map[string][]string `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(answer.Providers["openai"]) != 1 {
		t.Errorf("the allowlist reads %v", answer.Providers)
	}
}

// The check that matters most: a conversation already pinned to a model that has since been taken off the list is
// refused on its next turn. Without this, revoking a model only takes effect the next time somebody re-pins, and a
// person keeps spending a model they no longer have.
func TestATurnPinnedToARevokedModelIsRefused(t *testing.T) {
	_, opened, dir := allowlisted(t, identity.Caller{Admin: true})
	if _, err := control.SetProviderModels(dir, "openai", []string{"gpt-5"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	server := &Server{gate: Gate{People: opened, Dir: dir}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/workspaces/personal/conversations/1/messages", nil)
	if server.mayRun(recorder, request, "openai", "gpt-5-pro") {
		t.Fatal("a revoked model was allowed on a turn")
	}
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", recorder.Code)
	}
}
