package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/control"
)

// Purge is the only thing in acornfall that deletes a workspace, and it needs the record to find the folder
// (ADR 0034 §6). The order is the trap: purge runs instead of forget and never after it, because once a room is
// forgotten its folder is just a folder — and a command that deleted a directory by a name nothing points at any
// more is how somebody removes the wrong one.
func TestPurgeNeedsTheRecord(t *testing.T) {
	folder := t.TempDir()
	registry := control.RegistryFile{WorkspacesDir: filepath.Dir(folder), Workspaces: []control.Workspace{
		{Name: "team", Path: folder, Kind: control.KindRoom, Members: []control.Member{{Email: "ana@example.com", Role: control.RoleOwner}}},
	}}

	// A record that is there is what says which folder to delete, and which folder is printed before it goes.
	found, err := purgeTarget(registry, "team")
	if err != nil {
		t.Fatalf("purgeTarget(team): %v", err)
	}
	if found.Path != folder {
		t.Errorf("path = %q, want the folder the record names", found.Path)
	}

	// Nothing recorded means nothing to delete: the sentence says why rather than guessing a path from a name.
	if _, err := purgeTarget(registry, "forgotten"); err == nil {
		t.Fatal("a workspace with no record was purged")
	} else if !strings.Contains(err.Error(), "a forgotten folder is just a folder") {
		t.Errorf("the refusal should say a forgotten folder is just a folder, got %q", err)
	}
}
