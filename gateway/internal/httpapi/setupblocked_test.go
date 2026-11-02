package httpapi

import (
	"io/fs"
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

// throughDoor is the screen a visitor's browser is: the assertion header Cloudflare Access adds. The stub door
// below answers for anybody, so the header only has to be there for this check to ask its question.
func throughDoor(request *http.Request) *http.Request {
	request.Header.Set(identity.AssertionHeader, "an-assertion")
	return request
}

// doorConsole is an install with an owner and one invited friend, and the files a finished setup would have.
func doorConsole(t *testing.T, caller identity.Caller, complete bool) http.Handler {
	t.Helper()
	dir := t.TempDir()
	book, err := people.Open(filepath.Join(dir, people.File))
	if err != nil {
		t.Fatalf("book: %v", err)
	}
	if _, err := book.Claim("owner@example.com", "Owner"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := book.Add("friend@example.com", "Friend"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if complete {
		if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"openai":{"type":"api_key","key":"k"}}`), 0o600); err != nil {
			t.Fatalf("credentials: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"defaultModel":{"provider":"openai","modelId":"gpt"}}`), 0o600); err != nil {
			t.Fatalf("settings: %v", err)
		}
	}
	gate := Gate{Door: stubDoor{caller: caller}, People: book, Dir: dir}
	return New(nil, fs.FS(fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}), nil, gate).Handler()
}

// A visitor through the door before the install is finished gets one plain page, not a half-configured console.
func TestAVisitorGetsSetupInProgressBeforeTheInstallIsFinished(t *testing.T) {
	handler := doorConsole(t, identity.Caller{Email: "friend@example.com"}, false)
	for _, path := range []string{"/", "/settings"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, throughDoor(httptest.NewRequest(http.MethodGet, path, nil)))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s = %d, want 503", path, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "Setup in progress") {
			t.Errorf("%s did not say setup is in progress: %q", path, recorder.Body.String())
		}
	}
	api := httptest.NewRecorder()
	handler.ServeHTTP(api, throughDoor(httptest.NewRequest(http.MethodGet, "/api/remote", nil)))
	if api.Code != http.StatusServiceUnavailable || !strings.Contains(api.Body.String(), "setup in progress") {
		t.Errorf("api = %d %q, want the same refusal", api.Code, api.Body.String())
	}
}

// The owner finishes the setup through the door, so the page is not for them.
func TestTheOwnerIsNotBlockedBySetupInProgress(t *testing.T) {
	handler := doorConsole(t, identity.Caller{Email: "owner@example.com"}, false)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, throughDoor(httptest.NewRequest(http.MethodGet, "/", nil)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "<!doctype html>") {
		t.Fatalf("the owner got %d %q, want the interface", recorder.Code, recorder.Body.String())
	}
}

// Once the provider, the model and the second person are there, the door serves the console to everybody in the book.
func TestAFinishedInstallNoLongerShowsSetupInProgress(t *testing.T) {
	handler := doorConsole(t, identity.Caller{Email: "friend@example.com"}, true)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, throughDoor(httptest.NewRequest(http.MethodGet, "/", nil)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("a finished install answered %d, want the interface", recorder.Code)
	}
}

// The LAN address is not this question: setup is done there, by the person who started the console.
func TestTheLanAddressIsNotGatedBySetup(t *testing.T) {
	handler := doorConsole(t, identity.Caller{Email: "friend@example.com"}, false)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("a request with no assertion answered %d, want the interface", recorder.Code)
	}
}
