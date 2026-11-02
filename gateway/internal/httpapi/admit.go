// The door in front of every route: who is calling, and whether the request could have come from
// somewhere else.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/marcelowa/acornd/gateway/internal/control"
	"github.com/marcelowa/acornd/gateway/internal/identity"
	"github.com/marcelowa/acornd/gateway/internal/people"
)

// admit refuses a request no door proved, refuses a state-changing one that came from another site,
// and records the caller for the handlers that scope what they show.
//
// Health is the one route in front of the door, on purpose: it names nothing, a supervisor and the
// tunnel's own health check read it, and a liveness probe that needs a credential turns a working
// console into a restart loop.
//
// The routes that need no proof depend on what the gate is (ADR 0036 §3): an install nobody owns yet
// has to load its interface and answer the claim form, and an install with an owner still has to be
// able to sign a browser in before there is a cookie to check. A proof is a proof in every mode, so a
// token or a cookie reaches everything else in setup mode exactly as it does behind the door.
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
		settingUp := s.gate.mode() == modeSetup
		if withoutProof(settingUp, r) {
			next.ServeHTTP(w, r)
			return
		}
		caller, err := s.gate.Door.Prove(r)
		if err != nil {
			refuseProof(w, settingUp, err)
			return
		}
		caller, err = s.admitted(caller)
		if err != nil {
			// 403 rather than 401: the proof was good and this person may not come in. Signing in again
			// would not help, and a page that says so only sends them round the loop.
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(withCaller(r.Context(), s.who(caller))))
	})
}

// withoutProof reports whether this request reaches its handler with no proof, given the gate's mode. Setup
// is the one state with such routes: the interface has to load before it can draw a claim form, and signing
// in has to work before there is anything to sign in to. Once the install has an owner only the sign-in
// routes stay open, because a browser that lost its cookie could otherwise never get one back (ADR 0036
// §3–§4).
func withoutProof(settingUp bool, r *http.Request) bool {
	// The interface itself is public in every closed mode: it is a static bundle with no install data in
	// it, and it is what draws the claim form and the sign-in. Refusing it would leave a browser holding a
	// 401 with nothing to render — the API behind it is what stays closed.
	if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	if settingUp {
		return openDuringSetup(r)
	}
	return signInRoute(r)
}

// refuseProof is the 401 a request gets in one of the gate's two closed modes: the setup sentence while
// nobody owns the install, and the door's own words otherwise.
func refuseProof(w http.ResponseWriter, settingUp bool, err error) {
	if settingUp {
		// The interface reads this header to show the claim form rather than a sign-in it cannot answer
		// yet, and the body stays one sentence because the screen is the next slice.
		w.Header().Set(setupHeader, "1")
		http.Error(w, "setup required", http.StatusUnauthorized)
		return
	}
	// 401 and the sentence the door wrote. There is no login page to send anyone to: the edge does the
	// signing in, before the request gets here (ADR 0033).
	http.Error(w, err.Error(), http.StatusUnauthorized)
}

// admitted applies the book to a caller a door proved: a person must be named in it, and the address it
// names as the admin is the admin whatever door proved them. Without this the admin arrives through the
// same door as everybody else and is a lesser caller for it (ADR 0032 §3).
//
// A door with no book has no allowlist, and nobody passes that: the entry point always opens a book, so
// this is a gate built by a test that is about something else.
func (s *Server) admitted(caller identity.Caller) (identity.Caller, error) {
	if caller.Admin {
		return caller, nil
	}
	if s.gate.People == nil {
		return identity.Caller{}, errors.New("this install has no book of people yet, so it cannot know who may come in")
	}
	person, err := s.gate.People.SignIn(caller.Email)
	if err != nil {
		return identity.Caller{}, err
	}
	caller.Admin = person.Role == people.RoleAdmin
	return caller, nil
}

// sameSite refuses a state-changing request whose Origin says another site made it.
//
// The console's own cookie is SameSite=Lax, and the edge's is the edge's to configure; neither is a
// fact this door can see. A proof that arrives in a header — the install token — rides a cross-site
// form post perfectly well, and a door that opens for the attacker's page is not a door. A request
// with no Origin is allowed — the command line does not send one, and it is not the party this is
// aimed at — while a browser sends one on same-origin requests too, so the check costs a real page
// nothing.
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

// setupHeader marks a refusal that means "nobody owns this install yet" rather than "your proof was
// refused": the interface shows the claim form for the first and a sign-in for the second.
const setupHeader = "X-Acornd-Setup"

