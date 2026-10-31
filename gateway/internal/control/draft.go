package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// maxDraftBytes is how many bytes of words one draft may hold. A message is bounded by the
// prompt endpoint; a draft is broadcast with every state event, to every attached client, so
// a draft without a ceiling would make every state event as heavy as the paste that caused
// it. Beyond this the save is refused: the words stay in the composer, which is where they
// were anyway.
const maxDraftBytes = 64 << 10

// Draft is one person's unsent message in one conversation: the words, the files that already
// staged, and when it last moved. It is the console's draft, not a browser's: it lives beside the
// workspace, so the same half-thought waits in the composer on every machine — and it is the
// person's, so two people typing in one conversation stop erasing each other (ADR 0034 §7).
//
// A cleared draft stays as an entry with no words and a fresh stamp. The stamp is the point:
// a client that still holds the words that were just sent is told they are gone, because the
// empty draft is newer than the one that client wrote.
type Draft struct {
	Text        string            `json:"text,omitempty"`
	Attachments []DraftAttachment `json:"attachments,omitempty"`
	// UpdatedAt is when the draft last moved, Unix milliseconds, taken by this process. It
	// orders this machine's writes against another's; it is not a wall clock to show.
	UpdatedAt int64 `json:"updatedAt,omitempty"`
}

// DraftAttachment is one staged file riding a draft: the id the message will carry, and what
// the chip over the composer says while it waits. The bytes are the worker's; this is only
// the name for them.
type DraftAttachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	MIME string `json:"mime"`
}

// draftKey names one person's draft in one conversation. The address, not the whole Caller: an
// install with no admin claimed has one person in it and one key that is empty, which is also what
// every draft written before drafts were per person belonged to.
type draftKey struct {
	conversation string
	caller       string
}

// persistedDrafts is the file's shape: drafts[conversation][address]. It is nested rather than one
// flat key per pair so the file says which conversation and which person in a shape a reader can
// see, and so a key never has to contain a separator that an address might contain too.
//
// A file written before drafts were per person holds one draft per conversation and does not decode
// as this one. Those drafts are dropped rather than guessed at: a half-typed message attributed to
// the wrong person is worse than a composer that starts empty.
type persistedDrafts struct {
	Drafts map[string]map[string]Draft `json:"drafts,omitempty"`
}

// loadDrafts reads this workspace's drafts. A missing or unreadable file is no drafts, not
// an error: a workspace that never had one is the normal state, and a corrupt one must not
// stop the workspace from opening.
func (c *Control) loadDrafts() {
	if c.opts.DraftsPath == "" {
		return
	}
	payload, err := os.ReadFile(c.opts.DraftsPath)
	if err != nil {
		return
	}
	var stored persistedDrafts
	if err := json.Unmarshal(payload, &stored); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for conversation, byCaller := range stored.Drafts {
		if conversation == "" {
			continue
		}
		for caller, draft := range byCaller {
			c.drafts[draftKey{conversation: conversation, caller: caller}] = draft
		}
	}
}

// SetDraft records one person's unsent message in one conversation and wakes every attached
// client, so the words show on the machine they were typed on and on the one that has been open
// all along. The newest write wins: one person's two machines typing is a race the server cannot
// untangle, and the last save is the one they made most recently.
func (c *Control) SetDraft(caller Caller, conversationID, text string, attachments []DraftAttachment) (Draft, error) {
	if conversationID == "" {
		return Draft{}, errors.New("conversation id is required")
	}
	if len(text) > maxDraftBytes {
		return Draft{}, fmt.Errorf("a draft carries %d KB at most", maxDraftBytes>>10)
	}
	kept := make([]DraftAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment.ID == "" {
			continue
		}
		kept = append(kept, attachment)
	}
	draft := Draft{Text: text, Attachments: kept, UpdatedAt: time.Now().UnixMilli()}

	c.mu.Lock()
	c.drafts[draftKey{conversation: conversationID, caller: caller.Email}] = draft
	c.mu.Unlock()

	c.persistDrafts()
	// The state is how a client hears about a draft it did not write: the signal is the same
	// one a settled run uses, and the payload is built fresh per client.
	c.publish(protocol.Frame{Type: "state"})
	return draft, nil
}

// clearDraft empties the sender's draft once their message has been accepted, and only theirs:
// sending used to clear the room's one draft, so two people typing in one conversation erased each
// other (ADR 0034 §7). The entry stays with a fresh stamp, which is what tells the sender's other
// machines the words are no longer theirs to send — a delete alone would leave them with a draft
// that no state event contradicts.
func (c *Control) clearDraft(owner, conversationID string) {
	key := draftKey{conversation: conversationID, caller: owner}
	c.mu.RLock()
	current, found := c.drafts[key]
	c.mu.RUnlock()
	if !found || (current.Text == "" && len(current.Attachments) == 0) {
		return
	}
	c.mu.Lock()
	c.drafts[key] = Draft{UpdatedAt: time.Now().UnixMilli()}
	c.mu.Unlock()
	c.persistDrafts()
}

// persistDrafts writes every draft this workspace holds. One file per workspace, because a
// conversation id means something only inside one; the whole file is rewritten, because a
// draft is a handful of bytes and a partial update would be more code than it saves.
func (c *Control) persistDrafts() {
	if c.opts.DraftsPath == "" {
		return
	}
	// Nested on the way out, so the file is drafts[conversation][address] and nothing in it depends
	// on an address never containing a character.
	c.mu.RLock()
	drafts := make(map[string]map[string]Draft, len(c.drafts))
	for key, draft := range c.drafts {
		byCaller, known := drafts[key.conversation]
		if !known {
			byCaller = map[string]Draft{}
			drafts[key.conversation] = byCaller
		}
		byCaller[key.caller] = draft
	}
	c.mu.RUnlock()

	payload, err := json.MarshalIndent(persistedDrafts{Drafts: drafts}, "", "  ")
	if err != nil {
		return
	}
	temp := c.opts.DraftsPath + ".tmp"
	if err := os.WriteFile(temp, append(payload, '\n'), 0o600); err != nil {
		return
	}
	_ = os.Rename(temp, c.opts.DraftsPath)
}
