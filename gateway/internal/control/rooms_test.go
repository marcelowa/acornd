package control

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A room's record, from the three sides that can disagree about it: the room being born, the file it is read
// from, and the two acts that take it away.

// A room is born with its creator as its owner (ADR 0034 §3). It used to be born with nobody in it, and being in
// the room is what allows a caller — so the room the console made was refused to everybody, its maker included,
// and the journeys the ADR is for would have inherited that.
func TestARoomIsBornWithItsOwner(t *testing.T) {
	dataDir := t.TempDir()
	workspaces := registryIn(t, dataDir, "", nil)

	created, err := workspaces.Create("team", "Ana@Example.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Kind != KindRoom {
		t.Errorf("kind = %q, want %q: a room somebody made is shared by nature", created.Kind, KindRoom)
	}
	if created.Personal() {
		t.Error("a room somebody made is somebody's own room")
	}
	if !slices.Equal(created.Members, []Member{{Email: "ana@example.com", Role: RoleOwner}}) {
		t.Errorf("members = %v, want the creator as its owner, lowercased", created.Members)
	}
	if !created.Allows(Caller{Email: "ana@example.com"}) {
		t.Error("the room refuses the person who made it")
	}
	// And it is the record, not just the answer: the next start reads an owner rather than an empty room.
	reloaded, err := LoadRegistryFile(filepath.Join(dataDir, registryFile))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	stored := reloaded.Workspaces[slices.IndexFunc(reloaded.Workspaces, func(workspace Workspace) bool { return workspace.Name == "team" })]
	if got := stored.Owner(); got != "ana@example.com" {
		t.Errorf("the stored room's owner = %q, want the creator", got)
	}
}

// Exactly one owner per room, at load and on every write (ADR 0034 §3). Two owners is not a room with a spare:
// it is a question nobody can answer, because nothing says which of the two the room is.
func TestARoomHasExactlyOneOwner(t *testing.T) {
	workspaces := registry(t, nil)
	two := Workspace{Name: "team", Path: t.TempDir(), Kind: KindRoom, Members: []Member{
		{Email: "ana@example.com", Role: RoleOwner},
		{Email: "bo@example.com", Role: RoleOwner},
	}}
	if err := workspaces.Add(two); err == nil {
		t.Fatal("a room with two owners was written")
	} else if !strings.Contains(err.Error(), "exactly one owner") {
		t.Errorf("the refusal should say the rule, got %q", err)
	}

	// The same rule on the way in: a file that says so is refused rather than loaded as if it could say that.
	dataDir := t.TempDir()
	dir := t.TempDir()
	_, err := loadRecords(t, dataDir, dir, "", fmt.Sprintf(
		`{"name":"team","path":%s,"kind":"room","members":[{"email":"ana@example.com","role":"owner"},{"email":"bo@example.com","role":"owner"}]}`,
		strconvQuote(filepath.Join(dir, "team"))))
	if err == nil {
		t.Fatal("a registry naming two owners was loaded")
	}
	if !strings.Contains(err.Error(), registryFile) {
		t.Errorf("the refusal should name the file, got %q", err)
	}
}

// A personal room is never shared, on any install (ADR 0034 §5): journey 1 is *make a room and invite people*,
// not *open my room*. A room with a guest in it is fine, which is what makes the refusal the kind's and not the
// member list's.
func TestAPersonalRoomRefusesAGuest(t *testing.T) {
	workspaces := registry(t, nil)
	shared := Workspace{Name: "ana", Path: t.TempDir(), Kind: KindPersonal, Members: []Member{
		{Email: "ana@example.com", Role: RoleOwner},
		{Email: "bo@example.com", Role: RoleGuest},
	}}
	if err := workspaces.Add(shared); err == nil {
		t.Fatal("a personal room with a guest was written")
	} else if !strings.Contains(err.Error(), "never shared") {
		t.Errorf("the refusal should say a personal room is never shared, got %q", err)
	}

	// The same members in a room are what a room is for.
	room := shared
	room.Name, room.Kind = "team", KindRoom
	if err := workspaces.Add(room); err != nil {
		t.Fatalf("a room with a guest was refused: %v", err)
	}
}

