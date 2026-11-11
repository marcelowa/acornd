// the support bundle routes: a redacted diagnosis the owner can hand to somebody
//
// Part of the httpapi package. The collector is control's, and the permission is the room's:
// this file only turns the result into a preview document or a file.
package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/marcelowa/acornd/gateway/internal/support"
)

func (s *Server) supportPreview(w http.ResponseWriter, r *http.Request) {
	ctl, ok := s.withControl(w, r)
	if !ok {
		return
	}
	preview, err := ctl.SupportPreview(r.Context(), callerOf(r))
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) supportBundle(w http.ResponseWriter, r *http.Request) {
	ctl, ok := s.withControl(w, r)
	if !ok {
		return
	}
	bundle, err := ctl.SupportBundle(r.Context(), callerOf(r))
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	raw, err := bundle.TarGz()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filename := supportFilename(bundle)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(raw)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func supportFilename(bundle *support.Bundle) string {
	name := strings.TrimSpace(bundle.Workspace)
	name = strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(name)
	if name == "" {
		name = "workspace"
	}
	return fmt.Sprintf("acornd-support-%s-%s.tar.gz", name, bundle.Created.UTC().Format("20060102-150405"))
}
