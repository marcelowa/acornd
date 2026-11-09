package control

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/protocol"
)

// The three things the mutation report found covered but unasserted. Each one is a rule the
// interface or the next boot depends on, and each was a mutation that lived: flip the comparison,
// and the whole suite still passed.

// Which parts of a binding a conversation chose itself. The interface shows a value as inherited
// or chosen from this, so a wrong answer tells a person they chose something they did not — or
// hides a choice they did make.
func TestTheStateSaysWhichPartsOfTheBindingWereChosen(t *testing.T) {
	cases := []struct {
		name     string
		binding  protocol.ModelBinding
		inherits Inherited
	}{
		{
			name:     "nothing chosen: everything is inherited",
			inherits: Inherited{Model: true, Thinking: true, Agent: true},
		},
		{
			name:     "a model chosen, nothing else",
			binding:  protocol.ModelBinding{Provider: "p", ModelID: "m"},
			inherits: Inherited{Model: false, Thinking: true, Agent: true},
		},
		{
			name:     "a thinking level chosen",
			binding:  protocol.ModelBinding{ThinkingLevel: "low"},
			inherits: Inherited{Model: true, Thinking: false, Agent: true},
		},
		{
			name:     "an agent chosen",
			binding:  protocol.ModelBinding{Definition: "personal"},
			inherits: Inherited{Model: true, Thinking: true, Agent: false},
		},
		{
			name:     "everything chosen",
			binding:  protocol.ModelBinding{Provider: "p", ModelID: "m", ThinkingLevel: "high", Definition: "personal"},
			inherits: Inherited{Model: false, Thinking: false, Agent: false},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}})
			control.bindings["1"] = test.binding
			got := control.StateFor(Caller{}, "1").Inherited
			if got != test.inherits {
				t.Errorf("inherited = %+v, want %+v (binding %+v)", got, test.inherits, test.binding)
			}
		})
	}
}

// Only what a conversation chose is written to disk. A blank binding in the file is a choice
// nobody made, and it would outrank every layer below it for good.
func TestABlankBindingIsNotWrittenDown(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/settings.json"
	control := New(Options{
		Workspace:    Workspace{Name: "w", Path: dir},
		SettingsPath: path,
	})
	control.bindings["chose"] = protocol.ModelBinding{Provider: "p", ModelID: "m"}
	control.bindings["blank"] = protocol.ModelBinding{}
	control.bindings["agent-only"] = protocol.ModelBinding{Definition: "personal"}
	control.persist()

	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("persist wrote nothing: %v", err)
	}
	var written persistedSettings
	if err := json.Unmarshal(payload, &written); err != nil {
		t.Fatalf("the file is not what was written: %v", err)
	}
	if _, ok := written.Agents["blank"]; ok {
		t.Errorf("a blank binding was written: %+v", written.Agents)
	}
	if _, ok := written.Agents["chose"]; !ok {
		t.Errorf("the chosen model was not written: %+v", written.Agents)
	}
	// A definition alone is a choice too: it is what a conversation was told to run as.
	if _, ok := written.Agents["agent-only"]; !ok {
		t.Errorf("an agent-only binding was dropped: %+v", written.Agents)
	}
}

// The busy rule the interface reads. It is deliberately generous in one direction — a prompt
// counts before the worker's record exists — so each grace is a number somebody chose, and each
// boundary is worth pinning: a mutant that moves one changes when a person can send a message.
// The record itself is not on a clock: a quiet run is still a run, and the interface dims its
// dot instead of losing the Stop button.
func TestTheBusyRuleKeepsItsGraceWindows(t *testing.T) {
	const promptWindow = 10_000
	const actionWindow = 5 * 60_000
	// Timestamps are real milliseconds: the rule subtracts from a clock, so a fixture anchored at
	// the epoch would be answering a different question.
	base := time.Now().UnixMilli()
	finished := protocol.ConversationInfo{ID: "1", Running: false, UpdatedAt: base}
	running := protocol.ConversationInfo{ID: "1", Running: true, UpdatedAt: base}

	cases := []struct {
		name     string
		prompted int64
		action   int64
		info     protocol.ConversationInfo
		now      int64
		want     bool
	}{
		{"a prompt just sent", base, 0, finished, base + promptWindow - 1, true},
		{"a prompt older than the window", base, 0, finished, base + promptWindow, false},
		{"an action just started", 0, base, finished, base + actionWindow - 1, true},
		{"an action older than its window", 0, base, finished, base + actionWindow, false},
		// Both windows are asked in order: a stale prompt does not hide a live action.
		{"a stale prompt and a live action", base, base, finished, base + promptWindow + 1, true},
		{"a run that is going", 0, 0, running, base, true},
		// The run record is the worker's committed word. Age does not end it; the console's dot
		// dims with age, but a quiet run keeps its Stop button and its tool cards open.
		{"a run nobody has heard from", 0, 0, running, base + 31*60_000, true},
		{"nothing running and no record", 0, 0, finished, base, false},
		// The worker dates most runs, but a resumed one writes its system prompt before the
		// first dated entry. A record with no timestamp is still a live run.
		{"a run with no timestamp", 0, 0, protocol.ConversationInfo{ID: "1", Running: true}, base, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}})
			control.promptedAt["1"] = test.prompted
			control.actionAt["1"] = test.action
			if got := control.runActiveLocked(test.info, test.now); got != test.want {
				t.Errorf("busy = %v, want %v", got, test.want)
			}
		})
	}
}

// A harness that is down has no run it can finish. The update gate asks this list before
// swapping the binary, and waiting on a dead worker's last record would wait for the process
// the update is replacing. An up harness reports the same record as the state does.
func TestADownHarnessHasNoRunsToStop(t *testing.T) {
	control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}})
	control.conversations = []protocol.ConversationInfo{{ID: "1", Running: true, UpdatedAt: time.Now().UnixMilli()}}
	control.workerUp = false
	if ids := control.RunningIDs(); len(ids) != 0 {
		t.Errorf("a down harness has nothing to stop, got %v", ids)
	}
	if control.AnyRunning() {
		t.Error("a down harness is not running anything, so an update is not blocked by it")
	}
	control.workerUp = true
	if ids := control.RunningIDs(); len(ids) != 1 || ids[0] != "1" {
		t.Errorf("an up harness reports its live run, got %v", ids)
	}
	if !control.AnyRunning() {
		t.Error("an up harness with a live run is running something")
	}
}
