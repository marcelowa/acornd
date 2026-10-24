package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeReleases is github's release API as this build reads it: the newest release, a page of
// them, and one by tag. The gateway under test is a child process pointed at it by
// ACORNFALL_UPDATE_URL, so the update screen is driven end to end without reaching github.
func fakeReleases(t *testing.T) *httptest.Server {
	t.Helper()
	asset := func(id int, name string, size int) map[string]any {
		return map[string]any{"id": id, "name": name, "size": size, "browser_download_url": "https://example.invalid/" + name}
	}
	release := func(tag string, prerelease bool) map[string]any {
		return map[string]any{
			"tag_name":     tag,
			"name":         tag,
			"body":         "what changed in " + tag,
			"published_at": "2026-10-20T10:00:00Z",
			"prerelease":   prerelease,
			"assets": []any{
				asset(1, "acornfall-linux-amd64", 1000),
				asset(2, "worker.tar.gz", 200),
			},
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_ = json.NewEncoder(w).Encode(release("v9.9.9", false))
		case strings.HasSuffix(r.URL.Path, "/releases"):
			_ = json.NewEncoder(w).Encode([]any{release("v9.9.9", false), release("v9.10.0-rc.1", true)})
		case strings.Contains(r.URL.Path, "/releases/tags/"):
			if strings.HasSuffix(r.URL.Path, "nope") {
				http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(release("v1.0.0", false))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// withUpdateSource points the gateway at a fake github for the length of one test.
func withUpdateSource(t *testing.T) {
	t.Helper()
	server := fakeReleases(t)
	previous := os.Getenv("ACORNFALL_UPDATE_URL")
	// The full latest-release URL, not the base: the manager derives the API base by cutting at
	// `/releases/`, so a base here would silently fall back to github.
	if err := os.Setenv("ACORNFALL_UPDATE_URL", server.URL+"/repos/owner/repo/releases/latest"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("ACORNFALL_UPDATE_URL", previous) })
}

// The update screen is answered from the release API and says what it knows: the version running,
// what was found, whether it is newer, and the state of the switch. A screen that cannot tell
// "no update" from "never checked" is one nobody can act on.
func TestTheUpdateScreenAnswersFromTheReleaseAPI(t *testing.T) {
	withUpdateSource(t)
	s := start(t)
	c := s.client()

	var status struct {
		Current    string `json:"current"`
		Latest     string `json:"latest"`
		Available  bool   `json:"available"`
		Channel    string `json:"channel"`
		AutoCheck  bool   `json:"autoCheck"`
		Backups    []any  `json:"backups"`
		RunsActive bool   `json:"runsActive"`
	}
	c.get("/api/update/status", &status)
	if status.Current == "" {
		t.Error("the status should name the version running")
	}
	if status.Channel != "releases" {
		t.Errorf("channel = %q", status.Channel)
	}

	// Checking fetches the latest release and answers with the same shape as the status.
	var checked struct {
		Latest    string `json:"latest"`
		Available bool   `json:"available"`
	}
	c.post("/api/update/check", nil, &checked)
	if checked.Latest != "v9.9.9" {
		t.Errorf("check found %q, want what the API served", checked.Latest)
	}
	// This is a development build: it has no version to compare against, so a release is never
	// "available" to it and the screen says so rather than offering a downgrade of unknown shape.
	if checked.Available {
		t.Error("a dev build should not be offered an update it cannot compare itself against")
	}

	// The health switch names the running version, which is how a restart knows the new build
	// took over.
	var health struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	c.get("/healthz", &health)
	if !health.OK || health.Version != status.Current {
		t.Errorf("health = %+v, status named %q", health, status.Current)
	}
}

// The picker is a page of releases, GA only unless the switch is on, and each row says what it is
// next to the running build. A list that shows an rc to a stable machine is a one-click install of
// an rc.
func TestTheReleasePickerShowsStableAndSaysTheRelation(t *testing.T) {
	withUpdateSource(t)
	s := start(t)
	c := s.client()

	var stable struct {
		Releases []struct {
			Tag          string `json:"tag"`
			Prerelease   bool   `json:"prerelease"`
			Relation     string `json:"relation"`
			DownloadSize int64  `json:"downloadSize"`
			WorkerSize   int64  `json:"workerSize"`
			Notes        string `json:"notes"`
		} `json:"releases"`
	}
	c.get("/api/update/releases", &stable)
	if len(stable.Releases) != 1 || stable.Releases[0].Tag != "v9.9.9" {
		t.Fatalf("the stable list is %+v", stable.Releases)
	}
	row := stable.Releases[0]
	if row.Relation != "unknown" {
		t.Errorf("relation = %q: a dev build is not comparable with a release", row.Relation)
	}
	if row.DownloadSize != 1000 || row.WorkerSize != 200 {
		t.Errorf("the sizes for this machine: %d and %d", row.DownloadSize, row.WorkerSize)
	}
	if !strings.Contains(row.Notes, "what changed") {
		t.Errorf("notes = %q", row.Notes)
	}

	var withPrerelease struct {
		Releases []struct {
			Tag string `json:"tag"`
		} `json:"releases"`
	}
	c.get("/api/update/releases?prerelease=1", &withPrerelease)
	if len(withPrerelease.Releases) != 2 {
		t.Errorf("asking for prereleases shows them: %+v", withPrerelease.Releases)
	}

	// A limit that is not a number is the caller's mistake, not a reason to answer with twenty.
	if status, body := c.do("GET", "/api/update/releases?limit=abc", nil); status != 400 {
		t.Errorf("limit=abc: %d %s", status, body)
	}
}

// One release by name answers for a tag the picker never listed, and a tag nobody published is a
// 404 rather than an empty release: the version page has to tell those apart.
func TestOneReleaseIsAnsweredByName(t *testing.T) {
	withUpdateSource(t)
	s := start(t)
	c := s.client()

	var release struct {
		Tag      string `json:"tag"`
		Relation string `json:"relation"`
	}
	c.get("/api/update/releases/v1.0.0", &release)
	if release.Tag != "v1.0.0" {
		t.Errorf("tag = %q", release.Tag)
	}
	if status, _ := c.do("GET", "/api/update/releases/nope", nil); status != 404 {
		t.Errorf("an unknown tag: %d, want 404", status)
	}
}

// The switch is releases-only: the tree-build channel is designed and not built, so the endpoint
// refuses it rather than storing a setting that changes nothing.
func TestTheUpdateSwitchOnlyTakesWhatItCanDo(t *testing.T) {
	withUpdateSource(t)
	s := start(t)
	c := s.client()

	if status, body := c.do("POST", "/api/update/settings", map[string]any{"channel": "tree"}); status != 400 {
		t.Errorf("an unimplemented channel: %d %s", status, body)
	}
	var answer struct {
		Channel   string `json:"channel"`
		AutoCheck bool   `json:"autoCheck"`
	}
	c.post("/api/update/settings", map[string]any{"channel": "releases", "autoCheck": true}, &answer)
	if answer.Channel != "releases" || !answer.AutoCheck {
		t.Errorf("settings answered %+v", answer)
	}
	// The setting outlives the request, which is the point of it.
	c.get("/api/update/status", &answer)
	if !answer.AutoCheck {
		t.Error("autoCheck should have been saved")
	}
}
