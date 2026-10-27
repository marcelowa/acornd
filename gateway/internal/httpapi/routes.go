// the route table, grouped by resource
//
// Part of the httpapi package. The layer table is in ADR 0002: this is the transport edge,
// and it forwards frames it does not interpret.
package httpapi

import (
	"net/http"
)

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// A conversation is addressed under the workspace it belongs to, because ids are only
	// unique inside a workspace's storage. The earlier paths without a workspace mean the
	// general one, so existing links and scripts keep working.
	for _, base := range []string{"/api/workspaces/{ws}", "/api"} {
		s.conversationRoutes(mux, base)
	}
	// Install-wide defaults: no workspace in the path, because the point of them is that they
	// are not any one workspace's.
	// Install-wide decisions are the owner's, and they say so in the table rather than in each handler: a
	// reader can see at a glance which routes are about a workspace and which are about the install.
	mux.HandleFunc("POST /api/defaults/model", s.owner(s.setInstallDefaultModel))
	mux.HandleFunc("POST /api/defaults/thinking", s.owner(s.setInstallDefaultThinking))
	mux.HandleFunc("POST /api/defaults/agent", s.owner(s.setInstallDefaultAgent))

	mux.HandleFunc("GET /healthz", s.health)
	s.updateRoutes(mux)
	s.secretRoutes(mux)
	s.workspaceRoutes(mux)
	s.pageRoutes(mux)
	// The client's own assets, and nothing else. Every URL the interface routes is registered
	// above — the whole /settings subtree, one page per conversation and per workspace, the
	// release pages — so a path outside them is a 404 rather than a document the client cannot
	// route anywhere.
	mux.Handle("GET /", noCache(http.FileServerFS(s.static)))

	// The door is in front of everything, which is what makes "no path reaches a handler unproven" true
	// by construction rather than by each handler remembering.
	if s.gate.Door != nil {
		return s.admit(mux)
	}
	return mux
}

// conversationRoutes is everything scoped to one conversation, under a base path. Two bases are
// registered: the workspace-scoped one, and the older one that means the caller's own workspace.
func (s *Server) conversationRoutes(mux *http.ServeMux, base string) {
	mux.HandleFunc("GET "+base+"/state", s.state)
	mux.HandleFunc("GET "+base+"/status", s.workspaceStatus)
	mux.HandleFunc("GET "+base+"/stream", s.stream)
	mux.HandleFunc("GET "+base+"/conversations", s.conversations)
	mux.HandleFunc("POST "+base+"/conversations", s.createConversation)
	mux.HandleFunc("POST "+base+"/conversations/{id}/select", s.selectConversation)
	mux.HandleFunc("POST "+base+"/conversations/{id}/rename", s.renameConversation)
	mux.HandleFunc("POST "+base+"/conversations/{id}/hide", s.hideConversation)
	mux.HandleFunc("POST "+base+"/conversations/{id}/fork", s.forkConversation)
	mux.HandleFunc("POST "+base+"/conversations/{id}/compact", s.compactConversation)
	mux.HandleFunc("POST "+base+"/conversations/{id}/compact-preview", s.compactPreview)
	mux.HandleFunc("POST "+base+"/conversations/{id}/messages", s.messages)
	mux.HandleFunc("POST "+base+"/conversations/{id}/draft", s.draft)
	mux.HandleFunc("POST "+base+"/attachments", s.attachments)
	mux.HandleFunc("POST "+base+"/conversations/{id}/abort", s.abort)
	mux.HandleFunc("POST "+base+"/conversations/{id}/agent", s.agent)
	mux.HandleFunc("GET "+base+"/conversations/{id}/policy", s.policy)
	mux.HandleFunc("GET "+base+"/conversations/{id}/messages", s.messagesDoc)
	mux.HandleFunc("GET "+base+"/agents", s.agents)
	mux.HandleFunc("GET "+base+"/commands", s.commands)
	mux.HandleFunc("POST "+base+"/agents/reload", s.reload)
	mux.HandleFunc("GET "+base+"/usage", s.usage)
	// A provider key is the install's, spent by everyone: adding or removing one is the owner's.
	mux.HandleFunc("POST "+base+"/providers/login", s.owner(s.login))
	mux.HandleFunc("POST "+base+"/providers/logout", s.owner(s.logout))
	mux.HandleFunc("POST "+base+"/providers/refresh", s.owner(s.refresh))
	mux.HandleFunc("POST "+base+"/auth/answer", s.authAnswer)
	mux.HandleFunc("POST "+base+"/auth/cancel", s.authCancel)
	mux.HandleFunc("POST "+base+"/default-model", s.setDefaultModel)
	mux.HandleFunc("POST "+base+"/default-thinking", s.setDefaultThinking)
	mux.HandleFunc("POST "+base+"/default-agent", s.setDefaultAgent)
}