// openDuringSetup is what a caller may reach while nobody owns the install: the four routes that make
// an owner or sign one in, on top of the interface every closed mode serves. Everything else is the
// install's own business, and an install with no owner has no business to be about (ADR 0036 §3).
func openDuringSetup(r *http.Request) bool {
	switch r.Method + " " + r.URL.Path {
	case "GET /api/session", "POST /api/session", "DELETE /api/session", "POST /api/setup/claim":
		return true
	}
	return false
}

// signInRoute is how a browser becomes somebody again once the install has an owner: the sign-in form
// has to answer before there is a cookie to check, or a browser that lost its cookie could never get
// one back (ADR 0036 §4).
func signInRoute(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/session")
}

// callerKey carries who a proven request is from, in control's words, from the edge to the handlers that
// have to ask. Unexported, so the only way a caller gets there is a door proving it.
type callerKey struct{}

func withCaller(ctx context.Context, caller control.Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// callerOf is who this request is from. A request nothing proved is the single admin ADR 0020 describes:
// the gate is off, there is nobody to tell apart, and every path answers as it always did.
func callerOf(r *http.Request) control.Caller {
	if caller, ok := r.Context().Value(callerKey{}).(control.Caller); ok {
		return caller
	}
	return control.Caller{Admin: true}
}

// who turns a proof into control's own caller. The token proves the admin but not which address they are,
// and membership is written in terms of an address, so the book answers that one question.
func (s *Server) who(caller identity.Caller) control.Caller {
	who := control.Caller{Admin: caller.Admin, Email: caller.Email}
	if who.Admin && who.Email == "" && s.gate.People != nil {
		if admin, ok, err := s.gate.People.Admin(); err == nil && ok {
			who.Email = admin.Email
		}
	}
	return who
}

// admin wraps a route that is the install's own: its workspaces' lifecycle, its credentials, its policy. A
// person is refused with 403 here rather than the 404 a workspace answers, because these routes are not
// about a workspace somebody might be a member of — they are about an install they do not own.
func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !callerOf(r).Admin {
			http.Error(w, "this is the install's own business, and only its admin may do it", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// mayRun reports whether the caller may run a model, and writes the refusal when they may not.
//
// The admin has no list: the whole install is theirs. A person is bound by theirs, and the refusal names what
// they may run rather than saying "forbidden" — the person is family, and the fix is a sentence they can act
// on (ADR 0032 §6). An empty list is every model, which is what a person has until the admin narrows it.
//
// This is the control, not the whole of it: the picker still offers every model the install has, and filtering
// it is the convenience that comes after the refusal rather than instead of it.
func (s *Server) mayRun(w http.ResponseWriter, r *http.Request, provider, model string) bool {
	if provider == "" || model == "" {
		// Nothing is being assigned: an empty pair means "leave it alone", which is not a choice to check.
		return true
	}
	caller := callerOf(r)
	lists, ok := s.modelLists(caller)
	if !ok {
		http.Error(w, "the install's model list could not be read: ask the admin to look at it", http.StatusForbidden)
		return false
	}
	// One question, two lists inside it: the install's, which binds everybody including the admin, and the person's
	// own when they have one.
	if !lists.allows(provider, model) {
		http.Error(w, refusalFor(lists, provider, model), http.StatusForbidden)
		return false
	}
	return true
}

// mayRunInstall is the question a turn asks: may this install spend its key on this model? It is the install's
// list only, because a person's list governs choosing and not speaking — a guest runs the model the room was
// given by the member who pinned it (ADR 0034 §4).
func (s *Server) mayRunInstall(w http.ResponseWriter, r *http.Request, provider, model string) bool {
	lists, ok := s.modelLists(callerOf(r))
	if !ok {
		http.Error(w, "the install's model list could not be read: ask the admin to look at it", http.StatusForbidden)
		return false
	}
	if lists.install.ModelAllowed(provider, model) {
		return true
	}
	http.Error(w, refusalFor(modelLists{install: lists.install}, provider, model), http.StatusForbidden)
	return false
}

// qualified names models the way a person picks them: provider/model, which is also the form a list entry takes.
func qualified(provider string, models []string) string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		out = append(out, provider+"/"+model)
	}
	return strings.Join(out, ", ")
}

// refusalFor is the sentence a refusal carries: what this install allows, and — when the person has their own list —
// what they allow, so the person can pick one rather than guess.
func refusalFor(lists modelLists, provider, model string) string {
	install := qualified(provider, lists.install.Allowed(provider))
	if install == "" {
		install = "none of them"
	}
	if !lists.limited {
		return fmt.Sprintf("this install may not run %s/%s; %s allows %s", provider, model, provider, install)
	}
	personal := qualified(provider, lists.personalModels(provider))
	if personal == "" {
		personal = "none of them"
	}
	return fmt.Sprintf("you may not run %s/%s here; %s allows %s for this install and %s for you", provider, model, provider, install, personal)
}