// Somebody's own room is never removed (ADR 0034 §5). Forgetting one takes away the only place a person had, and
// the admin who wants it gone has revoking them, which is a decision with a name.
func TestAPersonalRoomCannotBeForgotten(t *testing.T) {
	workspaces := registry(t, []Workspace{
		{Name: "ana", Path: t.TempDir(), Kind: KindPersonal, Members: []Member{{Email: "ana@example.com", Role: RoleOwner}}},
	})
	if err := workspaces.Remove(context.Background(), "ana"); err == nil {
		t.Fatal("somebody's own room was forgotten")
	} else if !strings.Contains(err.Error(), "cannot be forgotten") {
		t.Errorf("the refusal should say it cannot be forgotten, got %q", err)
	}
	if _, still := workspaces.Get("ana"); !still {
		t.Error("the refused room went anyway")
	}
	if got := workspaces.registry["ana"].Owner(); got != "ana@example.com" {
		t.Errorf("the refused room lost its owner: %q", got)
	}
}

// Forget unregisters and leaves everything else alone (ADR 0034 §6): the room is gone from every rail and refused
// for every caller, and its folder and its state are exactly where they were, so a person who forgot one by
// mistake still has their repository and their history.
func TestForgetKeepsTheFolder(t *testing.T) {
	dataDir := t.TempDir()
	folder := filepath.Join(t.TempDir(), "team")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(folder, "notes.md")
	if err := os.WriteFile(note, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dataDir, "state", "team")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(state, "session.sqlite")
	if err := os.WriteFile(history, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaces := registryIn(t, dataDir, "", []Workspace{
		{Name: "team", Path: folder, Kind: KindRoom, Members: []Member{{Email: "ana@example.com", Role: RoleOwner}}},
	})

	if err := workspaces.Remove(context.Background(), "team"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	for _, workspace := range workspaces.List() {
		if workspace.Name == "team" {
			t.Error("a forgotten room is still on the rail")
		}
	}
	// Refused for every caller, and the owner is not a special case.
	for _, caller := range []Caller{{Email: "ana@example.com"}, {Admin: true, Email: "me@example.com"}} {
		if _, _, err := workspaces.Route(context.Background(), caller, "team"); !errors.Is(err, ErrNoWorkspace) {
			t.Errorf("Route(%+v) = %v, want ErrNoWorkspace", caller, err)
		}
	}
	for _, kept := range []string{note, history} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("forgetting a room took %s with it: %v", kept, err)
		}
	}
}

// The address list this install wrote becomes roles (ADR 0034 §8): one address was somebody's own room, and it
// is its owner. The file is written back in the shape this build reads, so the migration is paid once.
func TestAMembersListBecomesItsOwner(t *testing.T) {
	dataDir := t.TempDir()
	dir := t.TempDir()
	workspaces, err := loadRecords(t, dataDir, dir, "",
		fmt.Sprintf(`{"name":"ana","path":%s,"members":["Ana@Example.com"]}`, strconvQuote(filepath.Join(dir, "ana"))))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ana := workspaces.registry["ana"]
	if !slices.Equal(ana.Members, []Member{{Email: "ana@example.com", Role: RoleOwner}}) {
		t.Errorf("members = %v, want the one address as its owner, lowercased", ana.Members)
	}
	// And the file says so: a reload reads a role rather than guessing one again.
	reloaded, err := LoadRegistryFile(filepath.Join(dataDir, registryFile))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	stored := reloaded.Workspaces[slices.IndexFunc(reloaded.Workspaces, func(workspace Workspace) bool { return workspace.Name == "ana" })]
	if !slices.Equal(stored.Members, []Member{{Email: "ana@example.com", Role: RoleOwner}}) {
		t.Errorf("the written registry has %v, want the role it decided", stored.Members)
	}
}

