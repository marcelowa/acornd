// a turn: prompting, aborting, attachments, and the frames that go out to clients
//
// Part of the control package. The file is named after the job, not the layer: this is the
// one thing it does, and everything it needs from its siblings is a call, not a comment.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/protocol"
	"github.com/marcelowa/acornd/gateway/internal/workerpool"
)

// expandCommand asks the worker to expand a slash command, because that is where the templates
// live; an unknown name comes back unchanged, so a message that happens to start with a slash is
// never rewritten. A builtin is an action rather than a turn: it is run here, recorded, and
// reported as done, and nothing is sent to the model.
func (c *Control) expandCommand(
	ctx context.Context,
	caller Caller,
	author *protocol.Author,
	conversationID, text string,
) (string, bool, error) {
	var expanded struct {
		Text    string `json:"text"`
		Command string `json:"command"`
		Action  string `json:"action"`
	}
	if err := c.worker.CallInto(ctx, protocol.Command{Type: "expand", Text: text}, &expanded); err != nil {
		workerpool.Log("expanding %q: %v", text, err)
		return text, false, nil
	}
	if expanded.Action != "compact" && expanded.Action != "reset" {
		if expanded.Command != "" {
			return expanded.Text, false, nil
		}
		return text, false, nil
	}
	// `/clear` and `/compact` are commands, not routes, so the route table cannot be the permission table:
	// a guest may compact, and may not reset the room's context (ADR 0034 §4).
	if expanded.Action == "reset" && !c.May(caller, ActionReset) {
		return "", false, notAllowed("clear the room's context")
	}
	if expanded.Action == "compact" && !c.May(caller, ActionCompact) {
		return "", false, notAllowed("compact the room's context")
	}
	now := time.Now().UnixMilli()
	c.mu.Lock()
	c.promptedAt[conversationID] = now
	c.actionAt[conversationID] = now
	c.mu.Unlock()
	// The busy sign goes up with the click, not with the answer: the worker answers this call
	// only once the act and its record are in.
	c.publish(protocol.Frame{Type: "state"})
	var response struct {
		ConversationID string `json:"conversationId"`
	}
	err := c.worker.CallInto(ctx, protocol.Command{
		Type:           "action",
		ConversationID: conversationID,
		Text:           strings.TrimSpace(text),
		Action:         expanded.Action,
		// A builtin is somebody's act, so the record it leaves names them: `/compact` from a guest is not
		// the same event as the owner's, and a transcript that cannot tell them apart reads as a crash.
		Author: author,
	}, &response)
	c.mu.Lock()
	delete(c.promptedAt, conversationID)
	delete(c.actionAt, conversationID)
	c.mu.Unlock()
	if err != nil {
		return text, false, err
	}
	// The invocation was a message like any other: whatever its sender was typing for this room has
	// been said, and their other machines should hear that it is gone. Nobody else's draft is touched.
	c.clearDraft(draftOwner(author), conversationID)
	c.publish(protocol.Frame{Type: "state"})
	return text, true, nil
}

// draftOwner is whose draft a message clears: the address of the person who sent it, which is the author's, and ""
// for an install with no admin claimed — one person in it, and one key to hold their words.
func draftOwner(author *protocol.Author) string {
	if author == nil {
		return ""
	}
	return author.Email
}

