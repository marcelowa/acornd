package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/marcelowa/acornd/gateway/internal/identity"
	"github.com/marcelowa/acornd/gateway/internal/people"
)

// console is a server with the door a run builds over a temporary install folder. A gate that answers by
// rule would prove nothing here: this file is about the token, the setup code and the cookie, so the door
// is the real one.
type console struct {
	gate    Gate
	dir     string
	token   string
	code    string
	handler http.Handler
}

func newConsole(t *testing.T) *console {
	t.Helper()
	dir := t.TempDir()
	token, _, err := identity.EnsureToken(dir)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	code, _, err := identity.EnsureSetupCode(dir)
	if err != nil {
		t.Fatalf("setup code: %v", err)
	}
	book, err := people.Open(filepath.Join(dir, people.File))
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	gate := Gate{
		Door:   identity.Any(identity.Token{Dir: dir}, identity.Session{Dir: dir}),
		People: book,
		Dir:    dir,
	}
	return &console{
		gate:    gate,
		dir:     dir,
		token:   token,
		code:    code,
		handler: New(nil, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}, nil, gate).Handler(),
	}
}

// do sends one request, with whatever cookies the browser would have.
func (c *console) do(t *testing.T, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1:7777"+path, strings.NewReader(body))
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, request)
	return recorder
}

// claimMints is the cookie a response handed the browser, or nil when it handed none.
func cookieOf(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == identity.SessionCookie {
			return cookie
		}
	}
	return nil
}

// sessionDoc is the answer the three session routes share.
type sessionDoc struct {
	Setup    bool   `json:"setup"`
	Owner    string `json:"owner"`
	Email    string `json:"email"`
	SignedIn bool   `json:"signedIn"`
}

func sessionOf(t *testing.T, recorder *httptest.ResponseRecorder) sessionDoc {
	t.Helper()
	var doc sessionDoc
	if err := json.Unmarshal(recorder.Body.Bytes(), &doc); err != nil {
		t.Fatalf("the answer is not a session document: %v (%s)", err, recorder.Body.String())
	}
	return doc
}

// A fresh install is closed and says so: nobody owns it, the browser is nobody, and the interface has one
// question to ask before anything else (ADR 0036 §3).
func TestAFreshInstallSaysSetup(t *testing.T) {
	console := newConsole(t)
	recorder := console.do(t, http.MethodGet, "/api/session", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	doc := sessionOf(t, recorder)
	if !doc.Setup {
		t.Error("a fresh install does not say it needs setting up")
	}
	if doc.Owner != "" || doc.Email != "" {
		t.Errorf("owner = %q, email = %q; want nobody", doc.Owner, doc.Email)
	}
}

// An API route nobody may reach yet answers 401 with the header that tells the interface to show the claim
// form, and one sentence, because the screen is the next slice.
func TestAnAnonymousApiRequestDuringSetupSaysSetupRequired(t *testing.T) {
	console := newConsole(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/update/status"},
		{http.MethodPost, "/api/workspaces"},
		{http.MethodGet, "/api/people"},
	} {
		recorder := console.do(t, route.method, route.path, "{}")
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", route.method, route.path, recorder.Code)
			continue
		}
		if got := recorder.Header().Get(setupHeader); got != "1" {
			t.Errorf("%s %s answered %q, want %s: 1", route.method, route.path, got, setupHeader)
		}
		if body := strings.TrimSpace(recorder.Body.String()); body != "setup required" {
			t.Errorf("%s %s body = %q, want setup required", route.method, route.path, body)
		}
	}
}

// The interface has to load before it can draw a claim form, so every page is served: a page is a document,
// not the install's business.
func TestTheInterfaceLoadsDuringSetup(t *testing.T) {
	console := newConsole(t)
	recorder := console.do(t, http.MethodGet, "/", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want the interface", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "<!doctype html>") {
		t.Errorf("GET / served %q, want the client's document", recorder.Body.String())
	}
}

// The document is public behind the door too, because it draws the sign-in: the bundle carries no install
// data, and a browser that cannot load it has nowhere to type the token.
func TestTheInterfaceLoadsBehindTheDoor(t *testing.T) {
	console := newConsole(t)
	if claimed := console.do(t, http.MethodPost, "/api/setup/claim", `{"code":"`+console.code+`","email":"marcelo@example.com"}`); claimed.Code != http.StatusOK {
		t.Fatalf("claim = %d (%s)", claimed.Code, claimed.Body.String())
	}
	recorder := console.do(t, http.MethodGet, "/", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want the interface", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "<!doctype html>") {
		t.Errorf("GET / served %q, want the client's document", recorder.Body.String())
	}
}

