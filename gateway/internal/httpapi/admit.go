// The door in front of every route: who is calling, and whether the request could have come from
// somewhere else.
package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/identity"
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
		caller, err := s.gate.Door.Prove(r)
		if err != nil {
			// 401 and the sentence the door wrote. There is no login page to send anyone to: the edge
			// does the signing in, before the request gets here (ADR 0033).
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		// A proof of an address is not permission to use this install: the owner names people, and the
		// ones not named are refused before a handler runs and before anything is written. The token is
		// the owner, and does not need a record to be let in (ADR 0032 §3).
		if !caller.Owner {
			if _, err := s.gate.People.SignIn(caller.Email); err != nil {
				// 403 rather than 401: the proof was good and this person may not come in. Signing in
				// again would not help, and a page that says so only sends them round the loop.
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(withCaller(r.Context(), s.who(caller))))
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

// callerKey carries who a proven request is from, in control's words, from the edge to the handlers that
// have to ask. Unexported, so the only way a caller gets there is a door proving it.
type callerKey struct{}

func withCaller(ctx context.Context, caller control.Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// callerOf is who this request is from. A request nothing proved is the single owner ADR 0020 describes:
// the gate is off, there is nobody to tell apart, and every path answers as it always did.
func callerOf(r *http.Request) control.Caller {
	if caller, ok := r.Context().Value(callerKey{}).(control.Caller); ok {
		return caller
	}
	return control.Caller{Owner: true}
}

// who turns a proof into control's own caller. The token proves the owner but not which address they are,
// and membership is written in terms of an address, so the book answers that one question.
func (s *Server) who(caller identity.Caller) control.Caller {
	who := control.Caller{Owner: caller.Owner, Email: caller.Email}
	if who.Owner && who.Email == "" && s.gate.People != nil {
		if owner, ok, err := s.gate.People.Owner(); err == nil && ok {
			who.Email = owner.Email
		}
	}
	return who
}

// owner wraps a route that is the install's own: its workspaces' lifecycle, its credentials, its policy. A
// person is refused with 403 here rather than the 404 a workspace answers, because these routes are not
// about a workspace somebody might be a member of — they are about an install they do not own.
func (s *Server) owner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !callerOf(r).Owner {
			http.Error(w, "this is the install's own business, and only its owner may do it", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
