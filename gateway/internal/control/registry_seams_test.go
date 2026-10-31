package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// The list is somebody's own room first and then by name, so the sidebar does not reorder itself between
// releases because a map iterated in a different order.
func TestTheSidebarIsPersonalFirstThenByName(t *testing.T) {
	workspaces := NewWorkspaces(WorkspacesOptions{WorkspacesDir: t.TempDir()})
	shared := []Member{{Email: "a@example.com", Role: RoleOwner}, {Email: "b@example.com", Role: RoleGuest}}
	workspaces.registry["zulu"] = Workspace{Name: "zulu", Path: "/z", Kind: KindRoom, Members: shared}
	workspaces.registry[PersonalWorkspaceName] = Workspace{Name: PersonalWorkspaceName, Path: "/g", Kind: KindPersonal,
		Members: []Member{{Email: "a@example.com", Role: RoleOwner}}}
	workspaces.registry["alpha"] = Workspace{Name: "alpha", Path: "/a", Kind: KindRoom, Members: shared}

	names := make([]string, 0, 3)
	for _, workspace := range workspaces.List() {
		names = append(names, workspace.Name)
	}
	want := []string{PersonalWorkspaceName, "alpha", "zulu"}
	for index := range want {
		if index >= len(names) || names[index] != want[index] {
			t.Fatalf("order = %v, want %v", names, want)
		}
	}
}

// The registry is written sorted, so the file is the same on every run and a diff of it means
// something. A map written in iteration order gives a file that changes for no reason.
func TestTheRegistryFileIsWrittenSorted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspaces.json")
	registry := RegistryFile{
		Path: path,
		Workspaces: []Workspace{
			{Name: "zulu", Path: "/z"},
			{Name: "alpha", Path: "/a"},
			{Name: "mike", Path: "/m"},
		},
	}
	if err := SaveRegistryFile(registry); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var written RegistryFile
	if err := json.Unmarshal(payload, &written); err != nil {
		t.Fatal(err)
	}
	previous := ""
	for _, workspace := range written.Workspaces {
		if workspace.Name < previous {
			t.Errorf("the file is not sorted: %s after %s", workspace.Name, previous)
		}
		previous = workspace.Name
	}
}

// Eviction stops what nobody is watching, and keeps what somebody is: the general workspace,
// a workspace marked keep-alive because it runs a cron job, one used in the last quarter hour,
// and one with a run in progress or a client attached. Stopping any of those kills processes
// behind the user's back.
func TestEvictionSparesWhatIsStillInUse(t *testing.T) {
	workspaces := NewWorkspaces(WorkspacesOptions{WorkspacesDir: t.TempDir()})
	workspaces.registry[PersonalWorkspaceName] = Workspace{Name: PersonalWorkspaceName}
	workspaces.registry["kept"] = Workspace{Name: "kept", KeepAlive: true}
	workspaces.registry["fresh"] = Workspace{Name: "fresh"}
	workspaces.registry["stale"] = Workspace{Name: "stale"}
	workspaces.registry["running"] = Workspace{Name: "running"}
	workspaces.registry["watched"] = Workspace{Name: "watched"}

	for _, name := range []string{PersonalWorkspaceName, "kept", "fresh", "stale", "running", "watched"} {
		workspaces.controls[name] = New(Options{Workspace: Workspace{Name: name, Path: t.TempDir()}, DataDir: t.TempDir()})
		workspaces.used[name] = time.Now().Add(-time.Hour)
	}
	workspaces.used["fresh"] = time.Now().Add(-time.Minute)
	workspaces.controls["running"].conversations = []protocol.ConversationInfo{{ID: "c", Running: true}}
	workspaces.controls["watched"].subscribers[1] = make(chan protocol.Frame, 1)
	delete(workspaces.used, "watched") // never used: idleFor answers the threshold itself

	// Eviction takes the control out of the map as it hands it over: a request arriving during the
	// stop takes the registry lock and finds nothing to reuse, rather than a control that is
	// half-stopped and about to be closed under it.
	stale := workspaces.controls["stale"]
	stopping := workspaces.evictCandidates()
	if len(stopping) != 1 || stopping[0] != stale {
		names := make([]string, 0, len(stopping))
		for name, control := range map[string]*Control{"stale": stale} {
			for _, stopped := range stopping {
				if stopped == control {
					names = append(names, name)
				}
			}
		}
		t.Fatalf("evicted %d (%v), want only the stale one", len(stopping), names)
	}
	if _, still := workspaces.controls["stale"]; still {
		t.Error("the evicted control should be out of the map it was taken from")
	}
	if got := workspaces.progress["stale"].Status; got != StatusStopped {
		t.Errorf("a stopped workspace is recorded as stopped, got %q", got)
	}
	if _, recorded := workspaces.progress["fresh"]; recorded {
		t.Error("a workspace in use should not be recorded as stopped")
	}
}

