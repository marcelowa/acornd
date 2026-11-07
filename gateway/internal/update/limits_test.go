package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// "Newer" is a strict comparison: a build is not newer than itself, or the update screen offers
// an update to the release already running.
func TestABuildIsNotNewerThanItself(t *testing.T) {
	for _, tag := range []string{"v1.0.0", "v1.0.0-rc.1", "nightly"} {
		if newerThan(tag, tag) {
			t.Errorf("newerThan(%q, itself) = true", tag)
		}
	}
}

// The suffix is what follows the first dash, kept whole: a prerelease is a sequence, and a tag
// parsed one character late still sorts inside its own base, which is how a wrong parse hides.
func TestThePrereleaseSuffixIsTheWholeTail(t *testing.T) {
	cases := map[string]string{
		"v1.2.3-rc.1": "rc.1",
		"v1.2.3-beta": "beta",
		"v1.2.3":      "",
		"1.2.3-rc.10": "rc.10",
	}
	for tag, want := range cases {
		_, pre, ok := tagParts(tag)
		if !ok {
			t.Errorf("tagParts(%q) did not parse", tag)
			continue
		}
		if pre != want {
			t.Errorf("tagParts(%q) suffix = %q, want %q", tag, pre, want)
		}
	}
}

// The list asks for a page nobody offers beyond the cap, and hides prereleases unless they were
// asked for: a stable machine that is shown an rc and clicks it installs an rc.
func TestTheReleaseListIsCappedAndHidesPrereleases(t *testing.T) {
	asked := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v1.1.0", "prerelease": false},
			{"tag_name": "v1.2.0-rc.1", "prerelease": true},
			{"tag_name": "v1.0.0", "prerelease": false},
		})
	}))
	defer server.Close()
	manager, _ := testManager(t)
	manager.latestURL = server.URL + "/repos/owner/repo/releases/latest"
	manager.client = server.Client()

	stable, err := manager.List(context.Background(), 500, false)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "per_page=100" {
		t.Errorf("asked for %q, want the page cap", asked)
	}
	if len(stable) != 2 || stable[0].Tag != "v1.1.0" || stable[1].Tag != "v1.0.0" {
		t.Errorf("a stable list shows stable releases newest first: %+v", stable)
	}

	withPrereleases, err := manager.List(context.Background(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withPrereleases) != 3 || withPrereleases[0].Tag != "v1.2.0-rc.1" {
		t.Errorf("the rc is at the top when it is asked for: %+v", withPrereleases)
	}
	if asked != "per_page=20" {
		t.Errorf("a zero limit falls back to the default page, asked %q", asked)
	}
}

// A token in the environment is how a rate-limited machine authenticates, and it is sent as a
// header rather than in the URL, where it would land in logs.
func TestATokenInTheEnvironmentIsSentAsAHeader(t *testing.T) {
	t.Setenv("ACORND_GITHUB_TOKEN", "secret-token")
	var authorization, accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		accept = r.Header.Get("Accept")
		_, _ = w.Write([]byte(releaseJSON("v0.9.0", "")))
	}))
	defer server.Close()
	manager, _ := testManager(t)
	manager.latestURL = server.URL
	manager.client = server.Client()
	if _, err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer secret-token" {
		t.Errorf("Authorization = %q", authorization)
	}
	if accept == "" {
		t.Error("the request should name the API version it wants")
	}
}

// Without a token there is no Authorization header: an empty "Bearer " is a request GitHub
// refuses for a reason nobody can read.
func TestNoTokenMeansNoAuthorizationHeader(t *testing.T) {
	os.Unsetenv("ACORND_GITHUB_TOKEN")
	var present bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["Authorization"]
		_, _ = w.Write([]byte(releaseJSON("v0.9.0", "")))
	}))
	defer server.Close()
	manager, _ := testManager(t)
	manager.latestURL = server.URL
	manager.client = server.Client()
	if _, err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Error("no token, no Authorization header")
	}
}

