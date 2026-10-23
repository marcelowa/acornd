// conversations: their operations, their views, and what is running in them
//
// Part of the control package. The file is named after the job, not the layer: this is the
// one thing it does, and everything it needs from its siblings is a call, not a comment.
package control

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
	"github.com/acornfall/acornfall/gateway/internal/workerpool"
)

// Conversation is one transcript, as the UI lists it.
type Conversation struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Root  bool   `json:"root"`
	// Hidden conversations are tombstoned, not deleted; the UI can restore them.
	Hidden bool `json:"hidden"`
	// Running, LastEntryID, UpdatedAt, Tokens, and Cost are live state for the rail.
	Running     bool   `json:"running"`
	LastEntryID string `json:"lastEntryId"`
	// UpdatedAt is last activity, Unix milliseconds, so the rail can group by date.
	// Zero when the conversation has no entries yet, which renders flat.
	UpdatedAt int64   `json:"updatedAt,omitempty"`
	Tokens    float64 `json:"tokens"`
	Cost      float64 `json:"cost"`
	// Parent marks a subagent conversation: created by another conversation.
	Parent   string   `json:"parent,omitempty"`
	Children []string `json:"children,omitempty"`
	Model    string   `json:"model,omitempty"`
	Thinking string   `json:"thinking,omitempty"`
	// CreatedBy marks a peer: a normal conversation an agent created.
	CreatedBy string `json:"createdBy,omitempty"`
}

// CompactPreview is what a manual compaction would fold.
type CompactPreview struct {
	ConversationID string `json:"conversationId"`
	Entries        int    `json:"entries"`
	Turns          int    `json:"turns"`
}

// Select attaches to one conversation, detaching the previous one so frames
// describe a single transcript.
func (c *Control) Select(ctx context.Context, conversationID string) error {
	if conversationID == "" {
		return errors.New("conversation id is required")
	}
	c.mu.RLock()
	previous := c.active
	c.mu.RUnlock()
	if previous == conversationID {
		return nil
	}
	if previous != "" {
		if _, err := c.worker.Call(ctx, protocol.Command{Type: "detach", ConversationID: previous}); err != nil {
			workerpool.Log("detach %s: %v", previous, err)
		}
	}
	if _, err := c.worker.Call(ctx, protocol.Command{Type: "attach", ConversationID: conversationID}); err != nil {
		return err
	}
	c.mu.Lock()
	c.active = conversationID
	c.mu.Unlock()
	c.persist()
	// Refresh now rather than waiting for the next tick: the response to this call
	// carries the policy, and a stale policy on a fresh conversation reads as a bug.
	if _, err := c.listConversations(ctx); err != nil {
		workerpool.Log("refresh conversations after select: %v", err)
	}
	// A conversation that has no model yet would fail on its next prompt, so selecting
	// one is where it gets configured — and where the pickers read what will actually
	// run. No provider is a state, not a failure: selecting lands, and sending is what
	// says a provider is needed.
	if err := c.ensureConversation(ctx, conversationID); err != nil && !errors.Is(err, ErrNoProvider) {
		return err
	}
	c.publish(protocol.Frame{Type: "state"})
	return nil
}

// Rename gives a conversation a name. The name is committed into the
// conversation itself, so it is durable and travels with the transcript.
func (c *Control) Rename(ctx context.Context, conversationID, title string) error {
	if strings.TrimSpace(title) == "" {
		return errors.New("title is required")
	}
	if _, err := c.worker.Call(ctx, protocol.Command{
		Type:           "rename",
		ConversationID: conversationID,
		Title:          title,
	}); err != nil {
		return err
	}
	if _, err := c.listConversations(ctx); err != nil {
		return err
	}
	c.publish(protocol.Frame{Type: "state"})
	return nil
}

