package e2e

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A file staged for a message comes back with what the message needs to carry: an id, the name it
// had, and the type it was read as. The bytes never come back out, because the worker reads them
// from where they were staged.
func TestAnUploadedFileIsStagedAndAnsweredWithWhatAMessageCarries(t *testing.T) {
	t.Parallel()
	workspaceResources(t)
	s := start(t)
	c := s.client()

	status, payload := c.uploadStatus("personal", "notes.txt", "text/plain", []byte("what the file said"))
	if status != 201 {
		t.Fatalf("status %d: %s", status, payload)
	}
	var attached struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size"`
		MIME string `json:"mime"`
	}
	if err := json.Unmarshal(payload, &attached); err != nil {
		t.Fatalf("answer is not JSON: %s", payload)
	}
	if attached.ID == "" {
		t.Error("an uploaded file needs an id to be named by a message")
	}
	if attached.Name != "notes.txt" {
		t.Errorf("name = %q, want the one it was uploaded with", attached.Name)
	}
	if attached.Size != int64(len("what the file said")) {
		t.Errorf("size = %d", attached.Size)
	}
	if !strings.HasPrefix(attached.MIME, "text/plain") {
		t.Errorf("mime = %q", attached.MIME)
	}

	// Two uploads of the same file are two attachments: the second must not reuse the first's id,
	// or the two would be one file with two messages pointing at it.
	_, second := c.uploadStatus("personal", "notes.txt", "text/plain", []byte("what the file said"))
	var another struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(second, &another); err != nil {
		t.Fatalf("second answer is not JSON: %s", second)
	}
	if another.ID == attached.ID {
		t.Error("two uploads shared an id")
	}
}

// What cannot be staged is refused with the reason, and each refusal is a different sentence
// because each is a different mistake: a file with no bytes, one too large to carry, and a request
// that is not a file at all.
func TestAnUploadThatCannotBeStagedSaysWhy(t *testing.T) {
	t.Parallel()
	workspaceResources(t)
	s := start(t)
	c := s.client()

	status, payload := c.uploadStatus("personal", "empty.txt", "text/plain", nil)
	if status != 400 || !strings.Contains(string(payload), "empty") {
		t.Errorf("an empty file: %d %s", status, payload)
	}

	tooBig := bytes.Repeat([]byte("x"), (10<<20)+1)
	status, payload = c.uploadStatus("personal", "big.bin", "application/octet-stream", tooBig)
	if status != 413 {
		t.Errorf("a file over the cap: %d %s", status, payload)
	}

	// A JSON body is not a multipart form, which is the mistake a script makes.
	status, payload = c.do("POST", "/api/workspaces/personal/attachments", map[string]any{"file": "not a file"})
	if status != 400 || !strings.Contains(string(payload), "multipart") {
		t.Errorf("a request that is not a form: %d %s", status, payload)
	}
}
