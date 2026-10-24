package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
	"github.com/acornfall/acornfall/gateway/internal/sandbox"
)

// A harness that refuses is a refusal the caller sees. These are the commands whose failure has a
// consequence rather than a log line: a conversation that was not hidden, a fork that did not
// happen, a secret change that did not reach the sandbox. Each one is here because "it worked" was
// the only path tested.

func TestAConversationThatCannotBeHiddenSaysSo(t *testing.T) {
	worker := newFakeWorker()
	worker.failOn["hide"] = errors.New("the harness refused")
	control := controlWithWorker(t, worker)
	if err := control.Hide(context.Background(), "1", true); err == nil {
		t.Fatal("a refused hide should be reported")
	}
}

// Hiding the conversation that is on screen has to move somebody: to the next visible one, or to a
// new one when there is nothing left to show. A hidden room left open is a blank screen with a
// conversation id in the address.
func TestHidingTheLastConversationStartsAnother(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["conversations"] = map[string]any{"conversations": []any{}}
	worker.answer["create"] = map[string]any{"conversationId": "2"}
	control := controlWithWorker(t, worker)
	control.active = "1"
	control.conversations = []protocol.ConversationInfo{{ID: "1"}}

	if err := control.Hide(context.Background(), "1", true); err != nil {
		t.Fatal(err)
	}
	asked := worker.asked()
	created := false
	for _, command := range asked {
		if command.Type == "create" {
			created = true
		}
	}
	if !created {
		t.Errorf("with nothing left to show, a conversation should be started: %+v", asked)
	}
}

// A list that cannot be read after a hide is an error: the interface would otherwise keep showing
// a conversation it no longer has, or hide one it still does.
func TestAHideWhoseRelistFailsIsReported(t *testing.T) {
	worker := newFakeWorker()
	worker.failOn["conversations"] = errors.New("the harness is not answering")
	control := controlWithWorker(t, worker)
	if err := control.Hide(context.Background(), "1", true); err == nil {
		t.Fatal("a failed re-list should be reported")
	}
}

// A fork the harness refuses is not a new conversation, and the caller must not be told it is: the
// child would be opened by an id nobody has.
func TestAForkThatFailsIsReported(t *testing.T) {
	worker := newFakeWorker()
	worker.failOn["fork"] = errors.New("the harness refused")
	control := controlWithWorker(t, worker)
	if err := control.Fork(context.Background(), "1", "entry"); err == nil {
		t.Fatal("a refused fork should be reported")
	}
}

// A fork the harness accepts but does not name is the same failure with a worse shape: an empty id
// would be opened, selected, and written to.
func TestAForkWithoutAnIDIsRefused(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["fork"] = map[string]any{}
	control := controlWithWorker(t, worker)
	if err := control.Fork(context.Background(), "1", "entry"); err == nil {
		t.Fatal("a fork with no id should be reported")
	}
}

// Stopping everything is what a shutdown does, and it stops what it can reach: one harness that
// refuses does not leave the others running, because the process is going away either way.
func TestStoppingEverythingStopsWhatItCan(t *testing.T) {
	first := newFakeWorker()
	first.failOn["abort"] = errors.New("the harness refused")
	second := newFakeWorker()
	control := controlWithWorker(t, first)
	control.conversations = []protocol.ConversationInfo{{ID: "1"}}
	control.promptedAt["1"] = time.Now().UnixMilli()
	other := controlWithWorker(t, second)
	other.conversations = []protocol.ConversationInfo{{ID: "2"}}
	other.promptedAt["2"] = time.Now().UnixMilli()

	workspaces := NewWorkspaces(WorkspacesOptions{WorkspacesDir: t.TempDir()})
	workspaces.controls["one"] = control
	workspaces.controls["two"] = other
	workspaces.AbortAll(context.Background())

	if !second.ran("abort") {
		t.Error("a harness that was not the one that refused should still be told to stop")
	}
}

