package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A member list is the whole of it: no roles inside a workspace, and an address that is not in it is
// refused. Case is folded, because a door that proved Ana@… and a list that says ana@… are one person.
func TestAMemberIsTheWholeOfWhatAWorkspaceAllows(t *testing.T) {
	workspace := Workspace{Name: "house", Kind: KindRoom, Members: []Member{
		{Email: "ana@example.com", Role: RoleOwner},
		{Email: "bo@example.com", Role: RoleGuest},
	}}
	cases := []struct {
		name   string
		caller Caller
		want   bool
	}{
		{"a member", Caller{Email: "ana@example.com"}, true},
		{"a member in another case", Caller{Email: "Ana@Example.COM"}, true},
		{"somebody else", Caller{Email: "eve@example.com"}, false},
		{"nobody", Caller{}, false},
		// The install admin is not a member of anybody's workspace, and owning the install is not a way
		// in: ADR 0032 decision 4 is what makes the privacy claim true (ADR 0032 §4).
		{"the admin who is not a member", Caller{Admin: true, Email: "me@example.com"}, false},
		// An install with no admin claimed is the machine talking to itself: there is no list to check
		// against and nobody to protect it from.
		{"the machine before an admin is claimed", Caller{Admin: true}, true},
		{"a stranger with no address", Caller{}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := workspace.Allows(test.caller); got != test.want {
				t.Errorf("Allows(%+v) = %v, want %v", test.caller, got, test.want)
			}
		})
	}
}

// A person's list is their own workspaces. The admin's is the install: a list of names, which is what
// administering it means, and not a transcript from inside any of them.
func TestAListShowsAPersonOnlyTheirOwn(t *testing.T) {
	workspaces := registry(t, []Workspace{
		{Name: "personal", Path: "/w/personal", Kind: KindPersonal, Members: []Member{{Email: "me@example.com", Role: RoleOwner}}},
		{Name: "house", Path: "/w/house", Kind: KindRoom, Members: []Member{
			{Email: "me@example.com", Role: RoleOwner},
			{Email: "ana@example.com", Role: RoleGuest},
		}},
		{Name: "ana", Path: "/w/ana", Kind: KindPersonal, Members: []Member{{Email: "ana@example.com", Role: RoleOwner}}},
	})

	names := func(caller Caller) []string {
		var out []string
		for _, workspace := range workspaces.ListFor(caller) {
			out = append(out, workspace.Name)
		}
		slices.Sort(out)
		return out
	}

	if got := names(Caller{Admin: true, Email: "me@example.com"}); len(got) != 3 {
		t.Errorf("the admin sees %v, want the install", got)
	}
	if got := names(Caller{Email: "ana@example.com"}); !slices.Equal(got, []string{"ana", "house"}) {
		t.Errorf("a person sees %v, want only their own", got)
	}
	if got := names(Caller{Email: "eve@example.com"}); len(got) != 0 {
		t.Errorf("a stranger sees %v, want nothing", got)
	}
}

// The refusal does not say which of the two it was. A status or a sentence that told "not there" from
// "not yours" apart is how somebody enumerates the workspaces they cannot see.
func TestARouteRefusesWithoutSayingWhichRefusalItIs(t *testing.T) {
	workspaces := registry(t, []Workspace{
		{Name: "personal", Path: "/w/personal", Kind: KindPersonal, Members: []Member{{Email: "ana@example.com", Role: RoleOwner}}},
	})
	ctx := context.Background()
	_, _, missing := workspaces.Route(ctx, Caller{Email: "ana@example.com"}, "nowhere")
	_, _, forbidden := workspaces.Route(ctx, Caller{Email: "eve@example.com"}, "personal")
	for _, err := range []error{missing, forbidden} {
		if !errors.Is(err, ErrNoWorkspace) {
			t.Fatalf("err = %v, want ErrNoWorkspace", err)
		}
	}
	// The two differ only by the name the caller typed, which tells them nothing they did not know.
	if !equalApartFromName(missing.Error(), forbidden.Error()) {
		t.Errorf("the refusals read differently: %q and %q", missing, forbidden)
	}
}

