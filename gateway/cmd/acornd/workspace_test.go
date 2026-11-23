package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/control"
)

// Purge is the only thing in acornd that deletes a workspace, and it needs the record to find the folder
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

// A workspace is two folders, not one: the folder the agent works in, and the state beside the registry. Deleting
// only the first left session.sqlite, settings.json and drafts.json behind, and a room created under the same
// name opened them — a forgotten history arriving in a room that looked empty (ADR 0034 §6).
func TestPurgeDeletesTheStateToo(t *testing.T) {
	dataDir := t.TempDir()
	folder := filepath.Join(t.TempDir(), "team")
	state := control.StatePath(dataDir, "team")
	for _, path := range []string{folder, state} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The three files the state folder holds, and the one that outlives a room if nobody deletes it.
	for _, name := range []string{"session.sqlite", "settings.json", "drafts.json"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seedRegistry(t, dataDir, control.RegistryFile{
		WorkspacesDir: filepath.Dir(folder),
		Workspaces:    []control.Workspace{{Name: "team", Path: folder, Kind: control.KindRoom}},
	})
	registry := control.NewRegistry(dataDir)
	file, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	target, err := purgeTarget(file, "team")
	if err != nil {
		t.Fatalf("purgeTarget(team): %v", err)
	}
	// Both paths are what a purge prints before it deletes, so both are what the record has to answer with.
	purgedFolder, purgedState := purgeTargets(dataDir, target)
	if purgedFolder != folder || purgedState != state {
		t.Fatalf("purge names %q and %q, want the folder and its state", purgedFolder, purgedState)
	}
	if err := purgeWorkspace(registry, target.Name, purgedFolder, purgedState); err != nil {
		t.Fatalf("purge: %v", err)
	}
	for _, gone := range []string{folder, state} {
		if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the purge: %v", gone, err)
		}
	}
	reloaded, err := registry.Load()
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(reloaded.Workspaces, func(workspace control.Workspace) bool { return workspace.Name == "team" }) {
		t.Errorf("the record survived the purge: %+v", reloaded.Workspaces)
	}
}

// A personal room is never forgotten (ADR 0034 §5), and purge is stronger than forget, so it cannot be the way
// round the rule: the install's own room and somebody else's are both refused, each with the reason that fits,
// and the files stay the operator's to remove.
func TestPurgeRefusesEveryPersonalRoom(t *testing.T) {
	registry := control.RegistryFile{Workspaces: []control.Workspace{
		{Name: control.PersonalWorkspaceName, Path: "/w/personal", Kind: control.KindPersonal},
		{Name: "ana", Path: "/w/ana", Kind: control.KindPersonal,
			Members: []control.Member{{Email: "ana@example.com", Role: control.RoleOwner}}},
		{Name: "team", Path: "/w/team", Kind: control.KindRoom},
	}}
	install := refusalFor(t, registry, control.PersonalWorkspaceName)
	if !strings.Contains(install, "the install's own room") || !strings.Contains(install, "by hand") {
		t.Errorf("the install's own room should be refused for its own reason, got %q", install)
	}
	somebody := refusalFor(t, registry, "ana")
	if !strings.Contains(somebody, "somebody's own room") || !strings.Contains(somebody, "by hand") {
		t.Errorf("a person's own room should be refused for its own reason, got %q", somebody)
	}
	if somebody == install {
		t.Error("the two refusals are one sentence, and they are two situations")
	}
	// A room is what purge is for: the refusal is the kind, not the command.
	if _, err := purgeTarget(registry, "team"); err != nil {
		t.Errorf("a room was refused: %v", err)
	}
}

// seedRegistry writes a whole registry, as an install that already has one would hold it.
func seedRegistry(t *testing.T, dataDir string, file control.RegistryFile) {
	t.Helper()
	err := control.NewRegistry(dataDir).Change(func(registry *control.RegistryFile) error {
		*registry = file
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// refusalFor is the sentence a purge gives for one name, and a failure if it deletes it instead.
func refusalFor(t *testing.T, registry control.RegistryFile, name string) string {
	t.Helper()
	if _, err := purgeTarget(registry, name); err != nil {
		return err.Error()
	}
	t.Fatalf("purge accepted %q", name)
	return ""
}

// Two terminals adding one name: each checked the file before choosing an engine, so the check is asked again
// under the registry lock, and the second add is refused rather than written as a second record of the name.
func TestTwoTerminalsCannotRecordTheSameName(t *testing.T) {
	dataDir := t.TempDir()
	record := addRecord{name: "team", absolute: t.TempDir(), engine: "bwrap"}
	if _, err := recordWorkspace(dataDir, record); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := recordWorkspace(dataDir, record); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("the second add of one name should be refused, got %v", err)
	}
	file, err := control.NewRegistry(dataDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Workspaces) != 1 {
		t.Errorf("the registry holds %d records, want 1: %+v", len(file.Workspaces), file.Workspaces)
	}
}
