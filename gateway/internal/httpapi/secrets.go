package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/vault"
)

// secretBody is one create or update. A pointer field means "not mentioned": an update
// keeps what is stored, and a create takes the vault's default. Value empty on an update
// keeps the stored value; rotate is the endpoint that changes only the value.
type secretBody struct {
	Name         string              `json:"name"`
	Description  string              `json:"description"`
	Value        string              `json:"value"`
	Hosts        *[]string           `json:"hosts"`
	Substitution *vault.Substitution `json:"substitution"`
	AllowEnv     *bool               `json:"allowEnv"`
}

func (s *Server) secretStore(w http.ResponseWriter) (*vault.Store, bool) {
	store := s.workspaces.Vault()
	if store == nil {
		http.Error(w, "secrets are not configured on this install", http.StatusServiceUnavailable)
		return nil, false
	}
	return store, true
}

func (s *Server) workspaceAt(r *http.Request) (control.Workspace, bool) {
	name := r.PathValue("ws")
	if name == "" {
		name = control.GeneralWorkspaceName
	}
	return s.workspaces.Get(name)
}

func (s *Server) secretListGlobal(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStore(w)
	if !ok {
		return
	}
	metas, err := store.Global()
	if err != nil {
		secretFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": metas})
}

func (s *Server) secretPutGlobal(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStore(w)
	if !ok {
		return
	}
	s.secretPut(w, r, store, vault.Global, "")
}

func (s *Server) secretRotateGlobal(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStore(w)
	if !ok {
		return
	}
	s.secretRotate(w, r, store, vault.Global, "")
}

func (s *Server) secretDeleteGlobal(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStore(w)
	if !ok {
		return
	}
	s.secretDelete(w, r, store, vault.Global, "")
}

func (s *Server) secretListWorkspace(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStore(w)
	if !ok {
		return
	}
	workspace, ok := s.workspaceAt(r)
	if !ok {
		http.Error(w, "no such workspace", http.StatusNotFound)
		return
	}
	view, err := store.ForWorkspace(workspace.Path)
	if err != nil {
		secretFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace": map[string]string{"name": workspace.Name, "path": workspace.Path},
		"global":    view.Global,
		"own":       view.Workspace,
		"effective": view.Effective,
	})
}

func (s *Server) secretPutWorkspace(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStore(w)
	if !ok {
		return
	}
	workspace, ok := s.workspaceAt(r)
	if !ok {
		http.Error(w, "no such workspace", http.StatusNotFound)
		return
	}
	s.secretPut(w, r, store, vault.Workspace, workspace.Path)
}

func (s *Server) secretRotateWorkspace(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStore(w)
	if !ok {
		return
	}
	workspace, ok := s.workspaceAt(r)
	if !ok {
		http.Error(w, "no such workspace", http.StatusNotFound)
		return
	}
	s.secretRotate(w, r, store, vault.Workspace, workspace.Path)
}

func (s *Server) secretDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStore(w)
	if !ok {
		return
	}
	workspace, ok := s.workspaceAt(r)
	if !ok {
		http.Error(w, "no such workspace", http.StatusNotFound)
		return
	}
	s.secretDelete(w, r, store, vault.Workspace, workspace.Path)
}

func (s *Server) secretPut(w http.ResponseWriter, r *http.Request, store *vault.Store, scope vault.Scope, workspace string) {
	var body secretBody
	if !decode(w, r, &body) {
		return
	}
	meta, err := store.Upsert(vault.Upsert{
		Name:         body.Name,
		Description:  body.Description,
		Scope:        scope,
		Workspace:    workspace,
		Value:        body.Value,
		Hosts:        body.Hosts,
		Substitution: body.Substitution,
		AllowEnv:     body.AllowEnv,
	})
	if err != nil {
		secretFailure(w, err)
		return
	}
	s.applySecrets(r, scope, workspace, false)
	writeJSON(w, http.StatusOK, map[string]any{"secret": meta})
}

func (s *Server) secretRotate(w http.ResponseWriter, r *http.Request, store *vault.Store, scope vault.Scope, workspace string) {
	var body struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &body) {
		return
	}
	meta, err := store.Rotate(scope, workspace, r.PathValue("name"), body.Value)
	if err != nil {
		secretFailure(w, err)
		return
	}
	s.applySecrets(r, scope, workspace, false)
	writeJSON(w, http.StatusOK, map[string]any{"secret": meta})
}