// equalApartFromName reports whether two refusals are the same sentence once the workspace name is
// removed: the name is the caller's own input, so it is not a disclosure.
func equalApartFromName(a, b string) bool {
	strip := func(s string) string {
		before, _, found := strings.Cut(s, `"`)
		if !found {
			return s
		}
		return before
	}
	return strip(a) == strip(b)
}

// registry is a workspace layer over a hand-written registry file: no worker, no sandbox, because the
// rules these tests check are decisions rather than processes.
func registry(t *testing.T, list []Workspace) *Workspaces {
	t.Helper()
	return registryWithAdmins(t, "", list)
}

func registryWithAdmins(t *testing.T, admin string, list []Workspace) *Workspaces {
	t.Helper()
	return registryIn(t, t.TempDir(), admin, list)
}

func registryIn(t *testing.T, dataDir, admin string, list []Workspace) *Workspaces {
	t.Helper()
	// The folder is a temp one and it is what the file records, so a test that makes a room puts it in the
	// scratch folder rather than in the home directory of whoever runs the suite (G16).
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, AdminEmail: admin})
	if err := SaveRegistryFile(RegistryFile{Path: filepath.Join(dataDir, registryFile), WorkspacesDir: t.TempDir(), Workspaces: list}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return workspaces
}

// A person's own workspace is named after their address, has exactly one member, and is labelled as theirs
// rather than named as it: the console shows "Personal", and the name exists because one registry needs one.
func TestAPersonsOwnWorkspaceIsNamedAfterThem(t *testing.T) {
	workspaces := registry(t, []Workspace{{Name: "personal", Path: "/w/personal"}})
	created, err := workspaces.CreatePersonal("Ana.Silva+acorn@Example.com")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Name != "ana.silva+acorn" {
		t.Errorf("name = %q, want the readable part of the address", created.Name)
	}
	if created.Title != "Personal" {
		t.Errorf("title = %q, want the label the console shows", created.Title)
	}
	if !slices.Equal(created.Members, []Member{{Email: "ana.silva+acorn@example.com", Role: RoleOwner}}) {
		t.Errorf("members = %v, want just them as its owner, lowercased", created.Members)
	}
	if !created.Personal() || created.Kind != KindPersonal {
		t.Errorf("a person's own room is %q, want %q", created.Kind, KindPersonal)
	}
	// And it is a real workspace: the folder exists, because the agent works in it.
	if info, err := os.Stat(created.Path); err != nil || !info.IsDir() {
		t.Errorf("the workspace has no folder: %v", err)
	}
}

// Two people whose addresses start the same way get two workspaces, not one. The second Ana is ana-2, and
// neither of them loses their history to a collision.
func TestASecondAnaGetsANumber(t *testing.T) {
	workspaces := registry(t, nil)
	first, err := workspaces.CreatePersonal("ana@example.com")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := workspaces.CreatePersonal("ana@another.example.com")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Name != "ana" || second.Name != "ana-2" {
		t.Errorf("names = %q, %q; want ana then ana-2", first.Name, second.Name)
	}
	if second.Owner() != "ana@another.example.com" {
		t.Errorf("the second workspace's owner = %q", second.Owner())
	}
}

// The workspace is found, not made again: every request a person makes lands on the same one, and a registry
// with a new Ana in it for each request would be a workspace per page load.
func TestAPersonsOwnWorkspaceIsFoundRatherThanMadeAgain(t *testing.T) {
	workspaces := registry(t, []Workspace{{Name: "personal", Path: "/w/personal"}})
	caller := Caller{Email: "ana@example.com"}
	first, err := workspaces.personal(caller)
	if err != nil {
		t.Fatalf("personal: %v", err)
	}
	second, err := workspaces.personal(caller)
	if err != nil {
		t.Fatalf("personal again: %v", err)
	}
	if first != second {
		t.Errorf("the second request made %q, and the first made %q", second, first)
	}
	owned := 0
	for _, workspace := range workspaces.List() {
		if workspace.RoleOf("ana@example.com") != "" {
			owned++
		}
	}
	if owned != 1 {
		t.Errorf("%d workspaces belong to one person, want 1", owned)
	}
}

