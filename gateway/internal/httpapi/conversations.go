// conversations: their operations and their views
//
// Part of the httpapi package. The layer table is in ADR 0002: this is the transport edge,
// and it forwards frames it does not interpret.
package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/acornfall/acornfall/gateway/internal/control"
	"net/http"
)

func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	// Refreshed, not cached: the cached list is kept current by a poll that only runs
	// while a client is attached, so a script would otherwise read stale state.
	if err := ctl.RefreshConversations(r.Context()); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, ctl.State().Conversations)
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	s.stateChange(w, r, func(ctl *control.Control) error { return ctl.Create(r.Context()) })
}

func (s *Server) policy(w http.ResponseWriter, r *http.Request) {
	view(s, w, r, func(ctl *control.Control) (json.RawMessage, error) {
		return ctl.Policy(r.Context(), r.PathValue("id"))
	})
}

// messagesDoc exposes which entries were sent by another conversation, for debugging
// attribution without opening a browser.
func (s *Server) messagesDoc(w http.ResponseWriter, r *http.Request) {
	view(s, w, r, func(ctl *control.Control) (json.RawMessage, error) {
		return ctl.WorkerCall(r.Context(), "messages", r.PathValue("id"))
	})
}

func (s *Server) renameConversation(w http.ResponseWriter, r *http.Request) {
	stateBody(s, w, r, func(ctl *control.Control, body struct {
		Title string `json:"title"`
	}) error {
		return ctl.Rename(r.Context(), r.PathValue("id"), body.Title)
	})
}

func (s *Server) hideConversation(w http.ResponseWriter, r *http.Request) {
	stateBody(s, w, r, func(ctl *control.Control, body struct {
		Hidden bool `json:"hidden"`
	}) error {
		return ctl.Hide(r.Context(), r.PathValue("id"), body.Hidden)
	})
}

func (s *Server) forkConversation(w http.ResponseWriter, r *http.Request) {
	stateBody(s, w, r, func(ctl *control.Control, body struct {
		At string `json:"at"`
	}) error {
		if body.At == "" {
			return errors.New("at is required (the entry id to fork from)")
		}
		return ctl.Fork(r.Context(), r.PathValue("id"), body.At)
	})
}

func (s *Server) compactConversation(w http.ResponseWriter, r *http.Request) {
	s.stateChange(w, r, func(ctl *control.Control) error { return ctl.Compact(r.Context(), r.PathValue("id")) })
}

func (s *Server) compactPreview(w http.ResponseWriter, r *http.Request) {
	view(s, w, r, func(ctl *control.Control) (control.CompactPreview, error) {
		return ctl.CompactPreview(r.Context(), r.PathValue("id"))
	})
}

func (s *Server) selectConversation(w http.ResponseWriter, r *http.Request) {
	s.stateChange(w, r, func(ctl *control.Control) error { return ctl.Select(r.Context(), r.PathValue("id")) })
}
