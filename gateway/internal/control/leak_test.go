package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// A transcript frame belongs to one conversation and must reach only the clients watching it.
// The three types that carry a conversation are checked by name, so each one has to be tested as
// itself: a view read in the wrong room shows a run writing into the room just opened, and a
// secret prompt read in the wrong room asks somebody else to type a key.
func TestATranscriptFrameReachesOnlyItsOwnRoom(t *testing.T) {
	for _, kind := range []string{"view", "submission", "secret"} {
		control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}, DataDir: t.TempDir()})
		other := make(chan protocol.Frame, 4)
		own := make(chan protocol.Frame, 4)
		control.subscribers[1] = other
		control.subscriberConversation[1] = "somewhere-else"
		control.subscribers[2] = own
		control.subscriberConversation[2] = "here"

		control.publish(protocol.Frame{Type: kind, ConversationID: "here", Status: "running"})

		select {
		case frame := <-own:
			if frame.Type != kind {
				t.Errorf("%s: the room's own watcher got a %q", kind, frame.Type)
			}
		default:
			t.Errorf("%s: the room's own watcher got nothing", kind)
		}
		select {
		case frame := <-other:
			t.Errorf("%s: a client watching another room received it: %+v", kind, frame)
		default:
		}
	}
}

// A frame that belongs to no conversation is for everyone: a login prompt is not part of any
// transcript, and dropping it would leave a sign-in waiting for an answer nobody sees.
func TestAFrameWithNoConversationReachesEveryWatcher(t *testing.T) {
	control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}, DataDir: t.TempDir()})
	first := make(chan protocol.Frame, 4)
	second := make(chan protocol.Frame, 4)
	control.subscribers[1] = first
	control.subscriberConversation[1] = "a"
	control.subscribers[2] = second
	control.subscriberConversation[2] = "b"

	control.publish(protocol.Frame{Type: "auth", Status: "waiting"})

	for name, channel := range map[string]chan protocol.Frame{"a": first, "b": second} {
		select {
		case <-channel:
		default:
			t.Errorf("the watcher of %s got nothing: a frame with no conversation is for everyone", name)
		}
	}
}

// An ACORNFALL_MODEL that is not `provider/model` is a configuration mistake, and a mistake that
// is silently ignored runs every conversation on a model nobody chose.
func TestAMalformedLastResortModelIsReported(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["describe"] = protocol.Describe{}
	control := controlWithWorker(t, worker)
	control.opts.Model = "not-a-provider-slash-model"
	if err := control.Refresh(context.Background()); err == nil {
		t.Fatal("a malformed ACORNFALL_MODEL should be reported, not ignored")
	}
}

// A conversation that cannot be opened is an error, and it is not recorded as resolved: the next
// prompt would otherwise run under an agent the worker never confirmed.
func TestAConversationThatCannotBeOpenedIsNotMarkedResolved(t *testing.T) {
	worker := newFakeWorker()
	worker.failOn["open"] = errors.New("the agent could not be opened")
	worker.answer["describe"] = protocol.Describe{Provider: "faux", Providers: []protocol.ProviderInfo{{
		ID: "faux", Authenticated: true, Models: []protocol.ModelInfo{{ID: "faux-1"}},
	}}}
	control := controlWithWorker(t, worker)
	control.applyDescribe(worker.answer["describe"].(protocol.Describe))
	control.bindings["1"] = protocol.ModelBinding{Provider: "faux", ModelID: "faux-1"}

	if err := control.ensureConversation(context.Background(), "1"); err == nil {
		t.Fatal("a failed open should be reported")
	}
	if control.resolved["1"] != nil {
		t.Error("a conversation that was not opened should not be marked resolved")
	}
}

// A draft that is only attachments is still a draft: sending it clears the files too, and a
// draft left behind after its message was accepted is sent twice.
func TestAPictureOnlyDraftIsClearedWhenItIsSent(t *testing.T) {
	control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}, DataDir: t.TempDir()})
	control.drafts[draftKey{conversation: "1"}] = Draft{Attachments: []DraftAttachment{{ID: "a1", Name: "screenshot.png"}}, UpdatedAt: 1}
	control.clearDraft("", "1")
	if got := control.drafts[draftKey{conversation: "1"}]; len(got.Attachments) != 0 || got.Text != "" {
		t.Errorf("the attachment should be gone: %+v", got)
	}
}

// A workspace nobody ever opened counts as idle for the whole window: it is a workspace that
// started for a claim somebody made and never came back to, and holding its worker is the
// memory eviction exists to reclaim.
func TestAWorkspaceNeverUsedIsEvicted(t *testing.T) {
	workspaces := NewWorkspaces(WorkspacesOptions{WorkspacesDir: t.TempDir()})
	workspaces.registry["never"] = Workspace{Name: "never"}
	workspaces.registry["used"] = Workspace{Name: "used"}
	workspaces.controls["never"] = New(Options{Workspace: Workspace{Name: "never", Path: t.TempDir()}, DataDir: t.TempDir()})
	workspaces.controls["used"] = New(Options{Workspace: Workspace{Name: "used", Path: t.TempDir()}, DataDir: t.TempDir()})
	workspaces.used["used"] = time.Now().Add(-time.Minute)

	never := workspaces.controls["never"]
	stopping := workspaces.evictCandidates()
	if len(stopping) != 1 || stopping[0] != never {
		t.Fatalf("evicted %d, want the workspace nobody opened", len(stopping))
	}
}

// A run with no timestamp is a run the worker did not date, and an undated run is not one this
// can vouch for. The window is measured from the timestamp, so with none there is nothing to
// measure — which is a rule about the guard, not about the size of the clock. The stamps here are
// older than their windows and the clock is small enough that measuring from zero would land
// inside the half hour, so this fails if the guard is dropped.
func TestARunWithNoTimestampIsNotRunningHoweverSmallTheClock(t *testing.T) {
	control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}, DataDir: t.TempDir()})
	now := int64(1_000_000)
	control.promptedAt["1"] = now - 20_000
	control.actionAt["1"] = now - 400_000
	if control.runActive(protocol.ConversationInfo{ID: "1", Running: true}, now) {
		t.Error("a run with no timestamp should not read as running")
	}
}
