// a turn: prompting, drafts, aborting, and what the workspace can do
//
// Part of the httpapi package. The layer table is in ADR 0002: this is the transport edge,
// and it forwards frames it does not interpret.
package httpapi

import (
	"encoding/json"
	"github.com/marcelowa/acornd/gateway/internal/control"
	"github.com/marcelowa/acornd/gateway/internal/protocol"
	"net/http"
	"strings"
)

func (s *Server) commands(w http.ResponseWriter, r *http.Request) {
	view(s, w, r, func(ctl *control.Control) (json.RawMessage, error) {
		return ctl.Commands(r.Context())
	})
}

func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	view(s, w, r, func(ctl *control.Control) (protocol.AgentsData, error) {
		return ctl.Agents(r.Context())
	})
}

func (s *Server) reload(w http.ResponseWriter, r *http.Request) {
	s.stateChange(w, r, func(ctl *control.Control) error { return ctl.Reload(r.Context()) })
}

// mayRunPinned applies the same two lists to the model this conversation is already pinned to.
func (s *Server) mayRunPinned(w http.ResponseWriter, r *http.Request, ctl *control.Control, conversation string) bool {
	binding := ctl.StateFor(callerOf(r), conversation).Agent
	if binding.Provider == "" || binding.ModelID == "" {
		// Nothing pinned yet: the defaults decide, and they were checked when they were set.
		return true
	}
	return s.mayRunInstall(w, r, binding.Provider, binding.ModelID)
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	var body struct {
		Text        string            `json:"text"`
		WhenBusy    protocol.WhenBusy `json:"whenBusy"`
		Attachments []string          `json:"attachments"`
	}
	if !decode(w, r, &body) {
		return
	}
	when := body.WhenBusy
	switch when {
	case protocol.WhenBusySteer, protocol.WhenBusyReject, protocol.WhenBusyFollowUp:
	case "":
		when = protocol.WhenBusyFollowUp
	default:
		http.Error(w, "whenBusy must be steer, followUp or reject", http.StatusBadRequest)
		return
	}
	// The conversation's own model is checked before the words are sent: a model taken off the list must stop the
	// conversation that was using it, not wait for somebody to re-pin it.
	if !s.mayRunPinned(w, r, ctl, r.PathValue("id")) {
		return
	}
	// A shared room names its speaker before the model reads the turn (ADR 0032 §7), and the author rides with the
	// words so the worker stores who said them (ADR 0034 §7). The prefix is composed there, never here.
	if err := ctl.Prompt(r.Context(), callerOf(r), s.authorOf(r), r.PathValue("id"), strings.TrimSpace(body.Text), when, body.Attachments); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// draft is the composer's own write: the unsent message of one conversation, saved so the
// same words wait there on every machine and on the next reload. Empty words and no files
// clear it; the answer is what was stored, stamp included, so the client can tell its own
// echo from another machine's write.
func (s *Server) draft(w http.ResponseWriter, r *http.Request) {
	// ownAnswer, not stateBody: this handler answers with the stored draft, and a second document
	// (the state) written after it is a body no client can parse.
	ownAnswer(s, w, r, func(ctl *control.Control, body struct {
		Text        string                    `json:"text"`
		Attachments []control.DraftAttachment `json:"attachments"`
	}) error {
		stored, err := ctl.SetDraft(callerOf(r), r.PathValue("id"), body.Text, body.Attachments)
		if err != nil {
			return err
		}
		// The draft's answer is the stored draft, not the state: a keystroke is not a change to
		// anything else.
		writeJSON(w, http.StatusOK, stored)
		return nil
	})
}

func (s *Server) abort(w http.ResponseWriter, r *http.Request) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	if err := ctl.AbortBy(r.Context(), s.authorOf(r), r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}

func (s *Server) agent(w http.ResponseWriter, r *http.Request) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	if !s.mayRoom(w, r, ctl, control.ActionModel) {
		return
	}
	// Pointers, so "absent" and "set to empty" are different requests. A plain
	// struct cannot tell "leave the model alone" from "clear the model".
	var body struct {
		Provider      *string `json:"provider"`
		ModelID       *string `json:"modelId"`
		ThinkingLevel *string `json:"thinkingLevel"`
		Definition    *string `json:"definition"`
	}
	if !decode(w, r, &body) {
		return
	}
	provider, model := derefString(body.Provider), derefString(body.ModelID)
	if !s.mayRun(w, r, provider, model) {
		return
	}
	if err := ctl.SetAgent(r.Context(), r.PathValue("id"), control.AgentChange{
		Provider:      body.Provider,
		ModelID:       body.ModelID,
		ThinkingLevel: body.ThinkingLevel,
		Definition:    body.Definition,
	}); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, s.stateOf(ctl, callerOf(r), ""))
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	view(s, w, r, func(ctl *control.Control) (json.RawMessage, error) {
		return ctl.Usage(r.Context())
	})
}