// Two addresses could only come from a registry somebody edited by hand — no shipped code ever wrote one — so the
// reading is a guess: the first is the owner and the rest are guests, and one log line says it guessed
// (ADR 0034 §8). A migration that chose silently would hand somebody's room over with nothing in the log.
func TestASecondAddressBecomesAGuestAndSaysSo(t *testing.T) {
	dataDir := t.TempDir()
	dir := t.TempDir()
	path := filepath.Join(dataDir, registryFile)
	payload := fmt.Sprintf(`{"workspacesDir":%s,"workspaces":[{"name":"team","path":%s,"members":["ana@example.com","bo@example.com"]}]}`,
		strconvQuote(dir), strconvQuote(filepath.Join(dir, "team")))
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir})
	logged := captureLog(func() {
		if err := workspaces.loadRegistry(); err != nil {
			t.Errorf("load: %v", err)
		}
	})

	team := workspaces.registry["team"]
	if team.Owner() != "ana@example.com" {
		t.Errorf("owner = %q, want the first address", team.Owner())
	}
	if got := team.RoleOf("bo@example.com"); got != RoleGuest {
		t.Errorf("the second address is %q, want a guest", got)
	}
	if lines := strings.Count(logged, "\n"); lines != 1 {
		t.Errorf("the guess took %d log lines, want one: %q", lines, logged)
	}
	if !strings.Contains(logged, `"team"`) || !strings.Contains(logged, "guess") {
		t.Errorf("the log line should name the room and say it guessed, got %q", logged)
	}
}

// A room with one address, and no roles, is somebody's own room (ADR 0034 §8). Today every one of them was made by
// CreatePersonal or is the install's own, so the reading is a fact; a room born with its owner is the new thing,
// and it says so in its kind.
func TestAOneMemberRoomBecomesPersonal(t *testing.T) {
	dir := t.TempDir()
	workspaces, err := loadRecords(t, t.TempDir(), dir, "",
		fmt.Sprintf(`{"name":"ana","path":%s,"members":["ana@example.com"]}`, strconvQuote(filepath.Join(dir, "ana"))))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if ana := workspaces.registry["ana"]; !ana.Personal() || ana.Kind != KindPersonal {
		t.Errorf("kind = %q, want %q: one address and no roles is somebody's own room", ana.Kind, KindPersonal)
	}
}

// A room nobody is named in is the admin's (ADR 0034 §8). Before there were people every conversation in this
// registry was the admin's, so this records a fact rather than making a grant — and without it the first boot with
// a door would hide every existing workspace from the person at the keyboard.
func TestAMemberlessRoomIsTheAdmins(t *testing.T) {
	dataDir := t.TempDir()
	dir := t.TempDir()
	workspaces, err := loadRecords(t, dataDir, dir, "Admin@Example.com", fmt.Sprintf(
		`{"name":"personal","path":%s},{"name":"old","path":%s},{"name":"team","path":%s,"members":[{"email":"ana@example.com","role":"owner"}]}`,
		strconvQuote(filepath.Join(dir, "personal")), strconvQuote(filepath.Join(dir, "old")), strconvQuote(filepath.Join(dir, "team"))))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	own := workspaces.registry[PersonalWorkspaceName]
	if own.Owner() != "admin@example.com" || !own.Personal() {
		t.Errorf("the install's own room is %+v, want the admin as its owner", own.Members)
	}
	// A room somebody made before there were people is the admin's too, and it stays a room: a workspace made with
	// `workspace add` was never somebody's own room, and calling it personal would make it un-forgettable.
	made := workspaces.registry["old"]
	if made.Owner() != "admin@example.com" || made.Personal() {
		t.Errorf("an unnamed room is %+v, want the admin as its owner and a room", made)
	}
	// A room that already names somebody is not touched: the migration fills a blank, it does not rewrite a fact.
	if got := workspaces.registry["team"].Owner(); got != "ana@example.com" {
		t.Errorf("the named room's owner = %q, want it left alone", got)
	}
	// And it is stored: the next start reads an owner rather than adopting the room again.
	reloaded, err := LoadRegistryFile(filepath.Join(dataDir, registryFile))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, workspace := range reloaded.Workspaces {
		if workspace.Name != PersonalWorkspaceName && workspace.Name != "old" {
			continue
		}
		if workspace.Owner() != "admin@example.com" {
			t.Errorf("%s was not written with its owner: %v", workspace.Name, workspace.Members)
		}
	}
}

