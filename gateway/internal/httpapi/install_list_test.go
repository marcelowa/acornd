package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/identity"
)

// A turn is checked against the install's list and not the caller's: a person's list governs what they choose, and a
// guest runs the model the room was given by the member who pinned it. Without this, a guest whose own list is
// narrower than the room's model could not say a word in the room they were invited to (ADR 0034 §4).
func TestAGuestsListDoesNotBindARoomTheyWereInvitedTo(t *testing.T) {
	_, opened, dir := allowlisted(t, identity.Caller{Email: "ana@example.com"})
	if _, err := opened.SetModels("ana@example.com", []string{"anthropic/fast"}); err != nil {
		t.Fatalf("set models: %v", err)
	}
	server := &Server{gate: Gate{People: opened, Dir: dir}}
	asAna := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/api/messages", nil)
		return request.WithContext(withCaller(context.Background(), control.Caller{Email: "ana@example.com"}))
	}
	if recorder := httptest.NewRecorder(); !server.mayRunInstall(recorder, asAna(), "openai", "gpt-5-pro") {
		t.Fatalf("the guest's own list bound the room's model: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := control.SetProviderModels(dir, "openai", []string{"gpt-5"}); err != nil {
		t.Fatalf("set provider models: %v", err)
	}
	recorder := httptest.NewRecorder()
	if server.mayRunInstall(recorder, asAna(), "openai", "gpt-5-pro") {
		t.Fatal("the install's list did not bind the turn")
	}
	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", recorder.Code)
	}
}