func (s *Server) secretDelete(w http.ResponseWriter, r *http.Request, store *vault.Store, scope vault.Scope, workspace string) {
	name := r.PathValue("name")
	if err := store.Delete(scope, workspace, name); err != nil {
		secretFailure(w, err)
		return
	}
	s.applySecrets(r, scope, workspace, false)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name, "scope": scope})
}

// secretRequestAnswer is the card's endpoint: the value comes from the interface, goes
// straight into the vault, and never through the worker. The tool waiting on the other side
// learns only that it was set (and whether a restart is needed) or that it was cancelled.
func (s *Server) secretRequestAnswer(w http.ResponseWriter, r *http.Request) {
	store, ok := s.secretStore(w)
	if !ok {
		return
	}
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	requestID := r.PathValue("request")
	prompt, ok := ctl.SecretPrompt(requestID)
	if !ok {
		http.Error(w, "no secret request is waiting", http.StatusNotFound)
		return
	}
	if prompt.ConversationID != r.PathValue("id") {
		http.Error(w, "no secret request is waiting for this conversation", http.StatusNotFound)
		return
	}
	var body struct {
		Value       string    `json:"value"`
		Description *string   `json:"description"`
		Hosts       *[]string `json:"hosts"`
		Scope       string    `json:"scope"`
		Cancelled   bool      `json:"cancelled"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Cancelled {
		if !ctl.ResolveSecret(requestID, true, "", false, "the user cancelled") {
			http.Error(w, "no secret request is waiting", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
		return
	}
	if body.Value == "" {
		http.Error(w, "a secret needs a value", http.StatusBadRequest)
		return
	}
	scope := vault.Workspace
	switch body.Scope {
	case "", "workspace":
	case "global":
		scope = vault.Global
	default:
		http.Error(w, `scope is "workspace" or "global"`, http.StatusBadRequest)
		return
	}
	description := prompt.Description
	if body.Description != nil {
		description = strings.TrimSpace(*body.Description)
	}
	hosts := prompt.Hosts
	if body.Hosts != nil {
		hosts = *body.Hosts
	}
	if ctl.SecretNeedsHost() && len(hosts) == 0 {
		http.Error(w, "this engine needs at least one allowed host for a secret: name the host the value may reach", http.StatusBadRequest)
		return
	}
	workspace := ""
	if scope == vault.Workspace {
		workspace = ctl.WorkspacePath()
	}
	meta, err := store.Upsert(vault.Upsert{
		Name:        prompt.Name,
		Description: description,
		Scope:       scope,
		Workspace:   workspace,
		Value:       body.Value,
		Hosts:       &hosts,
	})
	if err != nil {
		secretFailure(w, err)
		return
	}
	// This answer is always during the turn that asked: defer the engine restart until that
	// run settles, or the sandbox would restart under the agent.
	s.applySecrets(r, scope, workspace, true)
	restart := ctl.SecretsRestart()
	if restart {
		// The turn will end because the sandbox has to restart. The control starts the
		// continuation itself once that restart lands; the user does not resume.
		ctl.QueueSecretContinuation(prompt.ConversationID, prompt.Name)
	}
	ctl.ResolveSecret(requestID, false, string(scope), restart, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"secret":          meta,
		"restartRequired": restart,
	})
}

// applySecrets asks the running workspaces a change affects to pick it up. The store already
// holds the value, so a sandbox that cannot take it now reports that in its state rather than
// making the save look failed. The work is bounded: an engine call per affected workspace.
func (s *Server) applySecrets(r *http.Request, scope vault.Scope, workspace string, deferRestart bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	s.workspaces.ApplySecrets(ctx, scope, workspace, deferRestart)
}

// secretFailure maps the vault's two caller mistakes to their status. Anything else is the
// install's own file or lock, which is a server problem, not the caller's. No error path
// ever carries a value.
func secretFailure(w http.ResponseWriter, err error) {
	var invalid vault.InvalidError
	switch {
	case errors.As(err, &invalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, vault.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