// `acornd start` prints /setup?code=…, and the browser has to be able to load the document that
// draws the claim form before it holds a cookie. The page is public; the claim route behind it is
// the one that asks for the code (ADR 0036 §3).
func TestSetupServesTheInterfaceWithoutAProof(t *testing.T) {
	console := newConsole(t)
	recorder := console.do(t, http.MethodGet, "/setup?code="+console.code, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /setup = %d, want the interface", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "<!doctype html>") {
		t.Errorf("GET /setup served %q, want the client's document", recorder.Body.String())
	}
}

// Health stays in front of the door in every mode: a supervisor and the tunnel read it, and a probe that
// needs a credential turns a working console into a restart loop.
func TestHealthAnswersWithoutAProofDuringSetup(t *testing.T) {
	console := newConsole(t)
	if recorder := console.do(t, http.MethodGet, "/healthz", ""); recorder.Code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", recorder.Code)
	}
}

// The one POST that makes an owner: the code proves the person holds the machine, the book records them,
// and the answer is the person with the cookie that keeps this browser in.
func TestTheSetupCodeClaimsTheInstall(t *testing.T) {
	console := newConsole(t)
	body := `{"code":"` + strings.ToLower(console.code) + `","email":"marcelo@example.com","name":"Marcelo"}`
	recorder := console.do(t, http.MethodPost, "/api/setup/claim", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("claim = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "marcelo@example.com") {
		t.Errorf("the claim did not answer the person: %s", recorder.Body.String())
	}
	cookie := cookieOf(t, recorder)
	if cookie == nil {
		t.Fatal("claiming did not mint a session cookie")
	}
	if !cookie.HttpOnly || cookie.Path != "/" {
		t.Errorf("the cookie is not the browser's copy of the proof: %+v", cookie)
	}

	// The browser is somebody now, and the install has an owner.
	signed := console.do(t, http.MethodGet, "/api/session", "", cookie)
	doc := sessionOf(t, signed)
	if doc.Setup {
		t.Error("the install still says it needs setting up after being claimed")
	}
	if doc.Owner != "marcelo@example.com" || doc.Email != "marcelo@example.com" {
		t.Errorf("owner = %q, email = %q; want the person who claimed it", doc.Owner, doc.Email)
	}
	// And the anonymous API is a door now, with the door's own sentence rather than the setup one.
	anonymous := console.do(t, http.MethodGet, "/api/update/status", "")
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", anonymous.Code)
	}
	if anonymous.Header().Get(setupHeader) != "" {
		t.Error("a claimed install still answers as one that needs setting up")
	}
	if body := anonymous.Body.String(); strings.Contains(body, "setup required") {
		t.Errorf("the refusal is the setup one after the claim: %q", body)
	}
}

// The code is a one-time claim, and the check is the book rather than a deleted file: a second POST is a
// conflict, not a second owner (ADR 0036 §3).
func TestClaimingTwiceIsRefused(t *testing.T) {
	console := newConsole(t)
	claim := `{"code":"` + console.code + `","email":"marcelo@example.com","name":"Marcelo"}`
	first := console.do(t, http.MethodPost, "/api/setup/claim", claim)
	if first.Code != http.StatusOK {
		t.Fatalf("first claim = %d (%s)", first.Code, first.Body.String())
	}
	// The second attempt arrives from the browser that claimed it, which is the only caller that can reach
	// the route now: the install is somebody's, and the door is standing.
	second := `{"code":"` + console.code + `","email":"someone@example.com","name":"Someone"}`
	recorder := console.do(t, http.MethodPost, "/api/setup/claim", second, cookieOf(t, first))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("second claim = %d, want 409 (%s)", recorder.Code, recorder.Body.String())
	}
	admin, found, err := console.gate.People.Admin()
	if err != nil || !found {
		t.Fatalf("admin = %v, %v", found, err)
	}
	if admin.Email != "marcelo@example.com" {
		t.Errorf("the second claim replaced the first owner: %s", admin.Email)
	}
}

