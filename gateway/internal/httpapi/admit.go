// The door in front of every route: who is calling, and whether the request could have come from
// somewhere else.
package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

// admit refuses a request no door proved, refuses a state-changing one that came from another site,
// and records the caller for the handlers that scope what they show.
//
// Health is the one route in front of the door, on purpose: it names nothing, a supervisor and the
// tunnel's own health check read it, and a liveness probe that needs a credential turns a working
// console into a restart loop.
func (s *Server) admit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if !sameSite(r) {
			http.Error(w, "this request came from another site, and the console refuses to act on one", http.StatusForbidden)
			return
		}
		// Who it is, proven; the handlers that must scope what they show will read it from the door
		// again until the workspace ACL lands (ADR 0032 decision 4). What this file guarantees is the
		// part that is already true: no route runs for a request nothing proved.
		if _, err := s.door.Prove(r); err != nil {
			// 401 and the sentence the door wrote. There is no login page to send anyone to: the edge
			// does the signing in, before the request gets here (ADR 0033).
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameSite refuses a state-changing request whose Origin says another site made it.
//
// The console sets no cookie of its own, so SameSite protects nothing here: the edge's cookie
// authenticates a cross-site form POST perfectly well, and a door that opens for the attacker is not
// a door. A request with no Origin is allowed — the command line does not send one, and it is not the
// party this is aimed at — while a browser sends one on same-origin requests too, so the check costs
// a real page nothing.
func sameSite(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