// Two backups made in the same nanosecond must not share a folder, because rotation counts
// folders and two backups in one would silently be one. The clock is pinned to make that happen.
func TestTwoBackupsInTheSameInstantGetTheirOwnFolder(t *testing.T) {
	manager, dataDir := testManager(t)
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	frozen := time.Date(2026, 10, 27, 12, 0, 0, 123456789, time.UTC)
	manager.now = func() time.Time { return frozen }

	first, err := manager.Backup()
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Backup()
	if err != nil {
		t.Fatal(err)
	}
	stamp := "20261027-120000.123456789"
	if filepath.Base(first) != stamp {
		t.Errorf("the first backup = %q, want the stamp %q", filepath.Base(first), stamp)
	}
	if filepath.Base(second) != stamp+"-1" {
		t.Errorf("the second backup = %q, want %q", filepath.Base(second), stamp+"-1")
	}
	// Both have the files: one folder per backup, not one folder holding two.
	for _, dir := range []string{first, second} {
		if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
			t.Errorf("%s is missing its copy: %v", filepath.Base(dir), err)
		}
	}
}

// The notes cache is capped: a tag's notes never change, so it only grows, and a long-lived
// process must not carry every release it ever looked at.
func TestTheNotesCacheDoesNotGrowForever(t *testing.T) {
	manager, _ := testManager(t)
	manager.notesMu.Lock()
	manager.notesCache = map[string]string{}
	for index := range 201 {
		manager.notesCache[fmt.Sprintf("v1.0.%d", index)] = "notes"
	}
	manager.notesMu.Unlock()
	if len(manager.notesCache) <= 200 {
		t.Fatalf("the cache should be over its cap to test it, has %d", len(manager.notesCache))
	}
	// The next batch that fills notes is what trips the cap.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(releaseJSON("v0.9.0", "the notes")))
	}))
	defer server.Close()
	manager.latestURL = server.URL + "/repos/owner/repo/releases/latest"
	manager.client = server.Client()
	manager.Notes(context.Background(), []string{"v0.9.0"})
	manager.notesMu.Lock()
	size := len(manager.notesCache)
	manager.notesMu.Unlock()
	if size > 200 {
		t.Errorf("the cache is %d after the cap should have reset it", size)
	}
}

// The cache cap is exact: at two hundred entries it is still small enough to keep, and it is the
// two hundred and first that clears it. A cap that fires early throws away notes still being read.
func TestTheNotesCacheHoldsTwoHundred(t *testing.T) {
	manager, _ := testManager(t)
	manager.notesMu.Lock()
	manager.notesCache = map[string]string{}
	for index := range 200 {
		manager.notesCache[fmt.Sprintf("v1.0.%d", index)] = "notes"
	}
	manager.notesMu.Unlock()

	manager.latestURL = "http://127.0.0.1:1" // unreachable: nothing more is read
	manager.client = &http.Client{Timeout: time.Second}
	manager.Notes(context.Background(), []string{"v1.0.0"})

	manager.notesMu.Lock()
	size := len(manager.notesCache)
	manager.notesMu.Unlock()
	if size != 200 {
		t.Errorf("a cache of two hundred should be kept, has %d", size)
	}
}