// A code that does not match claims nothing, and the install is still nobody's.
func TestAWrongSetupCodeClaimsNothing(t *testing.T) {
	console := newConsole(t)
	recorder := console.do(t, http.MethodPost, "/api/setup/claim", `{"code":"AAAA-AAAA","email":"marcelo@example.com"}`)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("claim = %d, want 401 (%s)", recorder.Code, recorder.Body.String())
	}
	if cookieOf(t, recorder) != nil {
		t.Error("a wrong code minted a cookie")
	}
	if _, found, err := console.gate.People.Admin(); err != nil || found {
		t.Fatalf("a wrong code claimed the install: %v, %v", found, err)
	}
}

// The address is the identity everything downstream keys on, so a typo is a 400 with the book's own
// sentence rather than a record nobody can sign in as.
func TestAClaimNeedsAnAddress(t *testing.T) {
	console := newConsole(t)
	recorder := console.do(t, http.MethodPost, "/api/setup/claim", `{"code":"`+console.code+`","email":"not-an-address"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("claim = %d, want 400 (%s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "is not an email address") {
		t.Errorf("the refusal does not say what is wrong: %q", recorder.Body.String())
	}
	if _, found, err := console.gate.People.Admin(); err != nil || found {
		t.Fatalf("a bad address claimed the install: %v, %v", found, err)
	}
}

// The install token is the same proof on the same form: a rotated or disabled code is never a lockout
// (ADR 0036 §3).
func TestTheInstallTokenSignsIn(t *testing.T) {
	console := newConsole(t)
	recorder := console.do(t, http.MethodPost, "/api/session", `{"secret":"`+console.token+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("sign in = %d (%s)", recorder.Code, recorder.Body.String())
	}
	cookie := cookieOf(t, recorder)
	if cookie == nil {
		t.Fatal("the token signed in with no cookie")
	}
	// A token names no address, so `email` cannot be how a screen knows it is signed in.
	if doc := sessionOf(t, recorder); !doc.SignedIn || doc.Email != "" {
		t.Fatalf("after the token signed in: %+v, want signedIn with no address", doc)
	}
	if state := console.do(t, http.MethodGet, "/api/session", "", cookie); state.Code != http.StatusOK {
		t.Fatalf("session state = %d", state.Code)
	} else if doc := sessionOf(t, state); !doc.SignedIn {
		t.Fatalf("the cookie did not read back as signed in: %+v", doc)
	}
	// A browser holding the install token is the admin, and reaches a handler that only an admin may.
	if reached := console.do(t, http.MethodGet, "/api/update/status", "", cookie); reached.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", reached.Code)
	}
}

// The setup code is the third secret on that form, and it works only while the install has no owner: it
// proves ownership, which is exactly what there is none of yet.
func TestTheSetupCodeSignsInUntilSomebodyOwnsTheInstall(t *testing.T) {
	console := newConsole(t)
	recorder := console.do(t, http.MethodPost, "/api/session", `{"secret":"`+console.code+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("sign in = %d (%s)", recorder.Code, recorder.Body.String())
	}
	cookie := cookieOf(t, recorder)
	if cookie == nil {
		t.Fatal("the setup code signed in with no cookie")
	}
	if reached := console.do(t, http.MethodGet, "/api/update/status", "", cookie); reached.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the handler's 503", reached.Code)
	}

	// Somebody claims the install, and the code stops being a way in at once.
	if claimed := console.do(t, http.MethodPost, "/api/setup/claim", `{"code":"`+console.code+`","email":"marcelo@example.com"}`); claimed.Code != http.StatusOK {
		t.Fatalf("claim = %d (%s)", claimed.Code, claimed.Body.String())
	}
	after := console.do(t, http.MethodPost, "/api/session", `{"secret":"`+console.code+`"}`)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("the code still signs in after a claim: %d (%s)", after.Code, after.Body.String())
	}
}

// A bogus secret is refused with 401, and the sentence names the two commands that print what would work.
func TestASecretThatIsNeitherIsRefused(t *testing.T) {
	console := newConsole(t)
	recorder := console.do(t, http.MethodPost, "/api/session", `{"secret":"guess"}`)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("sign in = %d, want 401", recorder.Code)
	}
	if cookieOf(t, recorder) != nil {
		t.Error("a wrong secret minted a cookie")
	}
	if !strings.Contains(recorder.Body.String(), "acornd token") {
		t.Errorf("the refusal does not say where the secret comes from: %q", recorder.Body.String())
	}
}

