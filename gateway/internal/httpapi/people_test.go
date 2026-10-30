package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/identity"
	"github.com/acornfall/acornfall/gateway/internal/people"
)

// The book is the admin's screen, and every mutation answers with the whole book so the screen never shows a
// state between a write and a read (ADR 0032 §3).
func TestTheAdminManagesTheBook(t *testing.T) {
	opened := book(t)
	if _, err := opened.Claim("me@example.com", "Me"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	handler := admitted(t, Gate{Door: stubDoor{caller: identity.Caller{Admin: true}}, People: opened})

	listed := do(t, handler, http.MethodGet, "/api/people", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "me@example.com") {
		t.Fatalf("the book: %d %s", listed.Code, listed.Body.String())
	}

	added := do(t, handler, http.MethodPost, "/api/people", `{"email":"ana@example.com","name":"Ana"}`)
	if added.Code != http.StatusOK || !strings.Contains(added.Body.String(), "ana@example.com") {
		t.Fatalf("the add: %d %s", added.Code, added.Body.String())
	}
	if !personIn(t, added.Body.Bytes(), "ana@example.com").CanCreate {
		t.Error("a new member cannot make a room")
	}

	narrowed := do(t, handler, http.MethodPost, "/api/people/ana@example.com/can_create", `{"can_create":false}`)
	if narrowed.Code != http.StatusOK || personIn(t, narrowed.Body.Bytes(), "ana@example.com").CanCreate {
		t.Errorf("the narrowing: %d %s", narrowed.Code, narrowed.Body.String())
	}

	modelled := do(t, handler, http.MethodPost, "/api/people/ana@example.com/models", `{"models":["anthropic/fast"]}`)
	if modelled.Code != http.StatusOK || len(personIn(t, modelled.Body.Bytes(), "ana@example.com").Models) != 1 {
		t.Errorf("the models: %d %s", modelled.Code, modelled.Body.String())
	}

	revoked := do(t, handler, http.MethodPost, "/api/people/ana@example.com/revoke", "")
	if revoked.Code != http.StatusOK || personIn(t, revoked.Body.Bytes(), "ana@example.com").State != people.Revoked {
		t.Errorf("the revoke: %d %s", revoked.Code, revoked.Body.String())
	}
}

func personIn(t *testing.T, payload []byte, email string) people.Person {
	t.Helper()
	var answer struct {
		People []people.Person `json:"people"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		t.Fatalf("the answer is not the book: %v", err)
	}
	for _, person := range answer.People {
		if person.Email == email {
			return person
		}
	}
	t.Fatalf("%s is not in the answer: %s", email, payload)
	return people.Person{}
}

// A person asking for the book is refused: the install's list is the admin's (ADR 0032 §5).
func TestAPersonCannotReadTheBook(t *testing.T) {
	opened := book(t)
	if _, err := opened.Claim("me@example.com", "Me"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	handler := admitted(t, Gate{Door: stubDoor{caller: identity.Caller{Email: "ana@example.com"}}, People: opened})
	if refused := do(t, handler, http.MethodGet, "/api/people", ""); refused.Code != http.StatusForbidden {
		t.Errorf("a person read the book: %d %s", refused.Code, refused.Body.String())
	}
}

// `can_create` is the install's answer to "may this person lead a room": the admin always may, a member only
// when the record says so (ADR 0034 §3).
func TestOnlyAMemberWhoseRecordAllowsItMayMakeARoom(t *testing.T) {
	opened := book(t)
	if _, err := opened.Claim("me@example.com", "Me"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := opened.Add("ana@example.com", "Ana"); err != nil {
		t.Fatalf("add: %v", err)
	}
	server := &Server{gate: Gate{People: opened}}
	as := func(caller control.Caller) *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/api/workspaces", nil)
		return request.WithContext(withCaller(context.Background(), caller))
	}
	if recorder := httptest.NewRecorder(); !server.mayMakeRoom(recorder, as(control.Caller{Admin: true, Email: "me@example.com"})) {
		t.Errorf("the admin could not make a room: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := httptest.NewRecorder(); !server.mayMakeRoom(recorder, as(control.Caller{Email: "ana@example.com"})) {
		t.Fatalf("a member who may make rooms was refused: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := opened.SetCanCreate("ana@example.com", false); err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if recorder := httptest.NewRecorder(); server.mayMakeRoom(recorder, as(control.Caller{Email: "ana@example.com"})) {
		t.Error("a member whose record says no made a room")
	} else if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", recorder.Code)
	}
}
