package control

import (
	"context"
	"errors"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/protocol"
)

// The weakest level is the one nearest the bottom of the scale, not the one the provider listed
// first: a provider is free to list "high, low, medium", and picking its first entry would run
// every default at the most expensive setting.
func TestTheWeakestLevelWinsWhateverOrderTheProviderListed(t *testing.T) {
	cases := []struct {
		levels []string
		want   string
	}{
		{[]string{"high", "low", "medium"}, "low"},
		{[]string{"medium", "high"}, "medium"},
		{[]string{"high"}, "high"},
		{nil, ""},
		// A level the scale does not know sorts as the strongest, so any known level beats it —
		// and one is still returned when nothing else was declared, because it is a level the
		// model said it has.
		{[]string{"astronomical", "high"}, "high"},
		{[]string{"astronomical"}, "astronomical"},
	}
	for _, one := range cases {
		if got := lowestThinking(one.levels); got != one.want {
			t.Errorf("lowestThinking(%v) = %q, want %q", one.levels, got, one.want)
		}
	}
}

// A subagent is configured by whatever created it. Re-applying the install default would
// overwrite the subagent's own model with a choice it never made — and the subagent is not in the
// interface, so nobody would see it happen.
func TestASubagentIsNeverReconfigured(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["describe"] = protocol.Describe{Provider: "faux", Providers: []protocol.ProviderInfo{{
		ID: "faux", Authenticated: true, Models: []protocol.ModelInfo{{ID: "faux-1"}},
	}}}
	control := controlWithWorker(t, worker)
	control.conversations = []protocol.ConversationInfo{{ID: "child", Parent: "parent"}}

	if err := control.ensureConversation(context.Background(), "child"); err != nil {
		t.Fatal(err)
	}
	for _, command := range worker.asked() {
		if command.Type == "open" {
			t.Fatalf("a subagent should not be opened with the install default: %+v", command)
		}
	}
}

// A conversation whose model was already applied is left alone — reopening it on every refresh
// would rewrite the agent under a run that is in progress — unless the provider it is bound to
// has gone away, and then it must be reconfigured or it cannot run at all.
func TestAnAppliedConversationIsReopenedOnlyWhenItsModelIsGone(t *testing.T) {
	available := protocol.Describe{Providers: []protocol.ProviderInfo{{
		ID: "faux", Authenticated: true, Models: []protocol.ModelInfo{{ID: "faux-1"}},
	}}}
	for _, alive := range []bool{true, false} {
		worker := newFakeWorker()
		catalog := available
		if !alive {
			catalog = protocol.Describe{Providers: []protocol.ProviderInfo{{ID: "faux", Authenticated: false}}}
		}
		worker.answer["describe"] = catalog
		control := controlWithWorker(t, worker)
		control.applyDescribe(catalog)
		control.resolved["1"] = &protocol.ResolvedAgent{Model: "faux/faux-1"}
		control.bindings["1"] = protocol.ModelBinding{Provider: "faux", ModelID: "faux-1"}

		if err := control.ensureConversation(context.Background(), "1"); err != nil {
			t.Fatal(err)
		}
		opened := false
		for _, command := range worker.asked() {
			if command.Type == "open" {
				opened = true
			}
		}
		if opened == alive {
			t.Errorf("provider available=%v: reopened=%v", alive, opened)
		}
	}
}

// A list that cannot be read is an error, and the list the interface is showing is left as it
// was: replacing a good list with an empty one because a read failed would make every
// conversation in the sidebar disappear.
func TestAFailedListReadKeepsWhatIsOnScreen(t *testing.T) {
	worker := newFakeWorker()
	worker.failure = errors.New("the harness is not answering")
	control := controlWithWorker(t, worker)
	control.conversations = []protocol.ConversationInfo{{ID: "kept"}}
	if _, err := control.listConversations(context.Background()); err == nil {
		t.Fatal("a failed read should be an error")
	}
	if len(control.conversations) != 1 || control.conversations[0].ID != "kept" {
		t.Errorf("the visible list should survive a failed read: %+v", control.conversations)
	}
}