// Hide tombstones a conversation. Nothing is removed: storage is append-only, so
// a hidden conversation keeps its transcript and can be restored.
func (c *Control) Hide(ctx context.Context, conversationID string, hidden bool) error {
	if _, err := c.worker.Call(ctx, protocol.Command{
		Type:           "hide",
		ConversationID: conversationID,
		Hidden:         hidden,
	}); err != nil {
		return err
	}
	conversations, err := c.listConversations(ctx)
	if err != nil {
		return err
	}
	// Hiding what you are looking at has to move you somewhere.
	if hidden && c.activeConversation() == conversationID {
		if next := firstVisible(conversations); next != "" {
			if err := c.Select(ctx, next); err != nil {
				return err
			}
		} else if err := c.Create(ctx); err != nil {
			return err
		}
	}
	c.publish(protocol.Frame{Type: "state"})
	return nil
}

// Fork branches a conversation at an entry. The fork inherits the transcript up
// to that entry and the agent in effect at that point.
func (c *Control) Fork(ctx context.Context, conversationID, at string) error {
	var response struct {
		ConversationID string `json:"conversationId"`
	}
	if err := c.worker.CallInto(ctx, protocol.Command{
		Type:           "fork",
		ConversationID: conversationID,
		At:             at,
	}, &response); err != nil {
		return err
	}
	if response.ConversationID == "" {
		return errors.New("worker returned no conversation id")
	}

	c.mu.RLock()
	parent := c.bindings[conversationID]
	parentResolved := c.resolved[conversationID]
	c.mu.RUnlock()
	if parent.Provider != "" || parent.Definition != "" {
		c.mu.Lock()
		c.bindings[response.ConversationID] = parent
		c.resolved[response.ConversationID] = parentResolved
		c.mu.Unlock()
	}

	if _, err := c.listConversations(ctx); err != nil {
		return err
	}
	if err := c.Select(ctx, response.ConversationID); err != nil {
		return err
	}
	if parent.Provider == "" {
		if err := c.ensureConversation(ctx, response.ConversationID); err != nil && !errors.Is(err, ErrNoProvider) {
			return err
		}
	}
	c.publish(protocol.Frame{Type: "state"})
	return nil
}

// Compact starts a manual compaction of a conversation. The summary replaces
// the old prefix while the conversation keeps working, and the transcript
// keeps a notice where the cut was.
func (c *Control) Compact(ctx context.Context, conversationID string) error {
	var response struct {
		ConversationID string `json:"conversationId"`
	}
	if err := c.worker.CallInto(ctx, protocol.Command{
		Type:           "compact",
		ConversationID: conversationID,
	}, &response); err != nil {
		return err
	}
	if response.ConversationID == "" {
		return errors.New("worker returned no conversation id")
	}
	c.publish(protocol.Frame{Type: "state"})
	return nil
}

// CompactPreview says what a manual compaction would fold: entry and turn counts,
// so the dialog states the scope. The cut itself stays the harness's call.
func (c *Control) CompactPreview(ctx context.Context, conversationID string) (CompactPreview, error) {
	var response CompactPreview
	if err := c.worker.CallInto(ctx, protocol.Command{
		Type:           "compact_preview",
		ConversationID: conversationID,
	}, &response); err != nil {
		return CompactPreview{}, err
	}
	return response, nil
}

// Reset starts a fresh context: the next turn sees nothing before the cut, and the
// transcript keeps a notice where it was.
// Create makes a new conversation and selects it.
func (c *Control) Create(ctx context.Context) error {
	var response struct {
		ConversationID string `json:"conversationId"`
	}
	if err := c.worker.CallInto(ctx, protocol.Command{Type: "create"}, &response); err != nil {
		return err
	}
	if response.ConversationID == "" {
		return errors.New("worker returned no conversation id")
	}
	if _, err := c.listConversations(ctx); err != nil {
		return err
	}
	if err := c.Select(ctx, response.ConversationID); err != nil {
		return err
	}
	// A conversation with no model cannot run: configure it now rather than
	// failing on its first prompt. It still counts as "no explicit model".
	if err := c.ensureConversation(ctx, response.ConversationID); err != nil && !errors.Is(err, ErrNoProvider) {
		return err
	}
	// Then it stops inheriting: what resolved when it was created is what it runs, and a
	// default changed tomorrow must not move it (ADR 0017).
	c.pinOne(response.ConversationID)
	return nil
}