// Same-nanosecond backups are numbered upward, so the next free name is found by counting and the
// folders sort the way the rotation reads them.
func TestTheNextBackupIsTheNextFreeNumber(t *testing.T) {
	manager, dataDir := testManager(t)
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 1, time.UTC) }
	first, err := manager.Backup()
	if err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Base(first)
	for _, name := range []string{stamp + "-1", stamp + "-2"} {
		if err := os.MkdirAll(filepath.Join(manager.backupsDir(), name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	second, err := manager.Backup()
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(second); got != stamp+"-3" {
		t.Errorf("second backup = %q, want %q", got, stamp+"-3")
	}
}

// A download carries the token too: a private mirror or a rate-limited API answers the metadata
// and then refuses the file, which reads as an update that can never be installed.
func TestADownloadCarriesTheTokenAndTheAcceptHeader(t *testing.T) {
	t.Setenv("ACORND_GITHUB_TOKEN", "secret-token")
	var authorization, accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		accept = r.Header.Get("Accept")
		_, _ = w.Write([]byte("the file"))
	}))
	defer server.Close()
	manager, _ := testManager(t)
	manager.client = server.Client()

	// An asset with an id is fetched through the API, which is where the Accept header is set.
	manager.latestURL = server.URL + "/repos/owner/repo/releases/latest"
	var body bytes.Buffer
	if err := manager.download(context.Background(), Asset{ID: 7, Name: "acornd"}, &body); err != nil {
		t.Fatal(err)
	}
	if body.String() != "the file" {
		t.Errorf("body = %q", body.String())
	}
	if authorization != "Bearer secret-token" {
		t.Errorf("Authorization = %q", authorization)
	}
	if accept != "application/octet-stream" {
		t.Errorf("Accept = %q", accept)
	}
}

// The release lookups can be pointed at a mirror, which is what a private install sets and what
// lets the update screen be driven against a server instead of github.
func TestTheReleaseSourceCanBeAMirror(t *testing.T) {
	t.Setenv("ACORND_UPDATE_URL", "https://mirror.example/repos/o/r/releases/latest")
	manager := New(t.TempDir(), "")
	if got := manager.apiBase(); got != "https://mirror.example/repos/o/r" {
		t.Errorf("apiBase = %q", got)
	}

	// A URL that does not carry the shape falls back to github rather than deriving a base from
	// nothing: a mistyped mirror is not a reason to ask github, but a base cannot be invented
	// either — and the fallback is what makes the mistake visible.
	t.Setenv("ACORND_UPDATE_URL", "https://mirror.example/whatever")
	if got := New(t.TempDir(), "").apiBase(); got != defaultAPIBase {
		t.Errorf("a URL without the release path should not become an API base, got %q", got)
	}
}

