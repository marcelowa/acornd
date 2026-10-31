// model and agent defaults, at the workspace and at the install
//
// Part of the httpapi package. The layer table is in ADR 0002: this is the transport edge,
// and it forwards frames it does not interpret.
package httpapi

import (
	"github.com/acornfall/acornfall/gateway/internal/control"
	"net/http"
)

func (s *Server) setDefaultThinking(w http.ResponseWriter, r *http.Request) {
	stateBody(s, w, r, func(ctl *control.Control, body struct {
		Level string `json:"level"`
	}) error {
		if !ctl.May(callerOf(r), control.ActionModel) {
			return control.ErrNotAllowed
		}
		return ctl.SetDefaultThinking(r.Context(), body.Level)
	})
}

func (s *Server) setDefaultAgent(w http.ResponseWriter, r *http.Request) {
	stateBody(s, w, r, func(ctl *control.Control, body struct {
		Definition string `json:"definition"`
	}) error {
		if !ctl.May(callerOf(r), control.ActionModel) {
			return control.ErrNotAllowed
		}
		return ctl.SetDefaultAgent(r.Context(), body.Definition)
	})
}

func (s *Server) setInstallDefaultModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		ModelID  string `json:"modelId"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.workspaces.SetInstallModel(r.Context(), body.Provider, body.ModelID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.listing())
}

func (s *Server) setInstallDefaultThinking(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Level string `json:"level"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.workspaces.SetInstallThinking(r.Context(), body.Level); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.listing())
}

func (s *Server) setInstallDefaultAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Definition string `json:"definition"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.workspaces.SetInstallAgent(r.Context(), body.Definition); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, s.listing())
}

// setDefaultModel is the room's default, and it is an assignment: the model is checked against both lists before
// it is stored, the same way a conversation's own model is. It is written by hand rather than through stateBody
// because a refused model answers with its own sentence, and a state document written after it would be a second
// body on one response.
func (s *Server) setDefaultModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		ModelID  string `json:"modelId"`
	}
	if !decode(w, r, &body) {
		return
	}
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	if !s.mayRoom(w, r, ctl, control.ActionModel) {
		return
	}
	if !s.mayRun(w, r, body.Provider, body.ModelID) {
		return
	}
	if err := ctl.SetDefaultModel(r.Context(), body.Provider, body.ModelID); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, s.stateOf(ctl, callerOf(r), ""))
}
