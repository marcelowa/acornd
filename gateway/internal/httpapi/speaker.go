// Who is speaking, in a room with more than one person in it.
//
// ADR 0032 §7: a workspace with several members puts the speaker's name in front of the words the model reads, so
// a model answering three people is not left guessing which of them asked. A personal workspace has one voice and
// says nothing — a prefix there would be noise with no answer behind it.
//
// No author field on entries. Attribution here is for the model, which reads assembled text; a durable schema
// change to carry a display name would be a harness change for a presentation need.
package httpapi

import (
	"net/http"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/control"
)

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
	return known && len(workspace.Members) > 1
}

// prefixed puts the speaker in front of the words, and leaves them alone when there is nothing to say, when the
// message is files without words, or when it is a command — a slash command is expanded by matching its first
// character, and a prefix would turn `/compact` into an ordinary sentence about compacting.
func prefixed(text, speaker string) string {
	trimmed := strings.TrimSpace(text)
	if speaker == "" || trimmed == "" || strings.HasPrefix(trimmed, "/") {
		return text
	}
	return "[" + speaker + "] " + text
}
