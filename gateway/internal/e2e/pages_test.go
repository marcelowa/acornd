package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Every address the interface has a URL for answers with the client, not a 404: /c/<workspace>/<id>
// is a conversation, /w/<workspace> a workspace, and /settings, /workspaces and /update are pages.
// These are the paths a reload, a bookmark, or a shared link takes, and a client-side router only
// gets the chance to route if the server hands it the document.
func TestEveryPageAddressAnswersWithTheClient(t *testing.T) {
	t.Parallel()
	s := start(t)
	c := s.client()
	clientBuilt(t, c)
	created := c.create("personal")

	addresses := []string{
		"/",
		"/w/personal",
		"/c/general/" + created.ID,
		"/c/" + created.ID,
		"/settings",
		"/settings/",
		"/workspaces",
		"/update",
		"/update/v1.2.3",
	}
	for _, address := range addresses {
		response, err := c.http.Get(c.base + address)
		if err != nil {
			t.Fatalf("GET %s: %v", address, err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatalf("GET %s: %v", address, err)
		}

		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", address, response.StatusCode)
			continue
		}
		if contentType := response.Header.Get("content-type"); !strings.Contains(contentType, "text/html") {
			t.Errorf("GET %s is %q, want the client", address, contentType)
		}
		// A page is never cached: a reload has to see the build that is there now, or a new
		// interface is served from an old one's cache.
		if cache := response.Header.Get("cache-control"); !strings.Contains(cache, "no-cache") {
			t.Errorf("GET %s cache-control = %q", address, cache)
		}
		if len(body) == 0 {
			t.Errorf("GET %s answered an empty document", address)
		}
	}
}

// An address the interface does not have is a 404, and that is the truth about it: every URL the
// client routes is registered on the server — the whole /settings subtree, one page per
// conversation and per workspace, and the release pages — so a path outside them is not a page
// somebody mistyped, it is a path that does not exist.
func TestAnAddressTheInterfaceDoesNotHaveIsA404(t *testing.T) {
	t.Parallel()
	s := start(t)
	c := s.client()
	clientBuilt(t, c)
	response, err := c.http.Get(c.base + "/nothing/here")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", response.StatusCode)
	}
}

// An upload is read as the type it is, not as the type it claims: a file sent as
// application/octet-stream is sniffed, so a picture is a picture and a text file is text. The
// claim is taken when it is specific, because the browser knows better than 512 bytes do.
func TestAnUploadIsReadAsTheTypeItIs(t *testing.T) {
	t.Parallel()
	workspaceResources(t)
	s := start(t)
	c := s.client()

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")
	cases := []struct {
		name    string
		claimed string
		payload []byte
		want    string
	}{
		// A specific claim is believed: the browser knows what it is sending.
		{"shot.png", "image/png", png, "image/png"},
		// A generic claim is not: the bytes are read instead, which is what catches a file sent
		// by a script that says nothing true about it.
		{"shot.bin", "application/octet-stream", png, "image/png"},
		{"notes.bin", "application/octet-stream", []byte("plain old text\n"), "text/plain"},
		// Nothing recognisable stays what it was declared to be.
		{"blob.bin", "application/octet-stream", []byte{0x00, 0x01, 0x02, 0x03}, "application/octet-stream"},
	}
	for _, one := range cases {
		status, payload := c.uploadStatus("personal", one.name, one.claimed, one.payload)
		if status != 201 {
			t.Errorf("%s: status %d %s", one.name, status, payload)
			continue
		}
		var attached struct {
			MIME string `json:"mime"`
		}
		if err := json.Unmarshal(payload, &attached); err != nil {
			t.Fatalf("%s: %v", one.name, err)
		}
		if !strings.HasPrefix(attached.MIME, one.want) {
			t.Errorf("%s sent as %q was read as %q, want %q", one.name, one.claimed, attached.MIME, one.want)
		}
	}
}

// clientBuilt skips a test whose subject is the interface's own document when the interface was
// never built into the binary. It asks the binary rather than importing the embedded files: a
// binary built without the client answers 500 "client not found", which is the honest answer, and
// this is what keeps a tier that does not run `make ui-build` from reporting that answer as a
// failure of the page routes.
func clientBuilt(t *testing.T, c *client) {
	t.Helper()
	response, err := c.http.Get(c.base + "/w/personal")
	if err != nil {
		t.Fatalf("GET /w/personal: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("GET /w/personal: %v", err)
	}
	// The probe is a page route, not the root: an embedded folder without a client lists itself at
	// "/" with a 200, so the root cannot tell a built interface from an empty folder. The page
	// handler can — it answers 500 "client not found" when there is no document to serve.
	if response.StatusCode == http.StatusInternalServerError && strings.Contains(string(body), "client not found") {
		t.Skip("this binary does not serve the interface (make ui-build)")
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /w/personal = %d: %s", response.StatusCode, body)
	}
}
