package control

import (
	"os"
	"path/filepath"
	"testing"
)

// An install from before workspaces has its database and settings beside the data folder; the
// general workspace is where they belong now. The move is what makes an upgrade look like no
// change at all, and every rule in it is about not losing a commit.

// oldLayout writes what a previous version left behind: the database, its write-ahead log and its
// shared-memory sidecar, plus the settings.
func oldLayout(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"session.sqlite", "session.sqlite-wal", "session.sqlite-shm", "settings.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnOldInstallIsMovedIntoTheGeneralWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	oldLayout(t, dataDir)
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, WorkspacesDir: t.TempDir()})

	if err := workspaces.migrate(); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dataDir, "state", GeneralWorkspaceName)
	// The write-ahead log travels with its database: moving one without the other loses every
	// commit that had not been checkpointed.
	for _, name := range []string{"session.sqlite", "session.sqlite-wal", "session.sqlite-shm", "settings.json"} {
		if _, err := os.Stat(filepath.Join(state, name)); err != nil {
			t.Errorf("%s should have moved: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, name)); err == nil {
			t.Errorf("%s should not be in the old place any more", name)
		}
	}
	// The folder is private: it holds a database of everything anybody said.
	if info, err := os.Stat(state); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the general workspace's state folder should be 0700: %v %v", info, err)
	}
}

// A database that already moved leaves its sidecars behind, and they belong to it: leaving them
// wastes megabytes and invites the next reader to open the wrong one.
func TestOrphanedSidecarsAreRemoved(t *testing.T) {
	dataDir := t.TempDir()
	state := filepath.Join(dataDir, "state", GeneralWorkspaceName)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "session.sqlite"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The sidecars are in the old place, with no database beside them.
	for _, name := range []string{"session.sqlite-wal", "session.sqlite-shm"} {
		if err := os.WriteFile(filepath.Join(dataDir, name), []byte("orphan"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, WorkspacesDir: t.TempDir()})
	if err := workspaces.migrate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"session.sqlite-wal", "session.sqlite-shm"} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); err == nil {
			t.Errorf("%s is an orphan of a database that moved, and should be gone", name)
		}
	}
	// The database that was already there is untouched.
	if payload, err := os.ReadFile(filepath.Join(state, "session.sqlite")); err != nil || string(payload) != "db" {
		t.Errorf("the moved database should be left alone: %q %v", payload, err)
	}
}

// Nothing to move is not an error: a fresh install has no old layout, and a second run finds the
// files where the first one put them.
func TestMigrationWithNothingToDoIsQuiet(t *testing.T) {
	dataDir := t.TempDir()
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, WorkspacesDir: t.TempDir()})
	if err := workspaces.migrate(); err != nil {
		t.Fatalf("a fresh install should migrate nothing: %v", err)
	}
	if err := workspaces.migrate(); err != nil {
		t.Fatalf("running it twice should be harmless: %v", err)
	}
}

// An update that would move a workspace's folder restarts its worker, because the folder is passed
// when the process starts and a change would otherwise wait for the next eviction — and a grant of
// private networks is built into the sandbox, so it needs a new one too. Withdrawing it does not:
// a downgrade takes effect when the worker next starts.
func TestAWorkspaceUpdateRestartsWhatDependsOnTheFolder(t *testing.T) {
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: t.TempDir(), WorkspacesDir: t.TempDir()})
	first := Workspace{Name: "w", Path: t.TempDir()}
	workspaces.registry["w"] = first
	worker := newFakeWorker()
	control := controlWithWorker(t, worker)
	control.opts.Workspace = first
	workspaces.controls["w"] = control

	moved := first
	moved.Path = t.TempDir()
	if err := workspaces.Update(moved); err != nil {
		t.Fatal(err)
	}
	if control.WorkerUp() {
		t.Error("a moved workspace's worker should be stopped: the folder is passed at start")
	}
	if got := workspaces.registry["w"].Path; got != moved.Path {
		t.Errorf("the registry should have the new folder, has %q", got)
	}
}

// The device grant is baked into a running sandbox, so withdrawing it rebuilds the sandbox and
// stops the worker instead of leaving a guest that still has the devices the record says it
// does not.
func TestAWorkspaceUpdateRebuildsTheSandboxWhenDevicesChange(t *testing.T) {
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: t.TempDir(), WorkspacesDir: t.TempDir()})
	first := Workspace{Name: "w", Path: t.TempDir()}
	workspaces.registry["w"] = first
	worker := newFakeWorker()
	control := controlWithWorker(t, worker)
	control.opts.Workspace = first
	workspaces.controls["w"] = control

	on := true
	updated := first
	updated.DisableDevices = &on
	if err := workspaces.Update(updated); err != nil {
		t.Fatal(err)
	}
	if control.WorkerUp() {
		t.Error("a device change should stop the worker whose sandbox was built with them")
	}
	if got := workspaces.registry["w"].DisableDevices; got == nil || !*got {
		t.Errorf("the registry should keep the withdrawal, has %+v", got)
	}
}

// Updating a workspace that is not registered is refused rather than created: a create through the
// update path would skip the folder checks that creating does.
func TestUpdatingAWorkspaceThatIsNotThereIsRefused(t *testing.T) {
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: t.TempDir(), WorkspacesDir: t.TempDir()})
	if err := workspaces.Update(Workspace{Name: "ghost", Path: t.TempDir()}); err == nil {
		t.Fatal("updating an unknown workspace should be refused")
	}
	if _, exists := workspaces.registry["ghost"]; exists {
		t.Error("a refused update should not register anything")
	}
}

// An update keeps the general flag where it was: exactly one workspace is general, and that is not
// this call's to change.
func TestAnUpdateCannotMoveTheGeneralFlag(t *testing.T) {
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: t.TempDir(), WorkspacesDir: t.TempDir()})
	general := Workspace{Name: GeneralWorkspaceName, Path: t.TempDir(), General: true}
	other := Workspace{Name: "other", Path: t.TempDir()}
	workspaces.registry[general.Name] = general
	workspaces.registry[other.Name] = other

	claim := other
	claim.General = true
	if err := workspaces.Update(claim); err != nil {
		t.Fatal(err)
	}
	if workspaces.registry["other"].General {
		t.Error("an update should not make a second workspace general")
	}
	if !workspaces.registry[GeneralWorkspaceName].General {
		t.Error("the general workspace should still be general")
	}
}
