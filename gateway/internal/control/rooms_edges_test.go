package control

import (
	"context"
	"errors"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/protocol"
)

// A prompt with no words and no files is not a message: it is refused before anything is configured, so an empty
// Enter never starts a turn (ADR 0034 §7).
func TestAnEmptyPromptIsRefused(t *testing.T) {
	ctl := controlWithWorker(t, newFakeWorker())
	if err := ctl.Prompt(context.Background(), Caller{Admin: true}, nil, "1", "   ", protocol.WhenBusyFollowUp, nil); err == nil {
		t.Fatal("an empty prompt was admitted")
	}
}

// A command that fails is reported rather than swallowed: the busy sign is taken down and the caller hears it
// (the live path is `expandCommand`'s action, the same one `/clear` takes).
func TestAFailedCommandIsReported(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["expand"] = map[string]any{"action": "compact", "text": "/compact"}
	worker.failOn["action"] = errors.New("the harness refused it")
	ctl := controlWithWorker(t, worker)
	ctl.SetWorkspace(Workspace{
		Name: "house", Kind: KindRoom,
		Members: []Member{{Email: "me@example.com", Role: RoleOwner}},
	})
	if err := ctl.Prompt(context.Background(), Caller{Email: "me@example.com"}, nil, "1", "/compact", protocol.WhenBusyFollowUp, nil); err == nil {
		t.Fatal("a failed command was not reported")
	}
}

// A set of the same engine does not throw the manager away: the restart is for a workspace that changed where it
// runs, and doing it for every record write would restart the world (ADR 0034 §3).
func TestSettingTheSameEngineKeepsItsManager(t *testing.T) {
	ctl := controlWithWorker(t, newFakeWorker())
	ctl.opts.Workspace = Workspace{Name: "house", Engine: "host"}
	ctl.engineDone = true
	ctl.SetWorkspace(Workspace{Name: "house", Engine: "host"})
	if !ctl.engineDone {
		t.Error("the same engine resolved its manager again")
	}
}

// A registry that already has roles and kinds is not rewritten: the migration writes only when it changed
// something, because a file that is rewritten on every start is a diff nobody can review (ADR 0034 §8).
func TestARegistryThatAlreadyHasRolesIsNotChanged(t *testing.T) {
	room := Workspace{
		Name: "house", Kind: KindRoom,
		Members: []Member{
			{Email: "me@example.com", Role: RoleOwner},
			{Email: "ana@example.com", Role: RoleGuest},
		},
	}
	migrated := room.migrateRoom("me@example.com")
	if migrated.changed || migrated.guessed || migrated.ownerless {
		t.Errorf("a migrated room changed: %+v", migrated)
	}
}

// A member with no address is not a person: the migration drops it rather than keeping a row nobody can be.
func TestAMemberWithNoAddressIsDropped(t *testing.T) {
	out, unnamed, changed := membersWithRoles([]Member{{Email: "  ", Role: RoleGuest}})
	if len(out) != 0 || unnamed || !changed {
		t.Errorf("a member with no address survived: %+v, unnamed %v, changed %v", out, unnamed, changed)
	}
}
