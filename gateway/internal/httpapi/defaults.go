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
		return ctl.SetDefaultThinking(r.Context(), body.Level)
	})
}

func (s *Server) setDefaultAgent(w http.ResponseWriter, r *http.Request) {
	stateBody(s, w, r, func(ctl *control.Control, body struct {
		Definition string `json:"definition"`
	}) error {
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

func (s *Server) setDefaultModel(w http.ResponseWriter, r *http.Request) {
	stateBody(s, w, r, func(ctl *control.Control, body struct {
		Provider string `json:"provider"`
		ModelID  string `json:"modelId"`
	}) error {
		return ctl.SetDefaultModel(r.Context(), body.Provider, body.ModelID)
	})
}
