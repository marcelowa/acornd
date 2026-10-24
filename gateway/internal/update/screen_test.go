package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What the update screen is told, and what the picker asks for. Both are small functions with a
// decision in them: a private repository is a different problem from an unreachable one, and a
// limit is clamped rather than trusted.

// An unreachable release check is a sentence, and which sentence depends on the cause: a private
// repository looks like a 404 and has its own fix, while anything else is a machine that could not
// ask.
func TestAnUnreachableCheckSaysWhichProblemItIs(t *testing.T) {
	t.Setenv("ACORNFALL_GITHUB_TOKEN", "")
	missing := releaseProblem(httpError("404 Not Found"))
	if !strings.Contains(missing, "ACORNFALL_GITHUB_TOKEN") {
		t.Errorf("a 404 with no token should name the token: %q", missing)
	}
	if !strings.Contains(missing, "no releases exist yet") {
		t.Errorf("a 404 may also mean there are none yet: %q", missing)
	}
	// With a token the 404 is not about authentication, so it is reported as it is.
	t.Setenv("ACORNFALL_GITHUB_TOKEN", "ghp_example")
	if got := releaseProblem(httpError("404 Not Found")); strings.Contains(got, "ACORNFALL_GITHUB_TOKEN") {
		t.Errorf("a 404 with a token is not a token problem: %q", got)
	}
	// Anything else is unreachable, with the reason kept.
	if got := releaseProblem(httpError("dial tcp: no route to host")); !strings.Contains(got, "unreachable") || !strings.Contains(got, "no route") {
		t.Errorf("an unreachable check should say so and keep the cause: %q", got)
	}
}

// A release's notes never change, so one read per tag is enough — and an empty tag is not a tag:
// caching one would put a blank entry in a page that asks by name.
func TestNotesAreCachedByTagAndBlankTagsAreNot(t *testing.T) {
	manager, _ := testManager(t)
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		_, _ = w.Write([]byte(releaseJSON("v0.9.0", "what changed")))
	}))
	defer server.Close()
	manager.latestURL = server.URL + "/o/r/releases/latest"
	manager.client = server.Client()

	manager.cacheNotes("", "should not be stored")
	if manager.notesCache != nil {
		t.Errorf("a blank tag made a cache: %+v", manager.notesCache)
	}
	manager.cacheNotes("v0.9.0", "what changed")
	if got := manager.notesCache["v0.9.0"]; got != "what changed" {
		t.Errorf("cache = %+v", manager.notesCache)
	}
	// A tag already in the cache is answered from it, without a second read.
	before := reads
	if got := manager.Notes(context.Background(), []string{"v0.9.0"}); got["v0.9.0"] != "what changed" {
		t.Errorf("notes = %+v", got)
	}
	if reads != before {
		t.Errorf("a cached tag was read again (%d reads)", reads-before)
	}
}

// The picker's limit is clamped rather than trusted: the screen asks for a page, and a caller
// asking for none or for a thousand gets a page.
func TestThePickerClampsItsLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		limit int
		want  int
	}{
		{"nothing asked for", 0, DefaultListLimit},
		{"a negative limit", -5, DefaultListLimit},
		{"a sensible page", 3, 3},
		{"more than the API allows", MaxListLimit + 100, MaxListLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, _ := testManager(t)
			var asked string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked = r.URL.Query().Get("per_page")
				_, _ = w.Write([]byte("[]"))
			}))
			defer server.Close()
			manager.latestURL = server.URL + "/o/r/releases/latest"
			manager.client = server.Client()
			if _, err := manager.List(context.Background(), test.limit, false); err != nil {
				t.Fatal(err)
			}
			if asked != itoa(test.want) {
				t.Errorf("per_page = %q, want %d", asked, test.want)
			}
		})
	}
}

// The three states a worker can be in, and what the screen says about each: the dev sources when
// this is a source checkout, the file's version when there is one, and a first boot when there is
// not.
func TestTheScreenSaysWhichWorkerThisIs(t *testing.T) {
	// A source checkout: no version file, so the version is dev.
	checkout := New(t.TempDir(), filepath.Join(t.TempDir(), "acornfall"))
	checkout.migrateDir = t.TempDir()
	checkout.client = serverlessClient()
	if err := checkout.Open(); err != nil {
		t.Fatal(err)
	}
	if got := checkout.Preflight(context.Background()).Worker; got != "dev (repo sources)" {
		t.Errorf("a dev checkout should say so, got %q", got)
	}

	// A release install: the worker's version comes from its file.
	versioned, _ := testManager(t)
	// The version file lives in a folder the installer makes, so the test makes it too.
	if err := os.MkdirAll(filepath.Dir(versioned.workerTag()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versioned.workerTag(), []byte("v1.2.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := versioned.Preflight(context.Background()).Worker; !strings.Contains(got, "v1.2.3") {
		t.Errorf("an installed worker should name its version, got %q", got)
	}

	// Nothing at all: the next boot fetches it, and the screen says that rather than nothing.
	empty, _ := testManager(t)
	if err := empty.SetCurrentVersion("v2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(empty.workerTag()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Preflight asks for releases; a client that cannot reach anything is the offline machine this
	// line is about.
	empty.client = unreachableClient()
	if got := empty.Preflight(context.Background()).Worker; !strings.Contains(got, "missing") {
		t.Errorf("a release with no worker should say it is missing, got %q", got)
	}
}

// serverlessClient is a client that answers every request with an error, which is what Preflight
// has to survive: the release check is the one part that needs a network.
func serverlessClient() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, httpError("offline")
	})}
}

func unreachableClient() *http.Client { return serverlessClient() }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// httpError is an error that reads like the one the HTTP client produces, because releaseProblem
// decides on its text.
func httpError(text string) error { return &textError{text} }

type textError struct{ text string }

func (e *textError) Error() string { return e.text }

// itoa keeps the expectation readable in the failure message.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
