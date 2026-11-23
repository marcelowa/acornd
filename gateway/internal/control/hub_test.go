package control

import (
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/protocol"
)

// The update hub on its own, with no Control and no worker. Each test is one of the reasons the
// hub has three roads instead of one queue: events queue, views coalesce, and the state is a
// signal.

func newTestHub() *updateHub {
	hub := &updateHub{}
	hub.prepare()
	return hub
}

func signalled(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

func viewFrame(conversationID string, seq int64) protocol.Frame {
	return protocol.Frame{Type: "view", ConversationID: conversationID, Seq: seq, Value: []byte(`{"seq":` + strconv.FormatInt(seq, 10) + `}`)}
}

// A client that reads nothing loses the middle of a run, never its end. Its event queue drops the
// oldest frame when full, and the view that says the run ended does not go through that queue at
// all: the signal is raised and the cache holds it.
func TestHubASlowClientLosesTheMiddleNeverTheEnd(t *testing.T) {
	hub := newTestHub()
	slow, _ := hub.subscribe("a")

	for index := range subscriberQueue + 5 {
		hub.publish(protocol.Frame{Type: "auth", AuthID: "flow", ID: string(rune('A' + index))})
		hub.publish(viewFrame("a", int64(index%9)+1))
	}
	hub.publish(protocol.Frame{Type: "auth", AuthID: "flow", ID: "last"})
	hub.publish(viewFrame("a", 9))

	if len(slow.Frames) != subscriberQueue {
		t.Fatalf("queue holds %d frames, want it full at %d", len(slow.Frames), subscriberQueue)
	}
	queued := make([]protocol.Frame, 0, subscriberQueue)
	for len(slow.Frames) > 0 {
		frame := <-slow.Frames
		if frame.Type == "view" {
			t.Fatal("a view travelled through the event queue")
		}
		queued = append(queued, frame)
	}
	if queued[0].ID == "A" {
		t.Error("the oldest event survived a full queue; the middle should have gone")
	}
	if newest := queued[len(queued)-1]; newest.ID != "last" {
		t.Errorf("newest queued event = %q, want the last one", newest.ID)
	}
	if !signalled(slow.View) {
		t.Fatal("the slow client was never told its view moved")
	}
	if signalled(slow.View) {
		t.Error("views did not coalesce: a second signal was waiting")
	}
	if view, found := hub.cachedView("a"); !found || view.Seq != 9 {
		t.Errorf("the cached view is %+v (found %v), want the run's end at seq 9", view, found)
	}
}

// A state change while a view is waiting must not replace it, and the view must not swallow the
// state: they are two channels so that neither can push the other out.
func TestHubAStateChangeWhileAViewIsQueued(t *testing.T) {
	hub := newTestHub()
	client, _ := hub.subscribe("a")

	hub.publish(viewFrame("a", 1))
	hub.announce("busy")
	hub.publish(viewFrame("a", 2))
	hub.announce("idle")

	if !signalled(client.View) {
		t.Error("the queued view signal was lost to a state change")
	}
	if !signalled(client.State) {
		t.Error("the state change was lost behind a queued view")
	}
	if signalled(client.State) || signalled(client.View) {
		t.Error("a signal did not coalesce")
	}
	if len(client.Frames) != 0 {
		t.Errorf("signals leaked into the event queue: %d frames", len(client.Frames))
	}
	if view, _ := hub.cachedView("a"); view.Seq != 2 {
		t.Errorf("cached seq = %d, want the newest view", view.Seq)
	}
	if hub.stale("idle") {
		t.Error("the state just announced reads as stale")
	}
	if !hub.stale("busy") {
		t.Error("a state other than the announced one reads as current")
	}
}

// A conversation's events and views stay in that conversation. An event with no conversation, a
// login step, is for everyone.
func TestHubRoutesByConversation(t *testing.T) {
	hub := newTestHub()
	inA, _ := hub.subscribe("a")
	inB, _ := hub.subscribe("b")

	hub.publish(protocol.Frame{Type: "submission", ConversationID: "a", Status: "done"})
	hub.publish(protocol.Frame{Type: "secret", ConversationID: "a"})
	hub.publish(viewFrame("a", 1))
	if len(inA.Frames) != 2 || len(inB.Frames) != 0 {
		t.Errorf("conversation events reached a=%d b=%d, want 2 and 0", len(inA.Frames), len(inB.Frames))
	}
	if !signalled(inA.View) || signalled(inB.View) {
		t.Error("a view woke the wrong conversation")
	}

	hub.publish(protocol.Frame{Type: "submission", Status: "done"})
	hub.publish(protocol.Frame{Type: "auth", ConversationID: "a"})
	if len(inA.Frames) != 4 || len(inB.Frames) != 2 {
		t.Errorf("shared events reached a=%d b=%d, want 4 and 2", len(inA.Frames), len(inB.Frames))
	}
}

// The first watcher of a conversation is what asks the worker to attach, and the last one out is
// what lets it detach. A late joiner starts from the cached view.
func TestHubCountsWatchersPerConversation(t *testing.T) {
	hub := newTestHub()
	hub.publish(viewFrame("a", 3))

	one, first := hub.subscribe("a")
	if !first {
		t.Error("the first watcher of a was not reported as first")
	}
	if one.Initial == nil || one.Initial.Seq != 3 {
		t.Errorf("a late joiner's initial view = %+v, want seq 3", one.Initial)
	}
	two, first := hub.subscribe("a")
	if first {
		t.Error("the second watcher of a was reported as first")
	}
	if two.ID == one.ID {
		t.Error("two subscribers got one id")
	}
	if ticket := hub.ticket(); ticket == one.ID || ticket == two.ID {
		t.Errorf("ticket %d collides with a subscriber id", ticket)
	}
	none, _ := hub.subscribe("b")
	if none.Initial != nil {
		t.Error("a conversation with no view yet handed out an initial one")
	}
	if !hub.watching("a") || hub.watching("c") || !hub.attached() {
		t.Error("watching or attached disagree with the subscriptions")
	}
	if got := hub.watched(); !slices.Contains(got, "a") || !slices.Contains(got, "b") || len(got) != 2 {
		t.Errorf("watched = %v, want a and b", got)
	}

	if left, empty := hub.unsubscribe(one.ID); left != "a" || empty {
		t.Errorf("first release = %q empty=%v, want a with a watcher left", left, empty)
	}
	if left, empty := hub.unsubscribe(two.ID); left != "a" || !empty {
		t.Errorf("last release = %q empty=%v, want a and empty", left, empty)
	}
	if left, empty := hub.unsubscribe(two.ID); left != "" || empty {
		t.Errorf("a second release of one id = %q empty=%v, want nothing", left, empty)
	}
	if got := hub.watched(); len(got) != 1 || got[0] != "b" {
		t.Errorf("watched after release = %v, want only b", got)
	}
	hub.unsubscribe(none.ID)
	if hub.attached() {
		t.Error("attached with nobody subscribed")
	}
}

// A cached view is copied out: the frame a client holds is not the cache, and the cache is not the
// frame the worker sent.
func TestHubCachedViewIsACopy(t *testing.T) {
	hub := newTestHub()
	sent := protocol.Frame{Type: "view", ConversationID: "a", Seq: 4, Value: []byte(`{"x":1}`), Acornd: []byte(`{"by":"me"}`)}
	hub.publish(sent)
	sent.Value[0] = 'X'

	view, found := hub.cachedView("a")
	if !found || string(view.Value) != `{"x":1}` || string(view.Acornd) != `{"by":"me"}` {
		t.Fatalf("cached view = %s %s (found %v)", view.Value, view.Acornd, found)
	}
	view.Value[0] = 'Y'
	if again, _ := hub.cachedView("a"); string(again.Value) != `{"x":1}` {
		t.Errorf("changing a read view changed the cache: %s", again.Value)
	}
	if hub.seq("a") != 4 || hub.seq("none") != 0 {
		t.Errorf("seq = %d and %d, want 4 and 0", hub.seq("a"), hub.seq("none"))
	}
	if _, found := hub.cachedView("none"); found {
		t.Error("a conversation with no view has a cached one")
	}
}

// Publishing never waits for a client. Many publishers and a client that never reads finish, and
// with -race (in CI) this is where a send outside the lock would show.
func TestHubPublishNeverBlocksOnAStalledClient(t *testing.T) {
	hub := newTestHub()
	stalled, _ := hub.subscribe("a")
	var group sync.WaitGroup
	for worker := range 8 {
		group.Go(func() {
			for index := range 200 {
				hub.publish(protocol.Frame{Type: "auth", ID: "step"})
				hub.publish(viewFrame("a", int64(worker+index%2)))
				hub.announce("s")
				extra, _ := hub.subscribe("a")
				hub.unsubscribe(extra.ID)
			}
		})
	}
	group.Wait()
	if len(stalled.Frames) != subscriberQueue || !signalled(stalled.State) || !signalled(stalled.View) {
		t.Errorf("the stalled client holds %d frames; its signals should be up", len(stalled.Frames))
	}
}