// Signing out is the browser dropping the cookie, and the answer is the state afterwards: nobody.
func TestSigningOutClearsTheCookie(t *testing.T) {
	console := newConsole(t)
	cookie, err := (identity.Session{Dir: console.dir}).Mint("marcelo@example.com", true)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	recorder := console.do(t, http.MethodDelete, "/api/session", "", cookie)
	if recorder.Code != http.StatusOK {
		t.Fatalf("sign out = %d", recorder.Code)
	}
	cleared := cookieOf(t, recorder)
	if cleared == nil {
		t.Fatal("signing out did not hand the browser a cookie to drop")
	}
	if cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("the clearing cookie keeps the session: %+v", cleared)
	}
	if doc := sessionOf(t, recorder); doc.Email != "" || doc.SignedIn {
		t.Errorf("after signing out: %+v, want nobody and not signed in", doc)
	}
}

// Once the install has an owner the door is the rule, with one exception: the sign-in routes, which are how
// a browser that lost its cookie gets one back (ADR 0036 §4).
func TestTheDoorStandsOnceTheInstallIsClaimed(t *testing.T) {
	console := newConsole(t)
	if recorder := console.do(t, http.MethodPost, "/api/setup/claim", `{"code":"`+console.code+`","email":"marcelo@example.com"}`); recorder.Code != http.StatusOK {
		t.Fatalf("claim = %d (%s)", recorder.Code, recorder.Body.String())
	}
	// The document still loads, because it is what draws the sign-in; what closes is the API behind it.
	if recorder := console.do(t, http.MethodGet, "/", ""); recorder.Code != http.StatusOK {
		t.Errorf("GET / = %d, want the interface once the install is closed", recorder.Code)
	}
	if recorder := console.do(t, http.MethodGet, "/api/state", ""); recorder.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/state = %d, want 401 once the install is closed", recorder.Code)
	}
	if recorder := console.do(t, http.MethodGet, "/api/session", ""); recorder.Code != http.StatusOK {
		t.Errorf("GET /api/session = %d, want the sign-in screen's question answered", recorder.Code)
	}
	if recorder := console.do(t, http.MethodDelete, "/api/session", ""); recorder.Code != http.StatusOK {
		t.Errorf("DELETE /api/session = %d, want 200", recorder.Code)
	}
	if recorder := console.do(t, http.MethodPost, "/api/setup/claim", `{"code":"`+console.code+`","email":"someone@example.com"}`); recorder.Code != http.StatusUnauthorized {
		t.Errorf("the claim route answered without a proof on a claimed install: %d", recorder.Code)
	}
}

// The cookie is the browser's copy of a proof that arrived over HTTPS, so Secure says how it got here: TLS on
// this connection, or the proxy in front saying so (ADR 0036 §4).
func TestTheSessionCookieIsSecureWhenTheRequestWasHTTPS(t *testing.T) {
	cases := []struct {
		name  string
		url   string
		proto string
		want  bool
	}{
		{"plain http", "http://127.0.0.1:7777", "", false},
		{"tls here", "https://127.0.0.1:7777", "", true},
		{"behind a proxy", "http://127.0.0.1:7777", "https", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			console := newConsole(t)
			request := httptest.NewRequest(http.MethodPost, test.url+"/api/session", strings.NewReader(`{"secret":"`+console.token+`"}`))
			if test.proto != "" {
				request.Header.Set("X-Forwarded-Proto", test.proto)
			}
			recorder := httptest.NewRecorder()
			console.handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("sign in = %d (%s)", recorder.Code, recorder.Body.String())
			}
			cookie := cookieOf(t, recorder)
			if cookie == nil {
				t.Fatal("no cookie")
			}
			if cookie.Secure != test.want {
				t.Errorf("Secure = %v, want %v", cookie.Secure, test.want)
			}
		})
	}
}

// A cookie a browser already holds answers the sign-in screen's third question, without asking for anything:
// the screen shows who you are, not a form to type your name into again.
func TestASessionCookieNamesTheBrowser(t *testing.T) {
	console := newConsole(t)
	cookie, err := (identity.Session{Dir: console.dir}).Mint("ana@example.com", false)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	doc := sessionOf(t, console.do(t, http.MethodGet, "/api/session", "", cookie))
	if doc.Email != "ana@example.com" {
		t.Errorf("email = %q, want ana", doc.Email)
	}
	// A tampered one is nobody rather than an error: this route answers a screen, and the door is what
	// refuses.
	tampered := &http.Cookie{Name: identity.SessionCookie, Value: "edited"}
	if doc := sessionOf(t, console.do(t, http.MethodGet, "/api/session", "", tampered)); doc.Email != "" {
		t.Errorf("a cookie this install did not sign named somebody: %q", doc.Email)
	}
}