// ensureConversation configures a conversation that has no explicit model, so it
// can run, without making the default an explicit choice it never made.
func (c *Control) ensureConversation(ctx context.Context, conversationID string) error {
	c.mu.RLock()
	explicit := c.bindings[conversationID]
	// A binding is not the same as an applied binding: pinning writes one without talking to
	// the worker, and a conversation whose binding was never opened has no model to run with.
	applied := c.resolved[conversationID] != nil
	for _, info := range c.conversations {
		if info.ID == conversationID && info.Parent != "" {
			// A subagent is configured by whatever created it. Re-applying a binding
			// here would overwrite that with the install default.
			c.mu.RUnlock()
			return nil
		}
	}
	c.mu.RUnlock()
	if applied && explicit.Provider != "" && c.isAvailable(c.Describe(), explicit) {
		return nil
	}
	return c.reapply(ctx, conversationID)
}

// isSubagent reports whether a conversation was created by another one.
func (c *Control) isSubagent(conversationID string) bool {
	info, found := c.conversationInfo(conversationID)
	return found && info.Parent != ""
}

// conversationInfo is one conversation from the cached list, read under one lock. Callers that
// need it twice, or need two fields of it, take it once.
func (c *Control) conversationInfo(conversationID string) (protocol.ConversationInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, info := range c.conversations {
		if info.ID == conversationID {
			return info, true
		}
	}
	return protocol.ConversationInfo{}, false
}

// RefreshConversations re-reads the list on demand, for callers that are not holding
// an event stream open.
func (c *Control) RefreshConversations(ctx context.Context) error {
	_, err := c.listConversations(ctx)
	return err
}

func (c *Control) listConversations(ctx context.Context) ([]protocol.ConversationInfo, error) {
	worker, err := c.currentWorker()
	if err != nil {
		return nil, err
	}
	var data struct {
		Conversations []protocol.ConversationInfo `json:"conversations"`
	}
	if err := worker.CallInto(ctx, protocol.Command{Type: "conversations"}, &data); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.conversations = data.Conversations
	c.mu.Unlock()

	// Conversations that never chose are pinned to what resolves now. Idempotent, and it runs
	// on every listing because a listing is the only place that sees every conversation.
	c.pinMissing(data.Conversations)

	// Policy for every conversation a client is watching, so an inspector is correct in
	// whichever tab it happens to be open.
	for _, conversationID := range c.watchedConversations() {
		policy, err := worker.Call(ctx, protocol.Command{Type: "policy", ConversationID: conversationID})
		if err != nil {
			continue
		}
		c.mu.Lock()
		c.policies[conversationID] = policy
		c.mu.Unlock()
	}
	return data.Conversations, nil
}

func (c *Control) activeConversation() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.active
}

// runActive is the one busy rule the interface reads. The worker's run record is the
// signal, but it lags behind the click and outlives dead runs, so both directions are
// gated: a prompt accepted in the last ten seconds counts before the record exists, and a
// record only counts while the activity is fresh. A settlement clears its prompt stamp,
// so the grace ends when the run does: the sign comes down with the answer rather than
// outliving it by seconds. Thirty minutes of silence with no new prompt is a residue,
// not a run — a legitimately quiet hour-long tool call is the known cost, and its tool
// cards still say it is working.
func (c *Control) runActive(info protocol.ConversationInfo, now int64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.runActiveLocked(info, now)
}

