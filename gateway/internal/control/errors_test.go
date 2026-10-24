package control

import (
	"context"
	"errors"
	"strings"
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

// A select that lands but cannot configure the conversation is reported: the conversation would
// run under no model at all on its next prompt.
func TestASelectWhoseConversationCannotBeConfiguredIsReported(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["conversations"] = map[string]any{"conversations": []any{}}
	worker.answer["describe"] = protocol.Describe{Provider: "faux", Providers: []protocol.ProviderInfo{{
		ID: "faux", Authenticated: true, Models: []protocol.ModelInfo{{ID: "faux-1"}},
	}}}
	worker.failOn["open"] = errors.New("the agent could not be opened")
	control := controlWithWorker(t, worker)
	// The conversation has a model, so the failure is the open rather than "no provider" — which
	// a select is allowed to tolerate.
	control.bindings["1"] = protocol.ModelBinding{Provider: "faux", ModelID: "faux-1"}
	if err := control.Select(context.Background(), "1"); err == nil {
		t.Fatal("a conversation that cannot be configured should be reported")
	}
}

// Hiding the last conversation starts a new one, and a new one that cannot be made is reported:
// hiding what is on screen has to land somewhere.
func TestAHideThatCannotStartAConversationIsReported(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["conversations"] = map[string]any{"conversations": []any{}}
	worker.failOn["create"] = errors.New("the harness refused")
	control := controlWithWorker(t, worker)
	control.active = "1"
	control.conversations = []protocol.ConversationInfo{{ID: "1"}}

	if err := control.Hide(context.Background(), "1", true); err == nil {
		t.Fatal("a hide with nowhere to land should be reported")
	}
}

// A fork that works is a new conversation with the parent's model: the branch runs what the
// conversation it came from runs, or it is a different agent than the one being continued.
func TestAForkKeepsTheParentsModel(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["fork"] = map[string]any{"conversationId": "child"}
	worker.answer["describe"] = protocol.Describe{Provider: "faux", Providers: []protocol.ProviderInfo{{
		ID: "faux", Authenticated: true, Models: []protocol.ModelInfo{{ID: "faux-1"}},
	}}}
	control := controlWithWorker(t, worker)
	control.bindings["parent"] = protocol.ModelBinding{Provider: "faux", ModelID: "faux-1"}

	if err := control.Fork(context.Background(), "parent", "entry"); err != nil {
		t.Fatal(err)
	}
	if got := control.bindings["child"]; got.Provider != "faux" || got.ModelID != "faux-1" {
		t.Errorf("the branch should keep the model it is continuing: %+v", got)
	}
}

// A create whose select fails is reported, and a create whose conversation cannot be configured is
// reported: an id the harness made but the client was never moved to is a conversation nobody sees.
func TestACreateThatCannotFinishIsReported(t *testing.T) {
	for _, one := range []struct {
		why    string
		failOn string
	}{
		{"the select fails", "attach"},
		{"the conversation cannot be configured", "open"},
	} {
		worker := newFakeWorker()
		worker.answer["create"] = map[string]any{"conversationId": "9"}
		worker.answer["conversations"] = map[string]any{"conversations": []any{}}
		worker.answer["describe"] = protocol.Describe{Provider: "faux", Providers: []protocol.ProviderInfo{{
			ID: "faux", Authenticated: true, Models: []protocol.ModelInfo{{ID: "faux-1"}},
		}}}
		worker.failOn[one.failOn] = errors.New("the harness refused")
		control := controlWithWorker(t, worker)
		control.bindings["9"] = protocol.ModelBinding{Provider: "faux", ModelID: "faux-1"}
		if err := control.Create(context.Background()); err == nil {
			t.Errorf("%s: it should be reported", one.why)
		}
	}
}

// Stopping everything answers with what failed: a shutdown that reports nothing while a run is
// still going is a process that exits with a conversation mid-answer and nobody knows why.
func TestStoppingEverythingReportsWhatFailed(t *testing.T) {
	worker := newFakeWorker()
	worker.failOn["abort"] = errors.New("the harness refused")
	control := controlWithWorker(t, worker)
	control.conversations = []protocol.ConversationInfo{{ID: "1"}}
	control.promptedAt["1"] = time.Now().UnixMilli()

	workspaces := NewWorkspaces(WorkspacesOptions{WorkspacesDir: t.TempDir()})
	workspaces.controls["one"] = control
	if err := workspaces.AbortAll(context.Background()); err == nil {
		t.Fatal("a run that could not be stopped should be reported")
	}
}

// A worker that cannot be started is reported rather than leaving a workspace that looks up and
// answers nothing: the command is the caller's, so a wrong one is a configuration mistake.
func TestAWorkerThatCannotStartIsReported(t *testing.T) {
	control := New(Options{
		Workspace:     Workspace{Name: "w", Path: t.TempDir()},
		DataDir:       t.TempDir(),
		StoragePath:   t.TempDir() + "/storage",
		WorkerCommand: []string{"/nonexistent-worker-binary"},
	})
	if err := control.startWorker(context.Background()); err == nil {
		t.Fatal("a worker that cannot start should be reported")
	}
	if control.WorkerUp() {
		t.Error("a worker that did not start is not up")
	}
}

// A machine with no sandbox tier says so instead of pretending: this is the state a build without
// an engine runs in, and every surface reads it rather than guessing.
func TestWithoutASandboxTierTheCapabilitiesAreTheRefusal(t *testing.T) {
	control := New(Options{Workspace: Workspace{Name: "w", Path: t.TempDir()}, DataDir: t.TempDir(), StoragePath: t.TempDir()})
	if manager := control.sandboxManager(); manager != nil {
		t.Fatalf("no tier means no manager, got %v", manager)
	}
	if delivery := control.SecretDelivery(); delivery != sandbox.DeliverySubstitute {
		t.Errorf("with no engine a secret cannot be put in a guest, got %v", delivery)
	}
	if env := sandboxEnv(control.sandboxManager()); env != nil {
		t.Errorf("a worker with no engine gets no engine environment, got %v", env)
	}
	state := control.provisionSandbox(context.Background())
	if state.Problem == "" {
		t.Error("a workspace with no engine should report the refusal")
	}
}

// What a worker is started with is decided here, and every branch of it is a promise: an engine
// that puts secrets in the worker's own environment gets them last, so the shell cannot shadow
// one, and a substitute engine gets a placeholder instead of a value. Neither may leak a value
// into a guest that would substitute it, or into argv where ps can read it.
func TestTheWorkersEnvironmentAndArgsSayWhatTheEngineCanDo(t *testing.T) {
	delivered := []sandbox.Secret{
		{Name: "TOKEN", Description: "d", Value: "s3cret", Hosts: []string{"api.example.com"}},
	}

	// No engine: no environment, and the meta-data still travels so the prompt can offer it.
	if env := workerEnv(nil, delivered); len(env) != 0 {
		t.Errorf("with no engine there is nowhere to put a value, got %v", env)
	}
	args := secretArgs(sandbox.DeliverySubstitute, delivered)
	if len(args) != 2 || args[0] != "--secrets" {
		t.Fatalf("args = %v", args)
	}
	if strings.Contains(args[1], "s3cret") {
		t.Errorf("a substitute engine must not receive the value: %s", args[1])
	}
	if !strings.Contains(args[1], "$MSB_TOKEN") {
		t.Errorf("a substitute engine gets the placeholder it recognises: %s", args[1])
	}

	// An environment engine hands the value over in the process environment, and says so in the
	// metadata so the interface can warn that the guest can read it.
	args = secretArgs(sandbox.DeliveryEnvironment, delivered)
	if !strings.Contains(args[1], `"delivery":"environment"`) {
		t.Errorf("the metadata should name the delivery: %s", args[1])
	}
	if strings.Contains(args[1], "$MSB_") {
		t.Errorf("an environment engine has no placeholder: %s", args[1])
	}

	// Nothing to deliver is no argument at all, rather than an empty list the worker has to
	// interpret.
	if args := secretArgs(sandbox.DeliverySubstitute, nil); args != nil {
		t.Errorf("no secrets, no arguments: %v", args)
	}
}

// A sandbox that cannot be produced is a state the workspace reports, not a crash and not a
// workspace that pretends to be confined: the tools refuse, and the screen says which engine
// failed and why.
func TestASandboxThatCannotBeProducedIsReportedAsAState(t *testing.T) {
	tier := sandbox.NewTier(t.TempDir(), "", nil)
	if _, err := tier.For("bwrap"); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir() + "/storage"
	control := New(Options{
		Workspace:   Workspace{Name: "w", Path: t.TempDir(), Engine: "bwrap", Image: "an-image-that-is-not-there"},
		DataDir:     t.TempDir(),
		StoragePath: storage,
		Sandboxes:   tier,
	})
	state := control.provisionSandbox(context.Background())
	if state.Problem == "" {
		t.Fatalf("a sandbox that could not be produced should report why: %+v", state)
	}
	if state.Engine == "" {
		t.Error("the state should still name the engine that was asked for")
	}
}
