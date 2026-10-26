package control

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A member list is the whole of it: no roles inside a workspace, and an address that is not in it is
// refused. Case is folded, because a door that proved Ana@… and a list that says ana@… are one person.
func TestAMemberIsTheWholeOfWhatAWorkspaceAllows(t *testing.T) {
	workspace := Workspace{Name: "house", Members: []string{"ana@example.com", "bo@example.com"}}
	cases := []struct {
		name   string
		caller Caller
		want   bool
	}{
		{"a member", Caller{Email: "ana@example.com"}, true},
		{"a member in another case", Caller{Email: "Ana@Example.COM"}, true},
		{"somebody else", Caller{Email: "eve@example.com"}, false},
		{"nobody", Caller{}, false},
		// The install owner is not a member of anybody's workspace, and owning the install is not a way
		// in: ADR 0032 decision 4 is what makes the privacy claim true (ADR 0032 §4).
		{"the owner who is not a member", Caller{Owner: true, Email: "me@example.com"}, false},
		// An install with no owner claimed is the machine talking to itself: there is no list to check
		// against and nobody to protect it from.
		{"the machine before an owner is claimed", Caller{Owner: true}, true},
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

// A person's list is their own workspaces. The owner's is the install: a list of names, which is what
// administering it means, and not a transcript from inside any of them.
func TestAListShowsAPersonOnlyTheirOwn(t *testing.T) {
	workspaces := registry(t, []Workspace{
		{Name: "general", Path: "/w/general", General: true, Members: []string{"me@example.com"}},
		{Name: "house", Path: "/w/house", Members: []string{"me@example.com", "ana@example.com"}},
		{Name: "ana", Path: "/w/ana", Members: []string{"ana@example.com"}},
	})

	names := func(caller Caller) []string {
		var out []string
		for _, workspace := range workspaces.ListFor(caller) {
			out = append(out, workspace.Name)
		}
		slices.Sort(out)
		return out
	}

	if got := names(Caller{Owner: true, Email: "me@example.com"}); len(got) != 3 {
		t.Errorf("the owner sees %v, want the install", got)
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
		{Name: "general", Path: "/w/general", General: true, Members: []string{"ana@example.com"}},
	})
	ctx := context.Background()
	_, _, missing := workspaces.Route(ctx, Caller{Email: "ana@example.com"}, "nowhere")
	_, _, forbidden := workspaces.Route(ctx, Caller{Email: "eve@example.com"}, "general")
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

// The workspaces that existed before there were people are the owner's. Recording that is a fact rather
// than a grant: every conversation in the registry was the owner's, and without the record the first
// boot with a door would hide all of them from the person at the keyboard.
func TestAWorkspaceThatPredatesPeopleIsTheOwners(t *testing.T) {
	dataDir := t.TempDir()
	workspaces := registryIn(t, dataDir, "me@example.com", []Workspace{
		{Name: "general", Path: "/w/general", General: true},
		{Name: "house", Path: "/w/house", Members: []string{"ana@example.com"}},
	})
	if err := workspaces.nameTheOwnersWorkspaces(); err != nil {
		t.Fatalf("name: %v", err)
	}
	if got := workspaces.registry["general"].Members; !slices.Equal(got, []string{"me@example.com"}) {
		t.Errorf("general members = %v, want the owner", got)
	}
	// A workspace that already names somebody is not touched: the migration fills what nobody decided.
	if got := workspaces.registry["house"].Members; !slices.Equal(got, []string{"ana@example.com"}) {
		t.Errorf("house members = %v, want them left alone", got)
	}
	// And it is stored, not just in memory: the next boot reads the file.
	reloaded := registryIn(t, dataDir, "me@example.com", nil)
	if err := reloaded.loadRegistry(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := reloaded.registry["general"].Members; !slices.Equal(got, []string{"me@example.com"}) {
		t.Errorf("after a reload general members = %v, want the owner", got)
	}
}

// With no owner claimed there is nobody to name a workspace for, and the migration says nothing rather
// than inventing an address.
func TestTheMigrationSaysNothingWithoutAnOwner(t *testing.T) {
	workspaces := registryWithOwners(t, "", []Workspace{{Name: "general", Path: "/w/general", General: true}})
	if err := workspaces.nameTheOwnersWorkspaces(); err != nil {
		t.Fatalf("name: %v", err)
	}
	if got := workspaces.registry["general"].Members; len(got) != 0 {
		t.Errorf("members = %v, want nobody", got)
	}
}

// registry is a workspace layer over a hand-written registry file: no worker, no sandbox, because the
// rules these tests check are decisions rather than processes.
func registry(t *testing.T, list []Workspace) *Workspaces {
	t.Helper()
	return registryWithOwners(t, "", list)
}

func registryWithOwners(t *testing.T, owner string, list []Workspace) *Workspaces {
	t.Helper()
	return registryIn(t, t.TempDir(), owner, list)
}

func registryIn(t *testing.T, dataDir, owner string, list []Workspace) *Workspaces {
	t.Helper()
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: dataDir, OwnerEmail: owner})
	if list != nil {
		if err := SaveRegistryFile(RegistryFile{Path: filepath.Join(dataDir, registryFile), WorkspacesDir: workspaces.Dir(), Workspaces: list}); err != nil {
			t.Fatalf("save registry: %v", err)
		}
	}
	if err := workspaces.loadRegistry(); err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return workspaces
}