// Prompt admits one message. author is who wrote it, or nil when the install can name nobody; it travels to the
// worker, which stores it on the entry and composes the speaker prefix the model reads (ADR 0034 §7).
func (c *Control) Prompt(
	ctx context.Context,
	caller Caller,
	author *protocol.Author,
	conversationID, text string,
	when protocol.WhenBusy,
	attachments []string,
) error {
	// A prompt admitted while a secret restart runs must not start a tool call against the
	// sandbox being replaced. The restart holds this lock across Stop/Produce.
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	// Files may ride alone: a screenshot with no words is still a message.
	if strings.TrimSpace(text) == "" && len(attachments) == 0 {
		return errors.New("empty message")
	}
	// A conversation about to run must be configured first: pinning writes a binding without
	// opening it, and a worker that was never told has no model to run the turn with.
	if err := c.ensureConversation(ctx, conversationID); err != nil {
		return err
	}
	// A message that opens with a slash may be a command.
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		expanded, acted, err := c.expandCommand(ctx, caller, author, conversationID, text)
		if err != nil {
			return err
		}
		if acted {
			return nil
		}
		text = expanded
	}
	if err := c.worker.CallInto(ctx, protocol.Command{
		Type:           "prompt",
		ConversationID: conversationID,
		Text:           text,
		WhenBusy:       when,
		Attachments:    attachments,
		Author:         author,
	}, nil); err != nil {
		return err
	}
	c.mu.Lock()
	c.promptedAt[conversationID] = time.Now().UnixMilli()
	c.mu.Unlock()
	// The words have landed: the sender's draft is what is left to type, which is nothing. Only theirs —
	// clearing the conversation's meant two people typing here erased each other (ADR 0034 §7). The
	// clear is announced by the publish below, to every machine watching the room.
	c.clearDraft(draftOwner(author), conversationID)
	// The grace starts now, and attached tabs hear state moves as signals, not on the
	// next poll: without this a second tab keeps a stale rail for up to three seconds
	// behind the click. Like every other mutation here, the prompt announces.
	c.publish(protocol.Frame{Type: "state"})
	return nil
}

// Abort stops everything running in one conversation and names nobody. It is what a drain does at shutdown, which is
// nobody's act: the note the stop lands as says the run was stopped, without attributing it.
func (c *Control) Abort(ctx context.Context, conversationID string) error {
	return c.stopRun(ctx, nil, conversationID)
}

// AbortBy stops what one person asked to stop, and the note it lands as names them (ADR 0034 §7): a guest stopping a
// member's turn otherwise reads as a crash. No speaker is sent, because a stop is not a message and nothing is
// prefixed with a name.
func (c *Control) AbortBy(ctx context.Context, author *protocol.Author, conversationID string) error {
	return c.stopRun(ctx, author, conversationID)
}

// stopRun is the one path both entry points take: the author is the only difference between them.
func (c *Control) stopRun(ctx context.Context, author *protocol.Author, conversationID string) error {
	c.cancelSecrets(conversationID)
	worker, err := c.currentWorker()
	if err != nil {
		return err
	}
	var stopper *protocol.Author
	if author != nil {
		stopper = &protocol.Author{Email: author.Email, Name: author.Name}
	}
	return worker.CallInto(ctx, protocol.Command{
		Type:           "abort",
		ConversationID: conversationID,
		Author:         stopper,
	}, nil)
}

func (c *Control) Usage(ctx context.Context) (json.RawMessage, error) {
	worker, err := c.currentWorker()
	if err != nil {
		return nil, err
	}
	return worker.Call(ctx, protocol.Command{Type: "usage"})
}

// Attachment is one stored file, answered with the id messages carry.
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	MIME string `json:"mime"`
}

// UploadAttachment stores staged bytes under the workspace's hidden folder.
// The temp file stays the caller's to remove; the worker only reads it.
func (c *Control) UploadAttachment(ctx context.Context, tempPath, name, mime string, size int64) (Attachment, error) {
	var response struct {
		Attachment Attachment `json:"attachment"`
	}
	if err := c.worker.CallInto(ctx, protocol.Command{
		Type:     "attachment",
		TempPath: tempPath,
		FileName: name,
		MIME:     mime,
		FileSize: size,
	}, &response); err != nil {
		return Attachment{}, err
	}
	if response.Attachment.ID == "" {
		return Attachment{}, errors.New("worker returned no attachment id")
	}
	return response.Attachment, nil
}