// The admin's default stays the general workspace until the rename in ADR 0032 §9 lands: this slice is about
// a person having somewhere to land, not about what the install's own workspace is called.
func TestTheAdminStillLandsOnTheGeneralWorkspace(t *testing.T) {
	workspaces := registry(t, nil)
	for _, caller := range []Caller{{Admin: true}, {Admin: true, Email: "me@example.com"}} {
		name, err := workspaces.personal(caller)
		if err != nil {
			t.Fatalf("personal: %v", err)
		}
		if name != PersonalWorkspaceName {
			t.Errorf("the admin's default = %q, want %q", name, PersonalWorkspaceName)
		}
	}
}

// A handle is a folder name, so everything a folder cannot hold becomes a dash and an address that reduces to
// nothing still gets a workspace.
func TestAHandleIsAFolderName(t *testing.T) {
	cases := map[string]string{
		"ana@example.com":           "ana",
		"Ana.Silva@example.com":     "ana.silva",
		"a b/c@example.com":         "a-b-c",
		"ünïcode@example.com":       "n-code",
		"@example.com":              "personal",
		"":                          "personal",
		"+tag@example.com":          "+tag",
		"dots.and-dashes_x@test.io": "dots.and-dashes_x",
	}
	for email, want := range cases {
		if got := handle(email); got != want {
			t.Errorf("handle(%q) = %q, want %q", email, got, want)
		}
	}
}

// The name search ends whatever the registry holds, and it is written so that no mutant can stop it ending: a
// range has no increment to invert and no condition to negate, so a busy registry gives a refusal rather than a
// loop that never returns. This pins the refusal, which is the half a hang would have hidden.
func TestANameSearchThatFindsNothingRefuses(t *testing.T) {
	workspaces := registry(t, nil)
	for attempt := range 100 {
		name := "ana"
		if attempt > 0 {
			name = fmt.Sprintf("ana-%d", attempt+1)
		}
		workspaces.registry[name] = Workspace{Name: name, Path: "/w/" + name}
	}
	if _, err := workspaces.CreatePersonal("ana@example.com"); err == nil {
		t.Fatal("a full registry produced a name")
	}
}

// The install's own room was created before there were people, so it has no member. Once the admin is known it is
// given one, as its owner: nobody named is the state of the file rather than a decision, and the admin's claim is
// the only answer there is (ADR 0034 §8).
func TestTheAdminsOwnRoomIsGivenItsMember(t *testing.T) {
	workspaces := registryWithAdmins(t,
		"admin@example.com",
		[]Workspace{{Name: PersonalWorkspaceName, Path: "/w/personal"}},
	)
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatalf("load: %v", err)
	}
	personal, ok := workspaces.registry[PersonalWorkspaceName]
	if !ok {
		t.Fatal("the personal workspace is gone")
	}
	if personal.Owner() != "admin@example.com" || len(personal.Members) != 1 {
		t.Errorf("the room's members are %v, want the admin as its owner", personal.Members)
	}
	if !personal.Personal() {
		t.Error("naming its member stopped it being somebody's own room")
	}

	// A room with members is left alone: this migration fills a blank, it does not rewrite a fact.
	shared := registryWithAdmins(t, "admin@example.com", []Workspace{
		{Name: PersonalWorkspaceName, Path: "/w/personal"},
		{Name: "shared", Path: "/w/shared", Kind: KindRoom, Members: []Member{
			{Email: "ana@example.com", Role: RoleOwner},
			{Email: "bea@example.com", Role: RoleGuest},
		}},
	})
	if err := shared.loadRegistry(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := shared.registry["shared"]; got.Owner() != "ana@example.com" || len(got.Members) != 2 {
		t.Errorf("a shared room was rewritten: %v", got.Members)
	}
}

// An install with no admin yet keeps its own room with nobody in it, and the room is still somebody's own: the
// kind is what says so, not the list, so a room nobody has been named in does not lose the two rules that follow
// from it.
func TestAnInstallWithNoAdminKeepsBothShapesOfTheOwnRoom(t *testing.T) {
	workspaces := registryWithAdmins(t, "", []Workspace{
		{Name: PersonalWorkspaceName, Path: "/w/personal"},
		{Name: "ana", Path: "/w/ana", Kind: KindPersonal, Members: []Member{{Email: "ana@example.com", Role: RoleOwner}}},
	})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatalf("load: %v", err)
	}
	ownerless := workspaces.registry[PersonalWorkspaceName]
	if len(ownerless.Members) != 0 {
		t.Errorf("an install with no admin invented a member: %v", ownerless.Members)
	}
	if !ownerless.Personal() {
		t.Error("a room with no members is not somebody's own room")
	}
	one := workspaces.registry["ana"]
	if one.Owner() != "ana@example.com" || !one.Personal() {
		t.Errorf("a room with one member is %+v, want somebody's own, owned by them", one)
	}
}