// runActiveLocked is the same rule for a caller that already holds the lock. A state build
// renders every conversation while holding its snapshot read lock, and taking the read lock
// again there is not a nested read: Go's RWMutex puts a read lock that arrives after a queued
// writer behind that writer, so the second acquisition waits for a writer that is waiting for
// the first — held by this same goroutine — and every request behind it waits too. It was
// observed in production: one state request and one event-stream subscribe wedged the control
// lock; the eviction sweep then took the registry lock and waited on that control, and the
// whole interface sat down until the process was killed.
func (c *Control) runActiveLocked(info protocol.ConversationInfo, now int64) bool {
	prompted := c.promptedAt[info.ID]
	action := c.actionAt[info.ID]
	if now-prompted < 10_000 {
		return true
	}
	if now-action < 5*60_000 {
		return true
	}
	if !info.Running || info.UpdatedAt <= 0 {
		return false
	}
	return now-info.UpdatedAt < 30*60*1000
}

// updateSummary reads the install's update tier without importing it: the
// entry point wires the function, and a nil one answers the zero summary.
func (c *Control) updateSummary() UpdateSummary {
	if c.opts.Update == nil {
		return UpdateSummary{}
	}
	return c.opts.Update()
}

// AnyRunning is the update gate: true while any conversation runs. It mirrors
// runActive without calling it, because runActive takes the lock itself and
// this scan already holds a copy made under it.
func (c *Control) AnyRunning() bool {
	return len(c.RunningIDs()) > 0
}

// RunningIDs names the conversations running now, for the update tier's
// drain-stop: the dialog promises runs stop, so they must be enumerable.
func (c *Control) RunningIDs() []string {
	now := time.Now().UnixMilli()
	c.mu.RLock()
	infos := make([]protocol.ConversationInfo, len(c.conversations))
	copy(infos, c.conversations)
	prompted := maps.Clone(c.promptedAt)
	c.mu.RUnlock()
	var ids []string
	for _, info := range infos {
		if now-prompted[info.ID] < 10_000 {
			ids = append(ids, info.ID)
			continue
		}
		if info.Running && info.UpdatedAt > 0 && now-info.UpdatedAt < 30*60*1000 {
			ids = append(ids, info.ID)
		}
	}
	return ids
}

// toConversations renders the rows the interface reads. It must be called with c.mu held for
// reading: the busy rule reads the prompt stamps, and asking for the lock here would be the
// second acquisition that deadlocks a state build behind a queued writer.
func (c *Control) toConversations(infos []protocol.ConversationInfo) []Conversation {
	now := time.Now().UnixMilli()
	conversations := make([]Conversation, 0, len(infos))
	for _, info := range infos {
		conversations = append(conversations, Conversation{
			ID:          info.ID,
			Title:       info.Title,
			Root:        info.Root,
			Hidden:      info.Hidden,
			Running:     c.runActiveLocked(info, now),
			LastEntryID: info.LastEntryID,
			UpdatedAt:   info.UpdatedAt,
			Tokens:      info.Tokens,
			Cost:        info.Cost,
			Parent:      info.Parent,
			Children:    info.Children,
			Model:       info.Model,
			Thinking:    info.Thinking,
			CreatedBy:   info.CreatedBy,
		})
	}
	return conversations
}

// hasConversation reports whether a conversation is a sensible place to land:
// present, not tombstoned, and not a subagent's transcript.
func hasConversation(infos []protocol.ConversationInfo, id string) bool {
	if id == "" {
		return false
	}
	for _, info := range infos {
		if info.ID == id {
			return !info.Hidden && info.Parent == ""
		}
	}
	return false
}

func firstVisible(infos []protocol.ConversationInfo) string {
	for _, info := range infos {
		if !info.Hidden && info.Parent == "" {
			return info.ID
		}
	}
	return ""
}

// defaultConversation prefers the root conversation, so an upgrade lands on the
// history it already has.
func defaultConversation(infos []protocol.ConversationInfo) string {
	for _, info := range infos {
		if info.Root && !info.Hidden {
			return info.ID
		}
	}
	return firstVisible(infos)
}