// Subscription is one client's attachment: the conversation it actually watches — the id it
// named, or the caller's landing when it named none — the queue of events it has not read, and
// the two signals that say what it holds is stale. Initial is the cached view it has not seen
// yet, if there is one.
//
// A view does not travel in Frames. It is a complete value, and a queue can lose the newest one
// to a stalled socket; View is raised instead, and the client reads the newest cached view.
type Subscription struct {
	// ID releases the attachment with Unsubscribe.
	ID           int
	Conversation string
	Frames       <-chan protocol.Frame
	State        <-chan struct{}
	View         <-chan struct{}
	Initial      *protocol.Frame
}

// Subscribe attaches one client to one conversation. The initial view is that conversation's,
// if any, so a late joiner starts from committed state. An empty id means the conversation this
// caller lands on, which is what a fresh tab opens.
//
// Watching a conversation does not move where anybody lands: that is Select's, and it is per person
// (ADR 0034 §7). Two people watching two conversations is the normal case now, not a conflict.
func (c *Control) Subscribe(ctx context.Context, caller Caller, conversationID string) (Subscription, error) {
	// Read before the lock: landingFor takes it, and the two orders would be a deadlock (G4).
	if conversationID == "" {
		conversationID = c.landingFor(caller)
	}
	c.mu.Lock()
	first := c.attachments[conversationID] == 0
	c.attachments[conversationID] += 1
	id := c.nextSub
	c.nextSub++
	channel := make(chan protocol.Frame, subscriberQueue)
	stale := make(chan struct{}, 1)
	views := make(chan struct{}, 1)
	c.subscribers[id] = channel
	c.staleSignals[id] = stale
	c.viewSignals[id] = views
	c.subscriberConversation[id] = conversationID
	initial, hasInitial := c.cachedViewLocked(conversationID)
	c.mu.Unlock()

	// The worker subscribes per conversation, so the first watcher is what asks for it.
	if first && conversationID != "" {
		if _, err := c.worker.Call(ctx, protocol.Command{Type: "attach", ConversationID: conversationID}); err != nil {
			c.Unsubscribe(id)
			return Subscription{}, err
		}
		// Fetch its policy now rather than leaving a client to see state without one until
		// the next refresh tick, which reads as a broken inspector for a few seconds.
		if policy, err := c.worker.Call(ctx, protocol.Command{Type: "policy", ConversationID: conversationID}); err == nil {
			c.mu.Lock()
			c.policies[conversationID] = policy
			c.mu.Unlock()
		}
	}

	subscription := Subscription{
		ID:           id,
		Conversation: conversationID,
		Frames:       channel,
		State:        stale,
		View:         views,
	}
	if hasInitial {
		subscription.Initial = &initial
	}
	return subscription, nil
}

// CachedView is the newest complete view of one conversation, copied, for a client that missed
// frames and has to resynchronize. False means nothing has been published yet, so there is
// nothing to send.
func (c *Control) CachedView(conversationID string) (protocol.Frame, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cachedViewLocked(conversationID)
}