// A secret change applied to a sandbox that refuses it is an error the caller sees: the person
// saved a value and the tools would still run without it.
func TestASecretChangeThatFailsIsReported(t *testing.T) {
	worker := newFakeWorker()
	control := controlWithWorker(t, worker)
	// The engine is where a change lands, and a workspace whose sandbox was never started has no
	// sandbox to change: that is a refusal the caller sees rather than one that looks applied.
	tier := sandbox.NewTier(t.TempDir(), "", nil)
	if _, err := tier.For("bwrap"); err != nil {
		t.Fatal(err)
	}
	control.opts.Sandboxes = tier
	control.opts.Workspace.Engine = "bwrap"
	control.engineDone = false
	if err := control.ApplySecrets(context.Background(), false); err == nil {
		t.Fatal("a change that never reached a sandbox should be reported")
	}
}

// Selecting a conversation attaches to it and detaches the one before, so the frames describe one
// transcript: two attached conversations is a transcript with another room's lines in it. Selecting
// what is already selected does nothing at all, because a detach-then-attach would drop frames for
// the conversation that never changed.
func TestSelectingAttachesAndDetachesExactly(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["conversations"] = map[string]any{"conversations": []any{}}
	worker.answer["describe"] = protocol.Describe{}
	control := controlWithWorker(t, worker)
	control.active = "old"

	if err := control.Select(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, 4)
	for _, command := range worker.asked() {
		types = append(types, command.Type+":"+command.ConversationID)
	}
	want := []string{"detach:old", "attach:new"}
	for index, one := range want {
		if index >= len(types) || types[index] != one {
			t.Fatalf("commands = %v, want it to start %v", types, want)
		}
	}
	if control.active != "new" {
		t.Errorf("active = %q", control.active)
	}

	// Selecting it again is a no-op: no second attach.
	before := len(worker.asked())
	if err := control.Select(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	if len(worker.asked()) != before {
		t.Errorf("selecting what is already selected sent %d more commands", len(worker.asked())-before)
	}
}

// An attach the harness refuses leaves the active conversation where it was: moving the interface
// to a conversation the worker is not attached to shows a room that will never receive a frame.
func TestARefusedAttachLeavesTheActiveConversationAlone(t *testing.T) {
	worker := newFakeWorker()
	worker.failOn["attach"] = errors.New("the harness refused")
	control := controlWithWorker(t, worker)
	control.active = "old"

	if err := control.Select(context.Background(), "new"); err == nil {
		t.Fatal("a refused attach should be reported")
	}
	if control.active != "old" {
		t.Errorf("active = %q, want it untouched", control.active)
	}
}

// A detach that fails is logged and the attach still happens: the conversation somebody asked for
// is the one they should get, and a stale attachment is the harness's to clear.
func TestADetachThatFailsStillAttaches(t *testing.T) {
	worker := newFakeWorker()
	worker.failOn["detach"] = errors.New("the harness refused")
	worker.answer["describe"] = protocol.Describe{}
	worker.answer["conversations"] = map[string]any{"conversations": []any{}}
	control := controlWithWorker(t, worker)
	control.active = "old"

	if err := control.Select(context.Background(), "new"); err != nil {
		t.Fatalf("a failed detach should not stop the attach: %v", err)
	}
	if !worker.ran("attach") {
		t.Error("the attach should have happened")
	}
	if control.active != "new" {
		t.Errorf("active = %q", control.active)
	}
}

// A conversation that cannot be created is reported, and nothing is selected: an id the harness
// never made would be attached to and written to.
func TestACreateThatFailsIsReported(t *testing.T) {
	worker := newFakeWorker()
	worker.failOn["create"] = errors.New("the harness refused")
	control := controlWithWorker(t, worker)
	if err := control.Create(context.Background()); err == nil {
		t.Fatal("a refused create should be reported")
	}
	if worker.ran("attach") {
		t.Error("nothing should be attached for a conversation that was never made")
	}
}

// A create that answers no id is the same failure: the caller would attach to "".
func TestACreateWithoutAnIDIsRefused(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["create"] = map[string]any{}
	control := controlWithWorker(t, worker)
	if err := control.Create(context.Background()); err == nil {
		t.Fatal("a create with no id should be reported")
	}
}