func (s *Server) updateRoutes(mux *http.ServeMux) {
	// Replacing the binary is the owner's: a person who could restart the gateway could take the install
	// down for everybody, which is not a workspace-sized decision.
	mux.HandleFunc("GET /api/update/status", s.owner(s.updateStatus))
	mux.HandleFunc("GET /api/update/releases", s.owner(s.updateReleases))
	mux.HandleFunc("GET /api/update/releases/{tag}", s.owner(s.updateRelease))
	mux.HandleFunc("GET /api/update/notes", s.owner(s.updateNotes))
	mux.HandleFunc("POST /api/update/check", s.owner(s.updateCheck))
	mux.HandleFunc("POST /api/update/apply", s.owner(s.updateApply))
	mux.HandleFunc("POST /api/update/restart", s.owner(s.updateRestart))
	mux.HandleFunc("POST /api/update/rollback", s.owner(s.updateRollback))
	mux.HandleFunc("POST /api/update/settings", s.owner(s.updateSettings))
}

// secretRoutes: install-wide entries at /api/secrets, a workspace's own under the workspace.
// Values go in and never come back out.
func (s *Server) secretRoutes(mux *http.ServeMux) {
	// A global secret reaches every workspace, so it is the owner's; a workspace's own secret is its
	// members', and is routed by the workspace below.
	mux.HandleFunc("GET /api/secrets", s.owner(s.secretListGlobal))
	mux.HandleFunc("POST /api/secrets", s.owner(s.secretPutGlobal))
	mux.HandleFunc("POST /api/secrets/{name}/rotate", s.owner(s.secretRotateGlobal))
	mux.HandleFunc("DELETE /api/secrets/{name}", s.owner(s.secretDeleteGlobal))
	mux.HandleFunc("GET /api/workspaces/{ws}/secrets", s.secretListWorkspace)
	mux.HandleFunc("POST /api/workspaces/{ws}/secrets", s.secretPutWorkspace)
	mux.HandleFunc("POST /api/workspaces/{ws}/conversations/{id}/secrets/{request}", s.secretRequestAnswer)
	mux.HandleFunc("POST /api/workspaces/{ws}/secrets/{name}/rotate", s.secretRotateWorkspace)
	mux.HandleFunc("DELETE /api/workspaces/{ws}/secrets/{name}", s.secretDeleteWorkspace)
}

func (s *Server) workspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/workspaces", s.workspaceList)
	// Making, re-making and forgetting a workspace is the install's own business: a person uses the rooms
	// they were added to. Creation becomes theirs when `can_create` is wired, and not before.
	mux.HandleFunc("POST /api/workspaces", s.owner(s.workspaceCreate))
	mux.HandleFunc("POST /api/workspaces/{ws}/update", s.owner(s.workspaceUpdate))
	mux.HandleFunc("POST /api/workspaces/{ws}/remove", s.owner(s.workspaceRemove))
}

// pageRoutes are the interface's own screens: /c/<workspace>/<conversation> is a conversation,
// /w/<workspace> is a workspace with whichever conversation it lands on. Routes rather than a
// catch-all, so a reload and a shared link land where someone was.
func (s *Server) pageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /c/{ws}/{id}", s.page)
	mux.HandleFunc("GET /c/{id}", s.page)
	mux.HandleFunc("GET /w/{ws}", s.page)
	mux.HandleFunc("GET /update", s.page)
	mux.HandleFunc("GET /update/{tag}", s.page)
	mux.HandleFunc("GET /settings", s.page)
	mux.HandleFunc("GET /settings/", s.page)
	mux.HandleFunc("GET /workspaces", s.page)
}