// A book nobody can read is not a fresh install: setup is the larger authority of the two, so a damaged file
// closes the console rather than opening the claim form (ADR 0036 §3).
func TestABookNobodyCanReadDoesNotOpenSetup(t *testing.T) {
	console := newConsole(t)
	if err := writeBrokenBook(console.dir); err != nil {
		t.Fatalf("break the book: %v", err)
	}
	recorder := console.do(t, http.MethodGet, "/api/update/status", "")
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get(setupHeader) != "" {
		t.Fatalf("a broken book answered %d with %q, want the door's own 401", recorder.Code, recorder.Header().Get(setupHeader))
	}
	// Claiming over it reaches the handler with a proof and is refused by the book, not by the door.
	cookie, err := (identity.Session{Dir: console.dir}).Mint("", true)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claim := console.do(t, http.MethodPost, "/api/setup/claim", `{"code":"`+console.code+`","email":"marcelo@example.com"}`, cookie)
	if claim.Code != http.StatusConflict {
		t.Fatalf("claiming over a broken book = %d, want 409 (%s)", claim.Code, claim.Body.String())
	}
}

// writeBrokenBook leaves a people.json that no reader can parse.
func writeBrokenBook(dir string) error {
	return os.WriteFile(filepath.Join(dir, people.File), []byte("not a book"), 0o600)
}

// An install folder that cannot say what its credentials are answers with the reason rather than a 401
// that blames the person typing: the secret cannot be compared with a token file that is not there.
func TestSigningInWithoutATokenFileIsTheInstallsProblem(t *testing.T) {
	console := newConsole(t)
	if err := os.Remove(filepath.Join(console.dir, identity.TokenFile)); err != nil {
		t.Fatalf("remove token: %v", err)
	}
	recorder := console.do(t, http.MethodPost, "/api/session", `{"secret":"anything"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("sign in = %d, want 500 (%s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "token") {
		t.Errorf("the refusal does not name what is missing: %q", recorder.Body.String())
	}
}

// The sign-in screen's first question is answerable on a gate with no install folder at all: it reports
// an install somebody would still have to set up, and nobody behind the cookie it cannot read.
func TestASessionStateWithoutAnInstallFolder(t *testing.T) {
	handler := admitted(t, Gate{})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/session", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if doc := sessionOf(t, recorder); doc.Setup || doc.Owner != "" || doc.Email != "" {
		t.Errorf("a gate with no folder answered %+v", doc)
	}
}

// A claim against a gate with no book has nowhere to record an owner, and says so rather than succeeding
// at nothing.
func TestAClaimWithoutABookIsRefused(t *testing.T) {
	console := newConsole(t)
	gate := Gate{
		Door:   console.gate.Door,
		People: nil,
		Dir:    console.dir,
	}
	handler := New(nil, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}, nil, gate).Handler()
	request := httptest.NewRequest(http.MethodPost, "/api/setup/claim", strings.NewReader(`{"code":"`+console.code+`","email":"marcelo@example.com"}`))
	request.Header.Set("Authorization", "Bearer "+console.token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("claim = %d, want 409 (%s)", recorder.Code, recorder.Body.String())
	}
}

// A code file that cannot be read is the install's problem, not a wrong code: the claim answers with the
// reason instead of sending somebody to retype a code that is not the matter.
func TestAClaimWithABrokenCodeFileIsTheInstallsProblem(t *testing.T) {
	console := newConsole(t)
	if err := os.Remove(filepath.Join(console.dir, identity.SetupCodeFile)); err != nil {
		t.Fatalf("remove code: %v", err)
	}
	if err := os.Mkdir(filepath.Join(console.dir, identity.SetupCodeFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	recorder := console.do(t, http.MethodPost, "/api/setup/claim", `{"code":"ABCD-EFGH","email":"marcelo@example.com"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("claim = %d, want 500 (%s)", recorder.Code, recorder.Body.String())
	}
}