// A release asset is 21 MB and a link is not always fast: v0.33.17 took 266 s at 81 KB/s. A client
// that bounds the whole exchange at 30 s turns that into "context deadline exceeded (Client.Timeout
// or context cancellation while reading body)", which is the bug this test pins. The durations are
// the shape, not the measurement: the metadata client keeps a 50 ms total deadline and the asset
// takes about 120 ms to arrive, so splitting the two clients is what decides the outcome. It stays
// short on purpose: a suite that sleeps is a per-mutant cost in mutation testing.
func TestASlowDownloadOutlivesTheCheckTimeout(t *testing.T) {
	manager, _ := testManager(t)
	binary := []byte(versionScript("v0.9.0"))
	// Not a real tarball: Apply only stages it, and the new gateway unpacks at boot.
	tarball := []byte("worker sources, arriving slowly")
	// The short client is the one metadata uses. A download sent through it fails on any link
	// slower than this, which is the whole point.
	manager.client = &http.Client{Timeout: 50 * time.Millisecond}

	trickle := func(w http.ResponseWriter, payload []byte) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		piece := len(payload)/6 + 1
		for start := 0; start < len(payload); start += piece {
			_, _ = w.Write(payload[start:min(start+piece, len(payload))])
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	sum := func(payload []byte) string {
		digest := sha256.Sum256(payload)
		return hex.EncodeToString(digest[:])
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/gw", func(w http.ResponseWriter, r *http.Request) { trickle(w, binary) })
	mux.HandleFunc("/worker.tgz", func(w http.ResponseWriter, r *http.Request) { trickle(w, tarball) })
	mux.HandleFunc("/sums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sum(binary) + "  acornd\n" + sum(tarball) + "  " + WorkerTarball + "\n"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	manager.mu.Lock()
	manager.latest = Release{Tag: "v0.9.0", Assets: []Asset{
		{Name: "acornd-" + runtime.GOOS + "-" + runtime.GOARCH, URL: server.URL + "/gw"},
		{Name: WorkerTarball, URL: server.URL + "/worker.tgz"},
		{Name: "sha256sums.txt", URL: server.URL + "/sums.txt"},
	}}
	manager.mu.Unlock()

	if err := manager.Apply(context.Background(), ApplyOptions{Drain: "stop"}); err != nil {
		t.Fatalf("a download slower than the metadata timeout must still install: %v", err)
	}
	got, err := os.ReadFile(manager.binPath)
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatalf("the binary must arrive byte-exact, got %q (%v)", got, err)
	}
	// The tarball takes the same slow path and is verified from the file it landed in.
	staged, err := os.ReadFile(manager.workerPending())
	if err != nil || !bytes.Equal(staged, tarball) {
		t.Fatalf("the worker tarball must arrive byte-exact, got %q (%v)", staged, err)
	}
}

// A connection that goes quiet mid-file must fail rather than hang. The transfer bound is the
// safety net, so the test injects a short one instead of waiting the fifteen minutes a real link
// gets, and asserts the download ends and does not report a short body as complete.
//
// The test carries its own deadline too, and checks which one fired. Losing the bound is a change
// mutation testing will try, and a test that only ends when the code says so holds the suite for
// the whole per-mutant allowance: one such mutant turned a three-minute sweep into seventeen.
func TestADownloadThatStallsGivesUp(t *testing.T) {
	manager, _ := testManager(t)
	manager.transferTimeout = 100 * time.Millisecond
	stalled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "32")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("the first piece"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-stalled:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(stalled)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var body bytes.Buffer
	started := time.Now()
	if err := manager.download(ctx, Asset{Name: "acornd", URL: server.URL}, &body); err == nil {
		t.Fatal("a download that stops delivering bytes must fail, not hang")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the transfer bound should end it in about 100 ms, took %v", elapsed)
	}
	if got := body.String(); got != "the first piece" {
		t.Fatalf("a stalled download should keep only what arrived, got %q", got)
	}
}

// The transfer bound is a safety net with a default behind it: a manager built without one (a test,
// a literal) still downloads, rather than failing every transfer on a zero deadline. Zero is not a
// bound, it is a missing value.
func TestAManagerWithoutABoundStillDownloads(t *testing.T) {
	manager, _ := testManager(t)
	manager.transferTimeout = 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("the file"))
	}))
	defer server.Close()
	var body bytes.Buffer
	if err := manager.download(context.Background(), Asset{Name: "acornd", URL: server.URL}, &body); err != nil {
		t.Fatalf("a missing bound should take the default: %v", err)
	}
	if body.String() != "the file" {
		t.Fatalf("body = %q", body.String())
	}
}

// zeroReader is the endless body the cap tests copy: no memory, so the only cost of reaching
// 256 MB here is the copy itself.
type zeroReader struct{}

func (zeroReader) Read(payload []byte) (int, error) {
	clear(payload)
	return len(payload), nil
}

// The cap is a wrong-answer guard, so a body that reaches it is refused rather than left as a file
// that looks complete when it is not. The boundary matters: exactly the cap is already too big. The
// reader stops at twice the cap, so a change that loses the limit fails here instead of hanging.
func TestABodyThatReachesTheCapIsRefused(t *testing.T) {
	if _, err := copyAsset("acornd", io.Discard, io.LimitReader(zeroReader{}, 2*maxAsset)); err == nil {
		t.Fatal("a body at the cap must be refused, not truncated into a file that looks complete")
	}
	if _, err := copyAsset("acornd", io.Discard, io.LimitReader(zeroReader{}, maxAsset-1)); err != nil {
		t.Fatalf("a body under the cap must pass: %v", err)
	}
}
