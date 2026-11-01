// the sign-in routes: the owner's cookie, the install token, and the code that claims a fresh install
//
// Part of the httpapi package. ADR 0036 §3–§4: until an admin exists the console is closed to every
// request without a proof, and these are the routes that make an owner and hand that owner the
// cookie. The identity work is `identity`'s; this file is the order the questions are asked in.
package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/marcelowa/acornd/gateway/internal/identity"
	"github.com/marcelowa/acornd/gateway/internal/people"
)

func (s *Server) sessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/session", s.sessionState)
	mux.HandleFunc("POST /api/session", s.sessionSignIn)
	mux.HandleFunc("DELETE /api/session", s.sessionSignOut)
	mux.HandleFunc("POST /api/setup/claim", s.setupClaim)
}

// sessionState is what the sign-in screen asks first: is this install claimed, who owns it, and is
// this browser already somebody. `email` is the cookie's answer and not the door's, because a request
// proved by an assertion has a person behind it and no session of ours.
func (s *Server) sessionState(w http.ResponseWriter, r *http.Request) {
	email, signedIn := "", false
	if s.gate.Dir != "" {
		if caller, err := (identity.Session{Dir: s.gate.Dir}).Prove(r); err == nil {
			// A cookie minted from the token proves the owner and names no address, so `email` alone
			// cannot answer "is this browser signed in" — the screen needs both facts.
			email, signedIn = caller.Email, true
		}
	}
	writeJSON(w, http.StatusOK, s.sessionAnswer(email, signedIn))
}

// sessionAnswer is the shape all three routes answer with, so a screen never has to know which one it
// called to know what it is looking at.
func (s *Server) sessionAnswer(email string, signedIn bool) map[string]any {
	owner := ""
	if s.gate.People != nil {
		if admin, found, err := s.gate.People.Admin(); err == nil && found {
			owner = admin.Email
		}
	}
	return map[string]any{
		"setup":    s.gate.mode() == modeSetup,
		"owner":    owner,
		"email":    email,
		"signedIn": signedIn,
	}
}

// sessionSignIn turns a secret into a cookie. The install token always works, and the setup code
// works while nobody owns the install: both prove ownership, which is the whole of what a session
// says (ADR 0036 §3). The cookie carries no address because neither proof names one — the book names
// the admin, and `who` asks it.
func (s *Server) sessionSignIn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Secret string `json:"secret"`
	}
	if !decode(w, r, &body) {
		return
	}
	secret := strings.TrimSpace(body.Secret)
	if secret == "" {
		http.Error(w, "no secret in the body: the install token, or the setup code", http.StatusBadRequest)
		return
	}
	ours, err := s.secretProvesOwnership(secret)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ours {
		http.Error(w, "that is not this install's token or its setup code: read it again with `acornd token`", http.StatusUnauthorized)
		return
	}
	if err := s.setSession(w, r, ""); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, s.sessionAnswer("", true))
}

// secretProvesOwnership is the two questions the sign-in form asks of one secret: is it the install token,
// and else is it the setup code while nobody owns the install? The order matters, because the token is the
// answer that never expires.
func (s *Server) secretProvesOwnership(secret string) (bool, error) {
	byToken, err := identity.MatchToken(s.gate.Dir, secret)
	if err != nil || byToken {
		return byToken, err
	}
	noOwner, err := s.gate.noOwner()
	if err != nil || !noOwner {
		return false, err
	}
	return identity.MatchSetupCode(s.gate.Dir, secret)
}

// setSession mints the owner's cookie for this browser and hands it to it, secured the way the request
// arrived. The caller's answer is written after it, so the browser has the cookie before it asks again.
func (s *Server) setSession(w http.ResponseWriter, r *http.Request, email string) error {
	cookie, err := (identity.Session{Dir: s.gate.Dir}).Mint(email, true)
	if err != nil {
		return err
	}
	cookie.Secure = identity.OverTLS(r)
	http.SetCookie(w, cookie)
	return nil
}

// sessionSignOut clears the cookie. The token and the setup code are untouched: signing out a browser
// is not revoking the install's ownership, and the same form signs it back in.
func (s *Server) sessionSignOut(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, identity.ClearSession(r))
	writeJSON(w, http.StatusOK, s.sessionAnswer("", false))
}

// setupClaim is the one POST that makes an owner: the setup code proves the person holds the machine
// the console runs on, the book records them as its admin, and the answer is the person with the
// cookie that keeps this browser in (ADR 0036 §3).
//
// The code is refused the moment an admin exists — the check is the book, not the file — so a code
// left on disk by an install that was claimed is not a way back in for whoever finds it.
func (s *Server) setupClaim(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code  string `json:"code"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !s.claimable(w) {
		return
	}
	matched, err := identity.MatchSetupCode(s.gate.Dir, body.Code)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !matched {
		http.Error(w, "that is not this install's setup code: read it again with `acornd setup-code`", http.StatusUnauthorized)
		return
	}
	// The book's own check, asked before the write so a typo is a 400 rather than a claim that half
	// happened.
	address, err := people.Address(body.Email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	person, err := s.gate.People.Claim(address, body.Name)
	if err != nil {
		if errors.Is(err, people.ErrHasAdmin) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), peopleStatus(err))
		return
	}
	if err := s.setSession(w, r, person.Email); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The book changed, so the door's group follows it like any other edit (ADR 0036 §7). Best effort
	// beside the write, and a no-op on an install with no remote access.
	go s.reconcileAfterPeople()
	writeJSON(w, http.StatusOK, person)
}

// claimable answers whether this install may still be claimed, writing the refusal itself when it may not:
// an install with an owner is a conflict, and a book nobody can read is not a fresh install (ADR 0036 §3).
func (s *Server) claimable(w http.ResponseWriter) bool {
	if s.gate.People == nil {
		http.Error(w, "this install has no book of people yet", http.StatusConflict)
		return false
	}
	noOwner, err := s.gate.noOwner()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return false
	}
	if !noOwner {
		http.Error(w, "this install already has an owner: sign in instead of claiming it", http.StatusConflict)
		return false
	}
	return true
}
