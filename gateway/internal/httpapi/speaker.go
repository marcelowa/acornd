// Who is speaking, in a room with more than one person in it.
//
// ADR 0032 §7: a workspace with several members puts the speaker's name in front of the words the model reads, so
// a model answering three people is not left guessing which of them asked. A personal workspace has one voice and
// says nothing — a prefix there would be noise with no answer behind it.
//
// ADR 0034 §7: the name is decided here and stored as a name. The edge no longer writes `[Ana] ` into the message,
// because a guest could type another person's prefix and be believed, and because with an author field the model
// would read the name twice. The worker composes the prefix when it assembles the transcript for the model, and
// `speakerFor` is still the only thing that decides it.
package httpapi

import (
	"net/http"

	"github.com/marcelowa/acornd/gateway/internal/control"
	"github.com/marcelowa/acornd/gateway/internal/people"
	"github.com/marcelowa/acornd/gateway/internal/protocol"
)

// authorOf is who wrote this request's words: the address the door proved, the display name the book has for it,
// and the speaker the model is told — which speakerFor decides.
//
// Nobody is nil: an install with no admin claimed has no address to attribute words to, and inventing a name there
// would put a guess in the transcript the interface is not allowed to make.
func (s *Server) authorOf(r *http.Request) *protocol.Author {
	caller := callerOf(r)
	if caller.Email == "" {
		return nil
	}
	person := people.Person{Email: caller.Email}
	if s.gate.People != nil {
		if named, found, err := s.gate.People.Find(caller.Email); err == nil && found {
			person = named
		}
	}
	return &protocol.Author{Email: caller.Email, Name: person.DisplayName(), Speaker: s.speakerFor(r)}
}

// speakerFor answers the name this request's words are prefixed with, and "" when nobody needs naming.
func (s *Server) speakerFor(r *http.Request) string {
	caller := callerOf(r)
	// The admin is not named in their own rooms: the install is theirs, and a prefix would tell the model that a
	// room's admin is a guest in it.
	if caller.Admin || caller.Email == "" || s.gate.People == nil || s.workspaces == nil {
		return ""
	}
	if !shared(s.workspaces, workspaceOf(r)) {
		return ""
	}
	person, found, err := s.gate.People.Find(caller.Email)
	if err != nil || !found {
		return ""
	}
	return person.DisplayName()
}

// workspaceOf is the room this request is about: the one in the path, then the one in the query, then the personal
// one — the same order control resolves a request in.
func workspaceOf(r *http.Request) string {
	if name := r.PathValue("ws"); name != "" {
		return name
	}
	if name := r.URL.Query().Get("workspace"); name != "" {
		return name
	}
	return control.PersonalWorkspaceName
}

// shared reports whether a room has more than one person in it, which is the whole of when a speaker is named.
func shared(workspaces *control.Workspaces, name string) bool {
	workspace, known := workspaces.Get(name)
	return known && workspace.Shared()
}
