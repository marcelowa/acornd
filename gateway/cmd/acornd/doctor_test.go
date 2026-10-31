package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/control"
)

// Doctor used to build its report manager without an engine, so it always exited with
// "sandbox: Driver is required": a diagnosis tool that could not diagnose the tier it exists
// for. This holds reportManager to naming one, and to reporting the host engine's lack of
// isolation rather than claiming a sandbox.
func TestTheDoctorReportNamesAnEngine(t *testing.T) {
	manager, err := reportManager(t.TempDir(), "")
	if err != nil {
		t.Fatalf("reportManager: %v", err)
	}
	report := manager.Report()
	if report.Engine != "microsandbox" || !report.Isolated {
		t.Errorf("the default report should be the isolated microsandbox, got %+v", report)
	}

	host, err := reportManager(t.TempDir(), "host")
	if err != nil {
		t.Fatalf("reportManager(host): %v", err)
	}
	if report := host.Report(); report.Engine != "host" || report.Isolated {
		t.Errorf("the host report should say it is not isolated, got %+v", report)
	}
}

// Nesting is answered in two halves and doctor asks both, without touching anything: an
// engine that is not installed has nothing to offer, and the host engine has no sandbox to
// nest. Both answers are the same on every machine, which is what makes them testable.
func TestDoctorNestingIsRefusedBeforeTheMachineIsChecked(t *testing.T) {
	manager, err := reportManager(t.TempDir(), "")
	if err != nil {
		t.Fatalf("reportManager: %v", err)
	}
	if support := manager.Nesting(context.Background()); support.Available || support.Reason == "" {
		t.Errorf("an engine that is not installed cannot nest, and the answer has to say why: %+v", support)
	}

	host, err := reportManager(t.TempDir(), "host")
	if err != nil {
		t.Fatalf("reportManager(host): %v", err)
	}
	if support := host.Nesting(context.Background()); support.Available || support.Reason == "" {
		t.Errorf("the host engine is not a sandbox and cannot nest: %+v", support)
	}
}

// Forgetting a room unregisters it and leaves the folder, so a folder nothing owns is a thing an install
// accumulates quietly. Doctor names every one of them (ADR 0034 §6): an orphan is seen once rather than forgotten
// twice, and the folder is usually somebody's repository, so what to do about it is the owner's decision.
func TestDoctorNamesAFolderWithNoRecord(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "workspaces")
	kept := filepath.Join(dir, "kept")
	for _, folder := range []string{kept, filepath.Join(dir, "forgotten")} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	registry := control.RegistryFile{
		Path:          filepath.Join(dataDir, "workspaces.json"),
		WorkspacesDir: dir,
		Workspaces:    []control.Workspace{{Name: "kept", Path: kept, Kind: control.KindRoom}},
	}
	if err := control.SaveRegistryFile(registry); err != nil {
		t.Fatal(err)
	}

	orphans := workspaceOrphans(dataDir)
	if !slices.Contains(orphans, filepath.Join(dir, "forgotten")) {
		t.Errorf("doctor named %v, want the folder with no record", orphans)
	}
	// A folder a record points at is not an orphan, and a file in the folder is not a folder.
	if slices.Contains(orphans, kept) {
		t.Errorf("doctor named a folder that has a record: %v", orphans)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := workspaceOrphans(dataDir); len(got) != 1 {
		t.Errorf("a file in the workspaces folder is not an orphan: %v", got)
	}
}
