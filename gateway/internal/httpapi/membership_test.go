package httpapi

import (
	"encoding/json"
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

// memberFixture is one install with a book, a room whose owner is the admin, and a personal room that belongs to
// ana. Each request is made as somebody the door proves, because that is the layer that decides who is calling.
type memberFixture struct {
	workspaces *control.Workspaces
	book       *people.Book
}

func newMemberFixture(t *testing.T) memberFixture {
	t.Helper()
	book := book(t)
	if _, err := book.Claim("me@example.com", "Me"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	for _, person := range []struct{ email, name string }{
		{"ana@example.com", "Ana"}, {"bo@example.com", "Bo"},
	} {
		if _, err := book.Add(person.email, person.name); err != nil {
			t.Fatalf("add %s: %v", person.email, err)
		}
	}
	workspaces := control.NewWorkspaces(control.WorkspacesOptions{DataDir: t.TempDir(), AdminEmail: "me@example.com"})
	for _, room := range []control.Workspace{
		{Name: "house", Path: filepath.Join(t.TempDir(), "house"), Kind: control.KindRoom,
			Members: []control.Member{{Email: "me@example.com", Role: control.RoleOwner}}},
		{Name: "ana", Path: filepath.Join(t.TempDir(), "ana"), Kind: control.KindPersonal,
			Members: []control.Member{{Email: "ana@example.com", Role: control.RoleOwner}}},
	} {
		if err := workspaces.Add(room); err != nil {
			t.Fatalf("add %s: %v", room.Name, err)
		}
	}
	return memberFixture{workspaces: workspaces, book: book}
}

// as builds the handler the door would hand a request from this address.
func (f memberFixture) as(t *testing.T, email string) http.Handler {
	t.Helper()
	gate := Gate{Door: stubDoor{caller: identity.Caller{Email: email}}, People: f.book}
	return New(f.workspaces, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}, nil, gate).Handler()
}

func (f memberFixture) addGuest(t *testing.T, owner, room, guest string) {
	t.Helper()
	if _, err := f.workspaces.AddGuest(control.Caller{Email: owner}, room, guest); err != nil {
		t.Fatalf("seed %s in %s: %v", guest, room, err)
	}
}

func do(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	handler.ServeHTTP(recorder, request)
	return recorder
}

// The room's owner invites somebody the book names, and the listing says who is in the room. A guest cannot; an
// address the book does not hold is refused with the sentence that says who can add them (ADR 0034 §3, §4).
func TestOnlyTheRoomsOwnerInvites(t *testing.T) {
	fixture := newMemberFixture(t)

	refused := do(t, fixture.as(t, "me@example.com"), http.MethodPost, "/api/workspaces/house/members", `{"email":"nobody@example.com"}`)
	if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "not in People") {
		t.Fatalf("an unknown address: %d %s", refused.Code, refused.Body.String())
	}

	invited := do(t, fixture.as(t, "me@example.com"), http.MethodPost, "/api/workspaces/house/members", `{"email":"ana@example.com"}`)
	if invited.Code != http.StatusOK {
		t.Fatalf("the owner invited: %d %s", invited.Code, invited.Body.String())
	}
	var listing struct {
		Workspaces []struct {
			Name    string `json:"name"`
			Members []struct {
				Email string `json:"email"`
				Role  string `json:"role"`
			} `json:"members"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(invited.Body.Bytes(), &listing); err != nil {
		t.Fatalf("the listing is not JSON: %v", err)
	}
	found := false
	for _, room := range listing.Workspaces {
		if room.Name != "house" {
			continue
		}
		for _, member := range room.Members {
			if member.Email == "ana@example.com" && member.Role == "guest" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("the invitation is not in the listing: %s", invited.Body.String())
	}

	// A guest has the room and cannot invite.
	guest := do(t, fixture.as(t, "ana@example.com"), http.MethodPost, "/api/workspaces/house/members", `{"email":"bo@example.com"}`)
	if guest.Code != http.StatusForbidden {
		t.Errorf("a guest invited somebody: %d %s", guest.Code, guest.Body.String())
	}
}

// A guest may leave and may not remove anybody else; the owner removes a guest and cannot leave (ADR 0034 §3).
func TestAGuestLeavesAndTheOwnerRemoves(t *testing.T) {
	fixture := newMemberFixture(t)
	fixture.addGuest(t, "me@example.com", "house", "ana@example.com")
	fixture.addGuest(t, "me@example.com", "house", "bo@example.com")

	removed := do(t, fixture.as(t, "ana@example.com"), http.MethodDelete, "/api/workspaces/house/members/bo@example.com", "")
	if removed.Code != http.StatusForbidden {
		t.Errorf("a guest removed somebody else: %d %s", removed.Code, removed.Body.String())
	}

	left := do(t, fixture.as(t, "ana@example.com"), http.MethodDelete, "/api/workspaces/house/members/ana@example.com", "")
	if left.Code != http.StatusOK {
		t.Errorf("a guest could not leave: %d %s", left.Code, left.Body.String())
	}

	if removed := do(t, fixture.as(t, "me@example.com"), http.MethodDelete, "/api/workspaces/house/members/bo@example.com", ""); removed.Code != http.StatusOK {
		t.Errorf("the owner could not remove a guest: %d %s", removed.Code, removed.Body.String())
	}
	if owner := do(t, fixture.as(t, "me@example.com"), http.MethodDelete, "/api/workspaces/house/members/me@example.com", ""); owner.Code != http.StatusConflict {
		t.Errorf("the owner left: %d %s", owner.Code, owner.Body.String())
	}
}

// Forgetting a room is its owner's, from the console; the install's admin who is not in the room cannot, and gets
// the same 404 as a room that is not there. A personal room is never shared (ADR 0034 §3, §5, §6).
func TestForgetIsTheRoomsOwnersAndPersonalIsNeverShared(t *testing.T) {
	fixture := newMemberFixture(t)

	if code := do(t, fixture.as(t, "me@example.com"), http.MethodPost, "/api/workspaces/house/members", `{"email":"ana@example.com"}`).Code; code != http.StatusOK {
		t.Fatalf("seeding the room: %d", code)
	}
	if refused := do(t, fixture.as(t, "ana@example.com"), http.MethodPost, "/api/workspaces/house/remove", `{}`); refused.Code != http.StatusForbidden {
		t.Errorf("a guest forgot the room: %d %s", refused.Code, refused.Body.String())
	}
	// The admin is not a member of ana's personal room, so it is not there as far as this route is concerned.
	if refused := do(t, fixture.as(t, "me@example.com"), http.MethodPost, "/api/workspaces/ana/remove", `{}`); refused.Code != http.StatusNotFound {
		t.Errorf("the admin forgot a room they are not in: %d %s", refused.Code, refused.Body.String())
	}
	if refused := do(t, fixture.as(t, "ana@example.com"), http.MethodPost, "/api/workspaces/ana/members", `{"email":"bo@example.com"}`); refused.Code != http.StatusConflict {
		t.Errorf("a personal room accepted a guest: %d %s", refused.Code, refused.Body.String())
	}
	if forgot := do(t, fixture.as(t, "me@example.com"), http.MethodPost, "/api/workspaces/house/remove", `{}`); forgot.Code != http.StatusOK {
		t.Errorf("the owner could not forget the room: %d %s", forgot.Code, forgot.Body.String())
	}
}
