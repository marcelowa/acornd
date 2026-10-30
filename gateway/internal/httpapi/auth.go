// provider sign-in as the interface drives it
//
// Part of the httpapi package. The layer table is in ADR 0002: this is the transport edge,
// and it forwards frames it does not interpret.
package httpapi

import (
	"github.com/acornfall/acornfall/gateway/internal/control"
	"net/http"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	var body struct {
		ProviderID string `json:"providerId"`
		AuthType   string `json:"authType"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ProviderID == "" {
		http.Error(w, "providerId is required", http.StatusBadRequest)
		return
	}
	authType := body.AuthType
	switch authType {
	case "oauth", "api_key":
	case "":
		authType = "api_key"
	default:
		http.Error(w, "authType must be api_key or oauth", http.StatusBadRequest)
		return
	}
	authID, err := ctl.Login(r.Context(), body.ProviderID, authType)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started", "authId": authID})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	var body struct {
		ProviderID string `json:"providerId"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ProviderID == "" {
		http.Error(w, "providerId is required", http.StatusBadRequest)
		return
	}
	if err := ctl.Logout(r.Context(), body.ProviderID); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, s.stateOf(ctl, callerOf(r), ""))
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	s.stateChange(w, r, func(ctl *control.Control) error { return ctl.Refresh(r.Context()) })
}

func (s *Server) authAnswer(w http.ResponseWriter, r *http.Request) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	var body struct {
		AuthID    string `json:"authId"`
		PromptID  string `json:"promptId"`
		Value     string `json:"value"`
		Cancelled bool   `json:"cancelled"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.PromptID == "" {
		http.Error(w, "promptId is required", http.StatusBadRequest)
		return
	}
	if err := ctl.AnswerAuth(r.Context(), body.AuthID, body.PromptID, body.Value, body.Cancelled); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "answered"})
}

func (s *Server) authCancel(w http.ResponseWriter, r *http.Request) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	var body struct {
		AuthID string `json:"authId"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := ctl.CancelAuth(r.Context(), body.AuthID); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}
