package control

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// May is the whole permission table for a room, and every row is one this ADR decided: a guest speaks,
// compacts and reads; everything else is the owner's (ADR 0034 §4).
func TestMayIsTheRoomsTable(t *testing.T) {
	room := Workspace{
		Name: "house", Kind: KindRoom,
		Members: []Member{
			{Email: "me@example.com", Role: RoleOwner},
			{Email: "ana@example.com", Role: RoleGuest},
		},
	}
	cases := []struct {
		name   string
		caller Caller
		action Action
		want   bool
	}{
		{"the owner prompts", Caller{Email: "me@example.com"}, ActionPrompt, true},
		{"the owner compacts", Caller{Email: "me@example.com"}, ActionCompact, true},
		{"the owner creates", Caller{Email: "me@example.com"}, ActionCreate, true},
		{"the owner changes the model", Caller{Email: "me@example.com"}, ActionModel, true},
		{"a guest prompts", Caller{Email: "ana@example.com"}, ActionPrompt, true},
		{"a guest compacts", Caller{Email: "ana@example.com"}, ActionCompact, true},
		{"a guest creates", Caller{Email: "ana@example.com"}, ActionCreate, false},
		{"a guest forks", Caller{Email: "ana@example.com"}, ActionFork, false},
		{"a guest hides", Caller{Email: "ana@example.com"}, ActionHide, false},
		{"a guest renames", Caller{Email: "ana@example.com"}, ActionRename, false},
		{"a guest resets", Caller{Email: "ana@example.com"}, ActionReset, false},
		{"a guest changes the model", Caller{Email: "ana@example.com"}, ActionModel, false},
		{"a guest administers", Caller{Email: "ana@example.com"}, ActionAdminister, false},
		{"a stranger prompts", Caller{Email: "eve@example.com"}, ActionPrompt, false},
		{"the install before an admin is claimed", Caller{Admin: true}, ActionModel, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := room.May(test.caller, test.action); got != test.want {
				t.Errorf("May(%+v, %s) = %v, want %v", test.caller, test.action, got, test.want)
			}
		})
	}
}

// memberships is a workspace layer holding one room, with an owner and two guests.
func memberships(t *testing.T) *Workspaces {
	t.Helper()
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: t.TempDir(), AdminEmail: "me@example.com"})
	err := workspaces.Add(Workspace{
		Name: "house", Path: filepath.Join(t.TempDir(), "house"), Kind: KindRoom,
		Members: []Member{
			{Email: "me@example.com", Role: RoleOwner},
			{Email: "ana@example.com", Role: RoleGuest},
			{Email: "bo@example.com", Role: RoleGuest},
		},
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	return workspaces
}

// A room's owner is the only one who invites or removes; a guest may leave, and the owner never leaves because
// a room with nobody in it is a room nobody can open (ADR 0034 §3).
func TestOnlyTheOwnerInvitesAndRemoves(t *testing.T) {
	owner := Caller{Email: "me@example.com"}
	ana := Caller{Email: "ana@example.com"}

	workspaces := memberships(t)
	if _, err := workspaces.AddGuest(ana, "house", "eve@example.com"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a guest invited somebody: %v", err)
	}
	added, err := workspaces.AddGuest(owner, "house", "Eve@Example.com")
	if err != nil {
		t.Fatalf("the owner invited: %v", err)
	}
	if role := added.RoleOf("eve@example.com"); role != RoleGuest {
		t.Errorf("the invitation made %q, want a guest", role)
	}
	if _, err := workspaces.AddGuest(owner, "house", "eve@example.com"); err == nil {
		t.Error("the same address was invited twice")
	}
	if _, err := workspaces.RemoveMember(ana, "house", "bo@example.com"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a guest removed somebody else: %v", err)
	}
	if _, err := workspaces.RemoveMember(ana, "house", "ana@example.com"); err != nil {
		t.Errorf("a guest could not leave: %v", err)
	}
	if _, err := workspaces.RemoveMember(owner, "house", "me@example.com"); err == nil {
		t.Error("the owner left")
	}
	if _, err := workspaces.RemoveMember(owner, "house", "eve@example.com"); err != nil {
		t.Errorf("the owner could not remove a guest: %v", err)
	}
	if _, err := workspaces.RemoveMember(owner, "house", "nobody@example.com"); err == nil {
		t.Error("removing somebody who is not in the room succeeded")
	}
	if _, err := workspaces.AddGuest(Caller{Email: "eve@example.com"}, "house", "eve@example.com"); !errors.Is(err, ErrNoWorkspace) {
		t.Errorf("a stranger added somebody: %v", err)
	}
}

// A personal room is never shared, by either route (ADR 0034 §5).
func TestAPersonalRoomRefusesAGuestThroughTheDoor(t *testing.T) {
	workspaces := NewWorkspaces(WorkspacesOptions{DataDir: t.TempDir(), AdminEmail: "me@example.com"})
	if err := workspaces.Add(Workspace{
		Name: PersonalWorkspaceName, Path: filepath.Join(t.TempDir(), "personal"), Kind: KindPersonal,
		Members: []Member{{Email: "me@example.com", Role: RoleOwner}},
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := workspaces.AddGuest(Caller{Email: "me@example.com"}, PersonalWorkspaceName, "ana@example.com"); err == nil {
		t.Error("a personal room accepted a guest")
	}
}

// `/clear` and `/compact` are commands, not routes, so the permission has to be asked where the action is chosen:
// a guest may compact the room's context and may not reset it (ADR 0034 §4).
func TestAGuestMayCompactButMayNotReset(t *testing.T) {
	worker := newFakeWorker()
	ctl := controlWithWorker(t, worker)
	ctl.applyDescribe(protocol.Describe{Providers: []protocol.ProviderInfo{{
		ID: "faux", Authenticated: true, Models: []protocol.ModelInfo{{ID: "faux-1"}},
	}}})
	ctl.resolved["1"] = &protocol.ResolvedAgent{Model: "faux/faux-1"}
	ctl.bindings["1"] = protocol.ModelBinding{Provider: "faux", ModelID: "faux-1"}
	ctl.SetWorkspace(Workspace{
		Name: "house", Kind: KindRoom,
		Members: []Member{
			{Email: "me@example.com", Role: RoleOwner},
			{Email: "ana@example.com", Role: RoleGuest},
		},
	})
	guest := Caller{Email: "ana@example.com"}

	worker.answer["expand"] = map[string]any{"action": "reset", "text": "/clear"}
	if err := ctl.Prompt(context.Background(), guest, nil, "1", "/clear", protocol.WhenBusyFollowUp, nil); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("a guest cleared the room's context: %v", err)
	}

	worker.answer["expand"] = map[string]any{"action": "compact", "text": "/compact"}
	worker.answer["action"] = map[string]any{"conversationId": "1"}
	if err := ctl.Prompt(context.Background(), guest, nil, "1", "/compact", protocol.WhenBusyFollowUp, nil); err != nil {
		t.Errorf("a guest could not compact: %v", err)
	}
}
