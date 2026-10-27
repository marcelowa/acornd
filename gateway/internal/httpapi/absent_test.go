package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// A build without the update tier answers every update route with "not configured" and a 503,
// rather than a nil dereference or an empty page that looks like "no updates". The check is in
// every handler for a reason, and a state nobody exercises is a state that rots.
func TestWithoutAnUpdateTierEveryUpdateRouteSaysSo(t *testing.T) {
	server := New(nil, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}, nil, Gate{})
	handler := server.Handler()

	routes := []struct{ method, path string }{
		{"GET", "/api/update/status"},
		{"GET", "/api/update/releases"},
		{"GET", "/api/update/releases/v1.0.0"},
		{"GET", "/api/update/notes?tags=v1,v2"},
		{"POST", "/api/update/check"},
		{"POST", "/api/update/apply"},
		{"POST", "/api/update/restart"},
		{"POST", "/api/update/rollback"},
		{"POST", "/api/update/settings"},
	}
	for _, route := range routes {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", route.method, route.path, recorder.Code)
			continue
		}
		if body := strings.TrimSpace(recorder.Body.String()); !strings.Contains(body, "updates are not configured") {
			t.Errorf("%s %s answered %q", route.method, route.path, body)
		}
	}

	// The switch that says the process is alive still answers: it is how a restart knows the new
	// build took over, and it must work in a build whose updater is absent.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Errorf("healthz = %d", recorder.Code)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"version":"unknown"`) {
		t.Errorf("a build with no updater does not know its version: %s", body)
	}
}