// A claim after boot is the same moment as an install that had an owner from the start: the room that predates
// people becomes the new admin's, without a restart, and a room somebody already holds is left alone.
func TestClaimingTheInstallHandsTheOwnerlessRoomToTheNewAdmin(t *testing.T) {
	workspaces := registryWithAdmins(t, "", []Workspace{
		{Name: PersonalWorkspaceName, Path: "/w/personal"},
		{Name: "ana", Path: "/w/ana", Kind: KindPersonal, Members: []Member{{Email: "ana@example.com", Role: RoleOwner}}},
	})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := workspaces.ClaimOwner("Me@Example.com"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	owned := workspaces.registry[PersonalWorkspaceName]
	if owned.Owner() != "me@example.com" {
		t.Errorf("the install's room is owned by %q, want the new admin", owned.Owner())
	}
	if ana := workspaces.registry["ana"]; ana.Owner() != "ana@example.com" {
		t.Errorf("a room with an owner was taken: %v", ana.Members)
	}
	// Written, not just held: a restart has to find the same answer.
	reloaded := registryWithAdmins(t, "", nil)
	reloaded.opts.DataDir = workspaces.opts.DataDir
	if err := reloaded.loadRegistry(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.registry[PersonalWorkspaceName].Owner() != "me@example.com" {
		t.Errorf("the claim was not written down: %+v", reloaded.registry[PersonalWorkspaceName])
	}
	if err := workspaces.ClaimOwner("other@example.com"); err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if workspaces.registry[PersonalWorkspaceName].Owner() != "me@example.com" {
		t.Error("a second claim took a room that already had an owner")
	}
}

// The install's own room names the admin when there is one, and nobody when there is not: the claim is what puts
// a person in it, and a room that invented a member before anybody claimed would be a room with no owner.
func TestTheInstallsOwnRoomNamesTheAdminOnlyWhenThereIsOne(t *testing.T) {
	withAdmin := registryWithAdmins(t, "Me@Example.com", nil)
	members := withAdmin.ownerMembers()
	if len(members) != 1 || members[0].Email != "me@example.com" || members[0].Role != RoleOwner {
		t.Fatalf("ownerMembers() = %+v, want the admin as the owner", members)
	}
	if none := registryWithAdmins(t, "", nil).ownerMembers(); len(none) != 0 {
		t.Errorf("an install with no admin invented %+v", none)
	}
}

// A member written as a bare address is read with no role; an empty value is a refusal and not a panic.
func TestAMemberOnItsOwnIsReadAsAnAddress(t *testing.T) {
	var member Member
	if err := json.Unmarshal([]byte(`"Ana@Example.com"`), &member); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if member.Email != "Ana@Example.com" || member.Role != "" {
		t.Errorf("a bare address became %+v", member)
	}
	if err := member.UnmarshalJSON(nil); err == nil {
		t.Error("an empty value was read as a member")
	}
}

// A room change the registry cannot save is an error, not a success the next start will forget: the folder is
// broken here (a data folder whose parent is a file), which no user can write to and no root can either.
func TestARoomChangeThatCannotBeSavedIsAnError(t *testing.T) {
	workspaces := registryWithAdmins(t, "owner@example.com", []Workspace{
		{
			Name: "room", Path: "/w/room", Kind: KindRoom,
			Members: []Member{{Email: "owner@example.com", Role: RoleOwner}, {Email: "guest@example.com", Role: RoleGuest}},
		},
	})
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatalf("load: %v", err)
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("not a folder"), 0o600); err != nil {
		t.Fatalf("block the folder: %v", err)
	}
	workspaces.opts.DataDir = filepath.Join(blocked, "sub")
	if _, err := workspaces.RemoveMember(Caller{Email: "owner@example.com"}, "room", "guest@example.com"); err == nil {
		t.Fatal("a change that could not be saved was reported as saved")
	}
}
