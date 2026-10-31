package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/control"
	"github.com/marcelowa/acornd/gateway/internal/people"
)

// speakerEdge is the edge with a book of named people and a registry, which is everything deciding who is speaking
// needs: the membership from the room and the name from the book.
func speakerEdge(t *testing.T, rooms ...control.Workspace) (*Server, *people.Book) {
	t.Helper()
	opened, err := people.Open(filepath.Join(t.TempDir(), people.File))
	if err != nil {
		t.Fatalf("open book: %v", err)
	}
	if _, err := opened.Add("ana@example.com", "Ana Silva"); err != nil {
		t.Fatalf("add a person: %v", err)
	}
	if _, err := opened.Claim("me@example.com", "Me"); err != nil {
		t.Fatalf("claim the admin: %v", err)
	}
	workspaces := control.NewWorkspaces(control.WorkspacesOptions{DataDir: t.TempDir()})
	for _, room := range rooms {
		if err := workspaces.Add(room); err != nil {
			t.Fatalf("add %s: %v", room.Name, err)
		}
	}
	server := New(workspaces, nil, nil, Gate{People: opened})
	return server, opened
}

// room is a request about one workspace, as the caller the door proved.
func room(name string, caller control.Caller) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/api/workspaces/"+name+"/messages", nil)
	request.SetPathValue("ws", name)
	return request.WithContext(withCaller(context.Background(), caller))
}

// A personal room has one voice, so the model is told no name: a prefix there would be a question with no answer
// behind it (ADR 0032 §7). The author is still recorded — the interface shows who wrote what.
func TestAPersonalRoomHasNoSpeaker(t *testing.T) {
	server, _ := speakerEdge(t, control.Workspace{
		Name:    "ana",
		Path:    t.TempDir(),
		Title:   "Ana",
		Members: []control.Member{{Email: "ana@example.com", Role: control.RoleOwner}},
	})
	request := room("ana", control.Caller{Email: "ana@example.com"})
	if speaker := server.speakerFor(request); speaker != "" {
		t.Errorf("a personal room named its speaker %q", speaker)
	}
	author := server.authorOf(request)
	if author == nil || author.Email != "ana@example.com" || author.Name != "Ana Silva" {
		t.Fatalf("the author should be the person, speaker or not, got %+v", author)
	}
	if author.Speaker != "" {
		t.Errorf("a personal room's author carries no speaker, got %q", author.Speaker)
	}
}

// A shared room names somebody who is not its admin: the words are prefixed for the model, and the person the prefix
// belongs to is the caller (ADR 0032 §7).
func TestASharedRoomNamesANonOwner(t *testing.T) {
	server, _ := speakerEdge(t, control.Workspace{
		Name:    "house",
		Path:    t.TempDir(),
		Title:   "House",
		Members: []control.Member{{Email: "me@example.com", Role: control.RoleOwner}, {Email: "ana@example.com", Role: control.RoleGuest}},
	})
	request := room("house", control.Caller{Email: "ana@example.com"})
	if speaker := server.speakerFor(request); speaker != "Ana Silva" {
		t.Errorf("a shared room's speaker = %q, want the caller's name", speaker)
	}
	if author := server.authorOf(request); author == nil || author.Speaker != "Ana Silva" {
		t.Errorf("the author should carry the speaker the model reads, got %+v", author)
	}

	// The install's admin is not named in it: a prefix would tell the model that the room's admin is a guest there.
	admin := room("house", control.Caller{Admin: true, Email: "me@example.com"})
	if speaker := server.speakerFor(admin); speaker != "" {
		t.Errorf("the admin was named in a shared room: %q", speaker)
	}
}

// An install with no admin claimed has no address to attribute words to. An author that invented one would be a guess
// in the transcript, which is the thing this design refuses.
func TestNobodyToNameIsNoAuthor(t *testing.T) {
	server, _ := speakerEdge(t, control.Workspace{
		Name:    "personal",
		Path:    t.TempDir(),
		Title:   "Personal",
		Members: []control.Member{{Email: "me@example.com", Role: control.RoleOwner}},
	})
	if author := server.authorOf(room("personal", control.Caller{Admin: true})); author != nil {
		t.Errorf("nobody proved an address, so nobody is the author, got %+v", author)
	}
}

// The name is the one the admin gave, and the address is the fallback: a person added by email only is still
// somebody the model can be told about.
func TestADisplayNameFallsBackToTheAddress(t *testing.T) {
	named := people.Person{Email: "ana@example.com", Name: "Ana Silva"}
	if got := named.DisplayName(); got != "Ana Silva" {
		t.Errorf("a named person is %q", got)
	}
	unnamed := people.Person{Email: "ana.silva@example.com"}
	if got := unnamed.DisplayName(); got != "ana.silva" {
		t.Errorf("an unnamed person is %q", got)
	}
}
