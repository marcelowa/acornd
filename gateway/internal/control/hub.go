// the fan-out of frames and state signals to the clients attached to one workspace
//
// Part of the control package. The file is named after the job, not the layer: this is the
// one thing it does, and everything it needs from its siblings is a call, not a comment.
package control

import (
	"encoding/json"
	"sync"

	"github.com/marcelowa/acornd/gateway/internal/protocol"
)

// updateHub is who is attached to a workspace and what each of them has to hear: the event
// queues, the state and view signals, the newest view per conversation, and the state that was
// last announced. It knows nothing about the worker or the conversations; Control decides what
// to publish and the hub decides who gets it.
//
// Lock order: mu is a leaf. Control may call the hub while it holds its own mu, and does (a
// state build reads a view's seq, Unsubscribe compares against the fallback conversation). The
// hub never takes another lock and never calls out while it holds mu, so the order can only ever
// be Control.mu, then updateHub.mu. Sends under mu never block: every one is a select with a
// default.
type updateHub struct {
	mu sync.RWMutex
	// announced is the signature of the state attached clients were last told about. The poll
	// compares the state against it, because a change only time can make has no data behind
	// it: nothing refreshes, and only the comparison across ticks can see it.
	announced   string
	views       map[string]cachedView
	subscribers map[int]chan protocol.Frame
	// staleSignals wakes one client per state change. It is a channel of its own because a
	// state frame is a signal, not a transcript: on the frame channel a slow write would drop
	// it, or the signal would evict an event the client has not read yet, and a client that
	// never hears the state moved keeps the state it read before — busy sign and all.
	staleSignals map[int]chan struct{}
	// viewSignals wakes one client when its conversation's view moved. Views do not queue:
	// a slow write would fill the queue behind them and drop the newest one — the one that
	// says the run ended — and the next view may never come, because nothing is running any
	// more. The signal coalesces, and the client reads the newest cached view when it wakes.
	viewSignals map[int]chan struct{}
	// nextSub numbers subscribers, and login flows share it, so an auth id and a subscriber id
	// never come from two counters.
	nextSub int
	// Which conversation each subscriber watches, and how many watch each. Several
	// clients watching different conversations is what makes browser tabs work.
	subscriberConversation map[int]string
	attachments            map[string]int
}

// cachedView is the last full view per conversation, so switching back is instant
// rather than waiting for the worker's next frame. The annotation is cached with it:
// a view frame is more than its value, and dropping the annotation on the way to a
// newly attached client is invisible until attribution quietly stops working.
type cachedView struct {
	value      []byte
	annotation json.RawMessage
	seq        int64
}

// prepare makes the maps. The zero hub answers every read as empty, which is what a Control
// built without New relies on; only writes need prepare.
func (h *updateHub) prepare() {
	h.views = map[string]cachedView{}
	h.subscribers = map[int]chan protocol.Frame{}
	h.staleSignals = map[int]chan struct{}{}
	h.viewSignals = map[int]chan struct{}{}
	h.subscriberConversation = map[int]string{}
	h.attachments = map[string]int{}
}

// subscriberQueue is how many frames may wait for one client. It is not 1: a login is a
// burst — a status line, an address, and the prompt that asks for an answer arrive within a
// millisecond — and a queue of one loses the middle of it, which is the prompt. A view
// cannot queue at all: it is a signal, so the newest is always one cache read away.
const subscriberQueue = 32

// subscribe registers one client watching one conversation. First is true when nobody watched
// it before, which is when the worker has to be asked to attach.
func (h *updateHub) subscribe(conversationID string) (subscription Subscription, first bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	first = h.attachments[conversationID] == 0
	h.attachments[conversationID] += 1
	id := h.nextSub
	h.nextSub++
	channel := make(chan protocol.Frame, subscriberQueue)
	stale := make(chan struct{}, 1)
	views := make(chan struct{}, 1)
	h.subscribers[id] = channel
	h.staleSignals[id] = stale
	h.viewSignals[id] = views
	h.subscriberConversation[id] = conversationID
	subscription = Subscription{
		ID:           id,
		Conversation: conversationID,
		Frames:       channel,
		State:        stale,
		View:         views,
	}
	if initial, found := h.cachedViewLocked(conversationID); found {
		subscription.Initial = &initial
	}
	return subscription, first
}

// unsubscribe releases one client. Left is the conversation it watched, and empty is true when
// it was the last watcher of a named one.
func (h *updateHub) unsubscribe(id int) (left string, empty bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	left = h.subscriberConversation[id]
	delete(h.subscribers, id)
	delete(h.staleSignals, id)
	delete(h.viewSignals, id)
	delete(h.subscriberConversation, id)
	if left == "" {
		return "", false
	}
	h.attachments[left] -= 1
	if h.attachments[left] < 0 {
		h.attachments[left] = 0
	}
	return left, h.attachments[left] == 0
}