// "Running" is three rules, and each one alone is a lie the sidebar used to tell: a prompt in the
// last ten seconds, a tool card in the last five minutes, or a run the worker says is still going
// and that spoke in the last half hour.
func TestARunShowsAsRunningOnAnyOfThreeClocks(t *testing.T) {
	const now = 1_000_000_000
	cases := []struct {
		why      string
		info     protocol.ConversationInfo
		prompted int64
		action   int64
		want     bool
	}{
		{"a prompt just sent", protocol.ConversationInfo{ID: "1"}, now, 0, true},
		{"a tool card just written", protocol.ConversationInfo{ID: "1"}, 0, now, true},
		{"a run the worker says is live", protocol.ConversationInfo{ID: "1", Running: true, UpdatedAt: now}, 0, 0, true},
		{"a run that spoke an hour ago", protocol.ConversationInfo{ID: "1", Running: true, UpdatedAt: now - 31*60_000}, 0, 0, false},
		{"an idle conversation", protocol.ConversationInfo{ID: "1", UpdatedAt: now}, 0, 0, false},
		{"a run with no timestamp", protocol.ConversationInfo{ID: "1", Running: true}, 0, 0, false},
	}
	for _, one := range cases {
		control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}, DataDir: t.TempDir()})
		if one.prompted != 0 {
			control.promptedAt["1"] = one.prompted
		}
		if one.action != 0 {
			control.actionAt["1"] = one.action
		}
		if got := control.runActive(one.info, now); got != one.want {
			t.Errorf("%s: runActive = %v, want %v", one.why, got, one.want)
		}
	}
}

// The conversation being watched is watched even when it has no attachment and no prompt stamp:
// a picture opened in it still has to stream, and the room the user is in is the one that has to.
func TestTheConversationOnScreenIsAlwaysWatched(t *testing.T) {
	control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}, DataDir: t.TempDir()})
	control.active = "1"
	control.attachments["2"] = 0 // written to, then settled: not watched
	control.attachments["3"] = 1
	watched := control.watchedConversations()
	if len(watched) != 2 {
		t.Fatalf("watched = %v, want the attachment and the active room", watched)
	}
	found := map[string]bool{}
	for _, id := range watched {
		found[id] = true
	}
	if !found["1"] || !found["3"] || found["2"] {
		t.Errorf("watched = %v", watched)
	}
	seen := map[string]bool{}
	for _, id := range control.watchedConversations() {
		if seen[id] {
			t.Errorf("the active room is listed twice: %v", control.watchedConversations())
		}
		seen[id] = true
	}
}

// Clearing a draft that was never there writes nothing, and clearing one that is already empty
// still stamps it: the stamp is what tells the other machines the words are no longer theirs to
// send, and a delete alone would leave them holding a draft nobody contradicts.
func TestClearingADraftStampsEvenWhenThereIsNothingToClear(t *testing.T) {
	control := New(Options{
		Workspace: Workspace{Name: "w", Path: t.TempDir()},
		DataDir:   t.TempDir(),
	})
	control.clearDraft("", "never-seen")
	if _, found := control.drafts[draftKey{conversation: "never-seen"}]; found {
		t.Error("a draft that was never written should not be created")
	}

	control.drafts[draftKey{conversation: "1"}] = Draft{Text: "half a thought", UpdatedAt: 1}
	control.clearDraft("", "1")
	if got := control.drafts[draftKey{conversation: "1"}]; got.Text != "" || got.Attachments != nil {
		t.Errorf("the words should be gone: %+v", got)
	}
	if got := control.drafts[draftKey{conversation: "1"}].UpdatedAt; got <= 1 {
		t.Errorf("the entry should be stamped, got %d", got)
	}
}
