package bwrap

import (
	"os"
	"path/filepath"
	"testing"
)

// The supervisor's last words are what a failed start is explained with, and only the last few:
// a log that grows for days should not be pasted into an error.
func TestTheSupervisorsLastWordsAreTheLastFewLines(t *testing.T) {
	dir := t.TempDir()
	bwrap := NewBwrap(dir).WithStateDir(dir)
	if got := bwrap.logTail("missing"); got != "" {
		t.Errorf("a workspace with no log has no last words, got %q", got)
	}
	workspace := bwrap.workspaceDir("ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := "one\ntwo\nthree\nfour\nfive\n"
	if err := os.WriteFile(filepath.Join(workspace, "supervisor.log"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := bwrap.logTail("ws"); got != "three; four; five" {
		t.Errorf("logTail = %q", got)
	}
}