// ticket is the next number from the subscriber counter, for an id that must not collide with
// one already handed out.
func (h *updateHub) ticket() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextSub++
	return h.nextSub
}

// publish fans one frame out to attached clients. A view frame is a complete value, so it does
// not travel through a queue: the cache holds the newest, and a coalescing signal tells each
// watcher to read it. An event frame — a submission, a login step — is one of a sequence and
// cannot be replaced: a dropped prompt leaves a dialog waiting for a question the provider
// already asked. A state frame does not come here; it is announce's.
func (h *updateHub) publish(frame protocol.Frame) {
	h.mu.Lock()
	if frame.Type == "view" {
		h.views[frame.ConversationID] = cachedView{
			value:      append([]byte(nil), frame.Value...),
			annotation: append(json.RawMessage(nil), frame.Acornd...),
			seq:        frame.Seq,
		}
		h.wakeViewsLocked(frame.ConversationID)
		h.mu.Unlock()
		return
	}
	subscribers := make([]chan protocol.Frame, 0, len(h.subscribers))
	for id, channel := range h.subscribers {
		// A submission or a secret prompt is one conversation's: a client watching another
		// conversation must never see it. Frames with no conversation are for everyone: a
		// login prompt belongs to no transcript.
		if (frame.Type == "submission" || frame.Type == "secret") && frame.ConversationID != "" {
			if h.subscriberConversation[id] != frame.ConversationID {
				continue
			}
		}
		subscribers = append(subscribers, channel)
	}
	h.mu.Unlock()

	for _, channel := range subscribers {
		queueFrame(channel, frame)
	}
}

// wakeViewsLocked raises every watcher's view signal for one conversation. A signal already
// waiting says the same thing, so a second view of the same run is not a second wakeup: the
// client reads the newest cache when it gets there, and one read carries everything skipped.
// The caller holds the lock.
func (h *updateHub) wakeViewsLocked(conversationID string) {
	for id, signal := range h.viewSignals {
		if conversationID != "" && h.subscriberConversation[id] != conversationID {
			continue
		}
		select {
		case signal <- struct{}{}:
		default:
		}
	}
}

// queueFrame hands one frame to a subscriber's queue without ever blocking the publisher.
// A client that cannot keep up loses the oldest frame, never the newest; a view does not come
// through here at all, so a stalled socket cannot lose the frame that says its run ended.
func queueFrame(channel chan protocol.Frame, frame protocol.Frame) {
	select {
	case channel <- frame:
		return
	default:
	}
	select {
	case <-channel:
	default:
	}
	select {
	case channel <- frame:
	default:
	}
}

// announce wakes every attached client: the state it holds is stale. A signal already waiting
// says the same thing, so a second one is not a second wakeup. The signature is remembered, so
// stale can tell a change already announced from one only time has made.
func (h *updateHub) announce(signature string) {
	h.mu.Lock()
	h.announced = signature
	signals := make([]chan struct{}, 0, len(h.staleSignals))
	for _, signal := range h.staleSignals {
		signals = append(signals, signal)
	}
	h.mu.Unlock()
	for _, signal := range signals {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
}

// stale reports whether a state with this signature is news to the attached clients.
func (h *updateHub) stale(signature string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return signature != h.announced
}

// cachedView is the newest complete view of one conversation, copied.
func (h *updateHub) cachedView(conversationID string) (protocol.Frame, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cachedViewLocked(conversationID)
}

// cachedViewLocked builds a frame from the cached view. The caller holds the lock, read or
// write, and the slices are copied: the frame outlives the cache entry it was read from.
func (h *updateHub) cachedViewLocked(conversationID string) (protocol.Frame, bool) {
	cached, found := h.views[conversationID]
	if !found || cached.value == nil {
		return protocol.Frame{}, false
	}
	return protocol.Frame{
		Type:           "view",
		ConversationID: conversationID,
		Seq:            cached.seq,
		Value:          append([]byte(nil), cached.value...),
		Acornd:         append(json.RawMessage(nil), cached.annotation...),
	}, true
}

// seq is the sequence number of a conversation's newest view, zero before the first.
func (h *updateHub) seq(conversationID string) int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.views[conversationID].seq
}

// watched lists the conversations at least one client is attached to.
func (h *updateHub) watched() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	list := make([]string, 0, len(h.attachments))
	for id, count := range h.attachments {
		if count > 0 {
			list = append(list, id)
		}
	}
	return list
}

// watching reports whether a client is looking at this conversation.
func (h *updateHub) watching(conversationID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, watched := range h.subscriberConversation {
		if watched == conversationID {
			return true
		}
	}
	return false
}

// attached reports whether any client is attached at all.
func (h *updateHub) attached() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers) > 0
}
