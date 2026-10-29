package control

import (
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// How a frame from the harness is routed, and the draft rules. Both are small functions where a
// mutation is a conversation that goes quiet or a client that sees another room's transcript.

// A view is one conversation's transcript. A client watching another conversation must never see
// it: switching away mid-run would otherwise show the run writing into the room just opened.
func TestAViewOnlyReachesTheClientWatchingThatConversation(t *testing.T) {
	c := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}})
	watching, other, anonymous := make(chan protocol.Frame, 4), make(chan protocol.Frame, 4), make(chan protocol.Frame, 4)
	c.subscribers[1] = watching
	c.subscriberConversation[1] = "room-1"
	_ = other
	c.subscribers[2] = other
	c.subscriberConversation[2] = "room-2"
	c.subscribers[3] = anonymous

	c.onFrame(protocol.Frame{Type: "view", ConversationID: "room-1", Seq: 7, Value: []byte(`{}`)})

	if len(watching) != 1 {
		t.Errorf("the client watching room-1 got %d frames", len(watching))
	}
	if len(other) != 0 {
		t.Errorf("a client watching room-2 saw room-1's view")
	}
	// And the cache holds the newest view, which is what a client that reconnects is given.
	if got := c.views["room-1"].seq; got != 7 {
		t.Errorf("cached seq = %d, want 7", got)
	}
}

// A frame that belongs to no conversation is for everyone: a login prompt is not a transcript, and
// dropping it would leave a client waiting for a form that already arrived.
func TestAFrameWithNoConversationReachesEveryone(t *testing.T) {
	c := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}})
	first, second := make(chan protocol.Frame, 4), make(chan protocol.Frame, 4)
	c.subscribers[1] = first
	c.subscriberConversation[1] = "room-1"
	c.subscribers[2] = second
	c.subscriberConversation[2] = "room-2"

	c.onFrame(protocol.Frame{Type: "submission", Status: "done"})
	if len(first) != 1 || len(second) != 1 {
		t.Errorf("a submission with no conversation reached %d and %d clients", len(first), len(second))
	}
}

// A state frame is a signal, not a payload: it wakes every client through the stale channel so a
// slow one cannot drop it. A client that never hears the state moved keeps the state it read
// before, which is how a busy sign outlives its run.
func TestAStateFrameIsASignalNotAPayload(t *testing.T) {
	c := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}})
	stream := make(chan protocol.Frame, 4)
	c.subscribers[1] = stream
	stale := make(chan struct{}, 1)
	c.staleSignals[1] = stale

	c.onFrame(protocol.Frame{Type: "state"})

	if len(stream) != 0 {
		t.Error("a state frame should not be forwarded as a frame")
	}
	select {
	case <-stale:
	default:
		t.Error("a state frame should wake the clients that hold a stale state")
	}
}

// A draft is the composer's, and it is bounded: a paste of a megabyte is refused rather than
// carried in every state a client asks for.
func TestADraftIsBounded(t *testing.T) {
	c := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}, DraftsPath: t.TempDir() + "/drafts.json"})
	if _, err := c.SetDraft(Caller{}, "", "text", nil); err == nil {
		t.Error("a draft needs a conversation")
	}
	atLimit := strings.Repeat("x", maxDraftBytes)
	if _, err := c.SetDraft(Caller{}, "1", atLimit, nil); err != nil {
		t.Errorf("a draft at the limit should be kept: %v", err)
	}
	if _, err := c.SetDraft(Caller{}, "1", atLimit+"x", nil); err == nil {
		t.Error("a draft over the limit should be refused")
	} else if !strings.Contains(err.Error(), "KB at most") {
		t.Errorf("the refusal should say the limit: %v", err)
	}
	// An attachment without an id is not an attachment: it would be a reference to nothing.
	draft, err := c.SetDraft(Caller{}, "1", "text", []DraftAttachment{{ID: ""}, {ID: "a1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Attachments) != 1 || draft.Attachments[0].ID != "a1" {
		t.Errorf("attachments = %+v", draft.Attachments)
	}
}

// Clearing a draft leaves an entry with a fresh stamp. A delete would leave the other machines
// holding a draft that no state event contradicts, so the words would stay on their screen.
func TestClearingADraftSaysSoRatherThanDeletingIt(t *testing.T) {
	c := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}, DraftsPath: t.TempDir() + "/drafts.json"})
	if _, err := c.SetDraft(Caller{}, "1", "half a sentence", nil); err != nil {
		t.Fatal(err)
	}
	c.clearDraft("", "1")
	cleared, found := c.drafts[draftKey{conversation: "1"}]
	if !found {
		t.Fatal("clearing should leave a stamped entry, not remove it")
	}
	if cleared.Text != "" || len(cleared.Attachments) != 0 {
		t.Errorf("the draft should be empty, got %+v", cleared)
	}
	if cleared.UpdatedAt == 0 {
		t.Error("the entry needs a stamp: that is what tells other machines the words are gone")
	}
	// Clearing one that is already empty changes nothing, so the stamp does not move for ever.
	before := cleared.UpdatedAt
	c.clearDraft("", "1")
	if after := c.drafts[draftKey{conversation: "1"}].UpdatedAt; after != before {
		t.Errorf("clearing an empty draft moved its stamp from %d to %d", before, after)
	}
	// And one that was never written is not created by clearing it.
	c.clearDraft("", "never")
	if _, found := c.drafts[draftKey{conversation: "never"}]; found {
		t.Error("clearing a draft nobody wrote should not create one")
	}
}
