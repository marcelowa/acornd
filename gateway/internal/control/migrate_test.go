package control

import (
	"os"
	"path/filepath"
	"slices"
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
	state := filepath.Join(dataDir, "state", PersonalWorkspaceName)
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
	state := filepath.Join(dataDir, "state", PersonalWorkspaceName)
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

// An update keeps who a room belongs to where it was. Membership is the fact the rest is derived from, and an
// update that could rewrite it would be a way to take somebody's room without saying so.
func TestAnUpdateCannotChangeWhoseRoomIsWhose(t *testing.T) {
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: t.TempDir(), WorkspacesDir: t.TempDir()})
	personal := Workspace{Name: PersonalWorkspaceName, Path: t.TempDir(), Kind: KindPersonal,
		Members: []Member{{Email: "admin@example.com", Role: RoleOwner}}}
	shared := Workspace{Name: "shared", Path: t.TempDir(), Kind: KindRoom,
		Members: []Member{
			{Email: "admin@example.com", Role: RoleOwner},
			{Email: "ana@example.com", Role: RoleGuest},
		}}
	workspaces.registry[personal.Name] = personal
	workspaces.registry[shared.Name] = shared

	// An update that names nobody and no kind is still the room it was: the membership and the kind are what the
	// record is, and a settings edit is not a way to take somebody's room or to make one un-forgettable.
	claim := Workspace{Name: shared.Name, Path: shared.Path}
	if err := workspaces.Update(claim); err != nil {
		t.Fatal(err)
	}
	after := workspaces.registry["shared"]
	if len(after.Members) != 2 || after.Owner() != "admin@example.com" {
		t.Errorf("an update rewrote the membership: %v", after.Members)
	}
	if after.Kind != KindRoom {
		t.Errorf("an update rewrote the kind: %q", after.Kind)
	}
	kept := Workspace{Name: PersonalWorkspaceName, Path: personal.Path}
	if err := workspaces.Update(kept); err != nil {
		t.Fatal(err)
	}
	if !workspaces.registry[PersonalWorkspaceName].Personal() {
		t.Error("the personal workspace stopped being personal")
	}
}

// The install's own workspace was called "general" while there was one workspace, and it is called
// "personal" now that there are people (ADR 0032 §9). The record and the whole state folder move together.
func TestTheGeneralWorkspaceBecomesPersonal(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "workspaces")
	state := filepath.Join(dataDir, "state", "general")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	// The write-ahead log travels with its database: ADR 0011 records what moving one without the other cost.
	for _, name := range []string{"session.sqlite", "session.sqlite-wal", "session.sqlite-shm", "settings.json", "drafts.json"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "general"), 0o755); err != nil {
		t.Fatal(err)
	}
	registry := RegistryFile{
		Path:          filepath.Join(dataDir, registryFile),
		WorkspacesDir: dir,
		Workspaces:    []Workspace{{Name: "general", Path: filepath.Join(dir, "general"), Title: "general"}},
	}
	if err := SaveRegistryFile(registry); err != nil {
		t.Fatal(err)
	}

	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, WorkspacesDir: dir, AdminEmail: "Me@Example.com"})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatalf("load: %v", err)
	}

	if _, ok := workspaces.registry["general"]; ok {
		t.Error("the old name is still in the registry")
	}
	personal, ok := workspaces.registry[PersonalWorkspaceName]
	if !ok {
		t.Fatal("the install's own workspace is not called personal")
	}
	if personal.Title != "Personal" {
		t.Errorf("title = %q, want the label the console shows", personal.Title)
	}
	if !slices.Equal(personal.Members, []Member{{Email: "me@example.com", Role: RoleOwner}}) {
		t.Errorf("members = %v, want the admin as its owner, lowercased", personal.Members)
	}
	if personal.Kind != KindPersonal {
		t.Errorf("kind = %q, want %q: the install's own room is never shared", personal.Kind, KindPersonal)
	}
	// Everything the folder held is under the new name, and nothing is left behind.
	for _, name := range []string{"session.sqlite", "session.sqlite-wal", "session.sqlite-shm", "settings.json", "drafts.json"} {
		if _, err := os.Stat(filepath.Join(dataDir, "state", "personal", name)); err != nil {
			t.Errorf("state/personal/%s is missing: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "state", "general")); err == nil {
		t.Error("the old state folder is still there")
	}
	// And it is stored: the next boot reads the new name rather than doing this again.
	reloaded := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, WorkspacesDir: dir})
	if err := reloaded.loadRegistry(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reloaded.registry[PersonalWorkspaceName]; !ok {
		t.Error("the rename was not written to the registry")
	}
}

// A folder already sitting under the new name is a question only a person can answer, so boot says so instead
// of merging two histories into one.
func TestARenameIsRefusedWhenTheNewFolderIsTaken(t *testing.T) {
	dataDir := t.TempDir()
	for _, name := range []string{"general", "personal"} {
		if err := os.MkdirAll(filepath.Join(dataDir, "state", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir})
	workspaces.registry = map[string]Workspace{
		"general": {Name: "general", Path: "/w/general"},
		"second":  {Name: "second", Path: "/w/second"},
	}
	if _, err := workspaces.renameGeneralToPersonal(); err == nil {
		t.Fatal("a rename over an existing folder was allowed")
	}
	if _, ok := workspaces.registry["general"]; !ok {
		t.Error("the record moved even though the folder did not")
	}
}

// An install that already has both names is left alone: guessing which of the two holds the history is how a
// transcript is lost.
func TestBothNamesInTheRegistryIsLeftAlone(t *testing.T) {
	dataDir := t.TempDir()
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir})
	workspaces.registry = map[string]Workspace{
		"general":  {Name: "general", Path: "/w/general"},
		"personal": {Name: "personal", Path: "/w/personal", Kind: KindPersonal, Members: []Member{{Email: "me@example.com", Role: RoleOwner}}},
	}
	if moved, err := workspaces.renameGeneralToPersonal(); err != nil || moved {
		t.Fatalf("moved = %v, err = %v", moved, err)
	}
	if len(workspaces.registry) != 2 {
		t.Errorf("registry = %+v, want both kept", workspaces.registry)
	}
	if got := workspaces.registry["personal"].Members; !slices.Equal(got, []Member{{Email: "me@example.com", Role: RoleOwner}}) {
		t.Errorf("personal members = %v, want them untouched", got)
	}
}
