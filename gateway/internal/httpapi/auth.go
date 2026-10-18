package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

const (
	tokenCookie = "acornfall_token"
	tokenHeader = "Authorization"
)

// ResolveToken returns the shared secret guarding this listener, or "" when none was
// given. The console listens on every interface and needs no token by default; a token
// passed as a flag or an environment variable is what turns the guard on.
func ResolveToken(explicit, env string) string {
	if explicit != "" {
		return explicit
	}
	return env
}

// RequireToken guards a handler. A token may arrive as a bearer header, a
// cookie, or a ?token= query parameter; the query form sets the cookie and then
// redirects the page so the secret does not linger in the address bar. EventSource
// cannot set headers, which is why the cookie exists.
func RequireToken(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryToken := r.URL.Query().Get("token")
		if !matches(r, queryToken, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="acornfall"`)
			http.Error(w, "unauthorized: open the URL once with ?token=<token>", http.StatusUnauthorized)
			return
		}
		if queryToken != "" {
			http.SetCookie(w, &http.Cookie{
				Name:     tokenCookie,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   365 * 24 * 60 * 60,
			})
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func matches(r *http.Request, queryToken, token string) bool {
	candidates := []string{queryToken}
	if header := r.Header.Get(tokenHeader); header != "" {
		candidates = append(candidates, strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
	}
	if cookie, err := r.Cookie(tokenCookie); err == nil {
		candidates = append(candidates, cookie.Value)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			return true
		}
	}
	return false
}