// cachedViewLocked builds a frame from the cached view. The caller holds the lock, read or
// write, and the slices are copied: the frame outlives the cache entry it was read from.
func (c *Control) cachedViewLocked(conversationID string) (protocol.Frame, bool) {
	cached, found := c.views[conversationID]
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

// Unsubscribe releases one client. The worker is told to stop watching only when the last
// watcher of that conversation leaves, and never for the workspace's own fallback, which is what a
// tab with no landing of its own will open next. A person's landing is not pinned: every caller
// would have to be, and a stream attaches what it shows anyway (ADR 0034 §7).
func (c *Control) Unsubscribe(id int) {
	c.mu.Lock()
	conversationID := c.subscriberConversation[id]
	delete(c.subscribers, id)
	delete(c.staleSignals, id)
	delete(c.viewSignals, id)
	delete(c.subscriberConversation, id)
	if conversationID != "" {
		c.attachments[conversationID] -= 1
		if c.attachments[conversationID] < 0 {
			c.attachments[conversationID] = 0
		}
	}
	last := conversationID != "" && c.attachments[conversationID] == 0 && conversationID != c.active
	c.mu.Unlock()

	if last {
		if _, err := c.worker.Call(context.Background(), protocol.Command{
			Type:           "detach",
			ConversationID: conversationID,
		}); err != nil {
			workerpool.Log("detach %s: %v", conversationID, err)
		}
	}
}

func (c *Control) onFrame(frame protocol.Frame) {
	switch frame.Type {
	case "secret_request":
		c.handleSecretRequest(frame)
		return
	case "secret_list":
		go c.answerSecretList(frame)
		return
	case "sandbox_wake":
		// Off the read loop: the answer is a command, and a Call made here would wait for a
		// response this goroutine is the one that reads.
		go c.answerSandboxWake(frame)
		return
	}
	c.publish(frame)
	// Titles are derived from the first user message, so a settled run is the
	// moment a conversation's name can change. The settlement also clears the prompt
	// stamp: the run is over because the worker said so, and the busy sign should come
	// down with the answer rather than lingering for the rest of the ten seconds.
	if frame.Type == "submission" {
		c.mu.Lock()
		delete(c.promptedAt, frame.ConversationID)
		c.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if _, err := c.listConversations(ctx); err != nil {
				workerpool.Log("refresh conversation list: %v", err)
				return
			}
			c.applyPendingSecretRestart(ctx)
			c.publish(protocol.Frame{Type: "state"})
		}()
	}
}

// subscriberQueue is how many frames may wait for one client. It is not 1: a login is a
// burst — a status line, an address, and the prompt that asks for an answer arrive within a
// millisecond — and a queue of one loses the middle of it, which is the prompt. A view
// cannot queue at all: it is a signal, so the newest is always one cache read away.
const subscriberQueue = 32

// publish fans one frame out to attached clients. A view frame is a complete value, so it does
// not travel through a queue: the cache holds the newest, and a coalescing signal tells each
// watcher to read it. An event frame — a submission, a login step — is one of a sequence and
// cannot be replaced: a dropped prompt leaves a dialog waiting for a question the provider
// already asked.
func (c *Control) publish(frame protocol.Frame) {
	// A state frame is a signal and takes the other road: the frame channel would let a slow
	// write drop it, and a client that never hears the state moved keeps the state it read
	// before — the busy sign outliving the run is exactly that.
	if frame.Type == "state" {
		c.notifyState()
		return
	}
	c.mu.Lock()
	if frame.Type == "view" {
		c.views[frame.ConversationID] = cachedView{
			value:      append([]byte(nil), frame.Value...),
			annotation: append(json.RawMessage(nil), frame.Acornd...),
			seq:        frame.Seq,
		}
		c.wakeViewsLocked(frame.ConversationID)
		c.mu.Unlock()
		return
	}
	subscribers := make([]chan protocol.Frame, 0, len(c.subscribers))
	for id, channel := range c.subscribers {
		// A submission or a secret prompt is one conversation's: a client watching another
		// conversation must never see it. Frames with no conversation are for everyone: a
		// login prompt belongs to no transcript.
		if (frame.Type == "submission" || frame.Type == "secret") && frame.ConversationID != "" {
			if c.subscriberConversation[id] != frame.ConversationID {
				continue
			}
		}
		subscribers = append(subscribers, channel)
	}
	c.mu.Unlock()

	for _, channel := range subscribers {
		queueFrame(channel, frame)
	}
}

// wakeViewsLocked raises every watcher's view signal for one conversation. A signal already
// waiting says the same thing, so a second view of the same run is not a second wakeup: the
// client reads the newest cache when it gets there, and one read carries everything skipped.
// The caller holds the lock.
func (c *Control) wakeViewsLocked(conversationID string) {
	for id, signal := range c.viewSignals {
		if conversationID != "" && c.subscriberConversation[id] != conversationID {
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

func (c *Control) applyDescribe(describe protocol.Describe) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.describe = describe
	c.providers = describe.Providers
}
