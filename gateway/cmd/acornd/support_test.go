package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupportFindsAWorkspaceByNameOrTitle(t *testing.T) {
	dir := t.TempDir()
	registry := `{"workspaces":[{"name":"youki","path":"/tmp/youki","title":"Youki room","engine":"youki"}]}`
	if err := os.WriteFile(filepath.Join(dir, "workspaces.json"), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"youki", "Youki room"} {
		workspace, err := supportWorkspace(dir, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if workspace.Name != "youki" {
			t.Fatalf("%s: workspace = %+v", name, workspace)
		}
	}
	if _, err := supportWorkspace(dir, "missing"); err == nil || !strings.Contains(err.Error(), "have youki") {
		t.Fatalf("a missing workspace should name the ones there are: %v", err)
	}
}

func TestSupportFilenameCannotEscapeItsFolder(t *testing.T) {
	if got := safeName("../a b\\c"); strings.ContainsAny(got, "/\\ ") {
		t.Fatalf("safeName = %q", got)
	}
}