// A registry that cannot be written is an error the caller sees, not a state that half happened:
// the folder was made, the registry entry was added, and nothing else would ever say so.
func TestAFirstRunThatCannotWriteItsRegistryFails(t *testing.T) {
	if os.Geteuid() == 0 {
		// A directory in the place of the temp file is what makes the write fail, and that does
		// not depend on who is running — but say why it is here for the reader.
		t.Log("the refusal below is made by a directory, not by permissions")
	}
	dir := t.TempDir()
	data := t.TempDir()
	if err := os.MkdirAll(filepath.Join(data, registryFile)+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	workspaces := NewWorkspaces(WorkspacesOptions{WorkspacesDir: dir, DataDir: data})
	if err := workspaces.loadRegistry(); err == nil {
		t.Fatal("a registry that cannot be written should fail the load")
	}
}

// A registry holding keys this build no longer writes is rewritten without them — once, on load —
// and a rewrite that fails is logged rather than losing the workspaces that were read.
func TestAStaleRegistryIsRewrittenWithoutTheOldKeys(t *testing.T) {
	dir := t.TempDir()
	data := t.TempDir()
	path := filepath.Join(data, registryFile)
	// `trusted` is the key this build no longer writes: a workspace record that still carries it
	// is rewritten on load.
	payload := `{"path":` + strconvQuote(path) + `,"workspaces":[{"name":"personal","path":"` +
		filepath.Join(dir, PersonalWorkspaceName) + `","general":true,"trusted":true}]}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaces := NewWorkspaces(WorkspacesOptions{WorkspacesDir: dir, DataDir: data})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(written), `"trusted"`) {
		t.Errorf("the key this build does not write should be gone: %s", written)
	}
	if _, ok := workspaces.registry[PersonalWorkspaceName]; !ok {
		t.Error("rewriting should keep the workspaces that were read")
	}
}

// A sign-in answers with an id that increases, so two flows started at once cannot be mistaken
// for each other by the answers that come back later.
func TestASignInsIDIncreases(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["describe"] = protocol.Describe{}
	control := controlWithWorker(t, worker)
	first, err := control.Login(context.Background(), "faux", "api_key")
	if err != nil {
		t.Fatal(err)
	}
	second, err := control.Login(context.Background(), "faux", "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if first != "auth-1" || second != "auth-2" {
		t.Errorf("ids = %q, %q; want auth-1 then auth-2", first, second)
	}
}

// A sign-out whose catalog re-read fails for any reason other than "there is no provider now"
// reports the failure: the caller is a person who just signed out and needs to know the screen
// it is looking at is not the current one.
func TestASignOutReportsAReReadThatFails(t *testing.T) {
	worker := newFakeWorker()
	worker.failOn["describe"] = errors.New("the catalog could not be read")
	control := controlWithWorker(t, worker)
	if err := control.Logout(context.Background(), "faux"); err == nil {
		t.Fatal("a failed re-read should be reported")
	}
}

func strconvQuote(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
