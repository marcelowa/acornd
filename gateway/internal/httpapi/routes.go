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
	mux.HandleFunc("POST /api/defaults/model", s.setInstallDefaultModel)
	mux.HandleFunc("POST /api/defaults/thinking", s.setInstallDefaultThinking)
	mux.HandleFunc("POST /api/defaults/agent", s.setInstallDefaultAgent)

	mux.HandleFunc("GET /healthz", s.health)
	s.updateRoutes(mux)
	s.secretRoutes(mux)
	s.workspaceRoutes(mux)
	s.pageRoutes(mux)
	// The client's own assets, and nothing else: a path that matches no route is a client-side
	// route, so the file server answers with index.html and the client decides.
	mux.Handle("GET /", noCache(http.FileServerFS(s.static)))
	return mux
}

// conversationRoutes is everything scoped to one conversation, under a base path. Two bases are
// registered: the workspace-scoped one, and the older one that means the general workspace.
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
	mux.HandleFunc("POST "+base+"/providers/login", s.login)
	mux.HandleFunc("POST "+base+"/providers/logout", s.logout)
	mux.HandleFunc("POST "+base+"/providers/refresh", s.refresh)
	mux.HandleFunc("POST "+base+"/auth/answer", s.authAnswer)
	mux.HandleFunc("POST "+base+"/auth/cancel", s.authCancel)
	mux.HandleFunc("POST "+base+"/default-model", s.setDefaultModel)
	mux.HandleFunc("POST "+base+"/default-thinking", s.setDefaultThinking)
	mux.HandleFunc("POST "+base+"/default-agent", s.setDefaultAgent)
}

func (s *Server) updateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/update/status", s.updateStatus)
	mux.HandleFunc("GET /api/update/releases", s.updateReleases)
	mux.HandleFunc("GET /api/update/releases/{tag}", s.updateRelease)
	mux.HandleFunc("GET /api/update/notes", s.updateNotes)
	mux.HandleFunc("POST /api/update/check", s.updateCheck)
	mux.HandleFunc("POST /api/update/apply", s.updateApply)
	mux.HandleFunc("POST /api/update/restart", s.updateRestart)
	mux.HandleFunc("POST /api/update/rollback", s.updateRollback)
	mux.HandleFunc("POST /api/update/settings", s.updateSettings)
}

// secretRoutes: install-wide entries at /api/secrets, a workspace's own under the workspace.
// Values go in and never come back out.
func (s *Server) secretRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/secrets", s.secretListGlobal)
	mux.HandleFunc("POST /api/secrets", s.secretPutGlobal)
	mux.HandleFunc("POST /api/secrets/{name}/rotate", s.secretRotateGlobal)
	mux.HandleFunc("DELETE /api/secrets/{name}", s.secretDeleteGlobal)
	mux.HandleFunc("GET /api/workspaces/{ws}/secrets", s.secretListWorkspace)
	mux.HandleFunc("POST /api/workspaces/{ws}/secrets", s.secretPutWorkspace)
	mux.HandleFunc("POST /api/workspaces/{ws}/conversations/{id}/secrets/{request}", s.secretRequestAnswer)
	mux.HandleFunc("POST /api/workspaces/{ws}/secrets/{name}/rotate", s.secretRotateWorkspace)
	mux.HandleFunc("DELETE /api/workspaces/{ws}/secrets/{name}", s.secretDeleteWorkspace)
}

func (s *Server) workspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/workspaces", s.workspaceList)
	mux.HandleFunc("POST /api/workspaces", s.workspaceCreate)
	mux.HandleFunc("POST /api/workspaces/{ws}/update", s.workspaceUpdate)
	mux.HandleFunc("POST /api/workspaces/{ws}/remove", s.workspaceRemove)
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