// A personal room naming a guest is refused at load, with the file named (ADR 0034 §8). It is a claim this design
// says is impossible, and a claim like that must not load as if it were possible — the same reason ADR 0032
// refuses a `trusted` field. Both shapes are refused: the one this build writes, and the address list it used to.
func TestAPersonalRoomWithAGuestIsRefused(t *testing.T) {
	cases := map[string]string{
		"a role the file decided":    `{"email":"ana@example.com","role":"owner"},{"email":"bo@example.com","role":"guest"}`,
		"an address list it guessed": `"ana@example.com","bo@example.com"`,
	}
	for name, members := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(t.TempDir(), registryFile)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			payload := fmt.Sprintf(`{"workspacesDir":%s,"workspaces":[{"name":"personal","path":%s,"members":[%s]}]}`,
				strconvQuote(dir), strconvQuote(filepath.Join(dir, "personal")), members)
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			workspaces := NewWorkspaces(WorkspacesOptions{DataDir: filepath.Dir(path)})
			err := workspaces.loadRegistry()
			if err == nil {
				t.Fatal("a personal room with a guest was loaded")
			}
			if !strings.Contains(err.Error(), registryFile) {
				t.Errorf("the refusal should name the file, got %q", err)
			}
			// And nothing was written: a refused registry is the one it was, so a person can edit it.
			written, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(written) != payload {
				t.Errorf("a refused registry was rewritten:\n%s", written)
			}
		})
	}
}

// loadRecords writes a registry file with these records and loads it. The records are JSON because the migration
// is a reading of the file: a test that started from Workspace values would skip the half under test.
func loadRecords(t *testing.T, dataDir, dir, admin, records string) (*Workspaces, error) {
	t.Helper()
	payload := fmt.Sprintf(`{"workspacesDir":%s,"workspaces":[%s]}`, strconvQuote(dir), records)
	if err := os.WriteFile(filepath.Join(dataDir, registryFile), []byte(payload), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, AdminEmail: admin})
	return workspaces, workspaces.loadRegistry()
}

// captureLog runs something with the diagnostic log captured, and answers what it wrote. The log is the package's
// diagnostics, so a test that reads a sentence is a test of what a person would see at boot.
func captureLog(run func()) string {
	var captured bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&captured)
	defer log.SetOutput(previous)
	run()
	return captured.String()
}

// A room made over the state of one that was forgotten would open its storage, its settings and its drafts: a
// room that looks new and answers with somebody else's history. The state is not the record, so the record cannot
// answer for it — the name is refused, before a folder is made, and the sentence says what deletes it (ADR 0034 §6).
func TestANewRoomCannotInheritAForgottenOne(t *testing.T) {
	dataDir := t.TempDir()
	workspaces := registryIn(t, dataDir, "", nil)
	left := StatePath(dataDir, "team")
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "session.sqlite"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := workspaces.Create("team", "ana@example.com")
	if err == nil {
		t.Fatal("a room was made over the state of a forgotten one")
	}
	if !strings.Contains(err.Error(), "acornfall workspace purge team") || !strings.Contains(err.Error(), left) {
		t.Errorf("the refusal should name the command and the folder, got %q", err)
	}
	// Refused before anything was created: no record, and no folder for the next Create to adopt either.
	if _, found := workspaces.Get("team"); found {
		t.Error("the refused room was recorded")
	}
	if _, err := os.Stat(filepath.Join(workspaces.Dir(), "team")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a folder was made for a name that was refused: %v", err)
	}

	// The same rule for a record registered another way, and the way out: with the state gone the name is free.
	if err := workspaces.Add(Workspace{Name: "team", Path: filepath.Join(workspaces.Dir(), "team"), Kind: KindRoom}); err == nil {
		t.Error("Add registered a room over a forgotten room's state")
	}
	if err := os.RemoveAll(left); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaces.Create("team", "ana@example.com"); err != nil {
		t.Errorf("a name whose state is gone was still refused: %v", err)
	}
}
