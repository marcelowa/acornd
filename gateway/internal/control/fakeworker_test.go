package control

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// A harness process that is not a process. It answers the way a real worker does — a describe
// with a catalog, an open that resolves an agent — and it records what it was asked, which is how
// control's own side of the protocol is tested without spawning bun.
type fakeWorker struct {
	mu       sync.Mutex
	commands []protocol.Command
	// answer is what CallInto decodes into its out parameter, by command type.
	answer map[string]any
	// failure is returned by every call, when it is set.
	failure error
	started int
	closed  int
	frames  func(protocol.Frame)
	onExit  func(error)
	words   string
}

func newFakeWorker() *fakeWorker {
	return &fakeWorker{answer: map[string]any{}}
}

func (f *fakeWorker) Start(context.Context) error {
	f.mu.Lock()
	f.started++
	f.mu.Unlock()
	return nil
}

func (f *fakeWorker) Close() {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
}

func (f *fakeWorker) Call(_ context.Context, command protocol.Command) (json.RawMessage, error) {
	f.mu.Lock()
	f.commands = append(f.commands, command)
	failure := f.failure
	answer := f.answer[command.Type]
	f.mu.Unlock()
	if failure != nil {
		return nil, failure
	}
	if answer == nil {
		return json.RawMessage(`{}`), nil
	}
	encoded, err := json.Marshal(answer)
	return encoded, err
}

func (f *fakeWorker) CallInto(_ context.Context, command protocol.Command, out any) error {
	payload, err := f.Call(context.Background(), command)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, out)
}

func (f *fakeWorker) OnFrame(handler func(protocol.Frame)) {
	f.mu.Lock()
	f.frames = handler
	f.mu.Unlock()
}

func (f *fakeWorker) OnExit(handler func(error)) {
	f.mu.Lock()
	f.onExit = handler
	f.mu.Unlock()
}

func (f *fakeWorker) LastWords() string { return f.words }

// asked is every command it was sent, in order.
func (f *fakeWorker) asked() []protocol.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.Command(nil), f.commands...)
}

// controlWithWorker is a control whose harness is the fake, with no sandbox and no worker process.
func controlWithWorker(t *testing.T, worker *fakeWorker) *Control {
	t.Helper()
	control := New(Options{
		Workspace:    Workspace{Name: "w", Path: t.TempDir()},
		DataDir:      t.TempDir(),
		SettingsPath: t.TempDir() + "/settings.json",
		Credentials:  t.TempDir() + "/credentials.json",
	})
	control.worker = worker
	control.workerUp = true
	return control
}

// A describe is a catalog: providers, models, and the levels each model has. Everything the
// pickers show comes from here, so it is the command control must send first and the answer it
// must keep.
func TestACatalogComesFromTheWorkerAndIsKept(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["describe"] = protocol.Describe{
		Provider: "faux",
		Providers: []protocol.ProviderInfo{{
			ID: "faux", Name: "Dev", Authenticated: true,
			Models: []protocol.ModelInfo{{ID: "faux-1", Name: "One", ThinkingLevels: []string{"low"}}},
		}},
	}
	control := controlWithWorker(t, worker)
	if err := control.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	asked := worker.asked()
	if len(asked) == 0 || asked[0].Type != "describe" {
		t.Fatalf("the first command should be describe, got %+v", asked)
	}
	if got := control.Describe().Provider; got != "faux" {
		t.Errorf("the catalog says %q", got)
	}
	if got := control.supportedThinking("faux", "faux-1"); len(got) != 1 || got[0] != "low" {
		t.Errorf("levels = %v", got)
	}
}

// A failure from the harness is returned, not swallowed: the caller is a screen that says why.
func TestAFailureFromTheHarnessIsReturned(t *testing.T) {
	worker := newFakeWorker()
	worker.failure = errors.New("the harness said no")
	control := controlWithWorker(t, worker)
	if err := control.Refresh(context.Background()); err == nil {
		t.Fatal("a failed describe should be an error")
	} else if !contains(err.Error(), "the harness said no") {
		t.Errorf("the error should keep the harness's words: %v", err)
	}
}

// Signing out clears every binding that named that provider — a conversation pinned to a provider
// nobody is signed in to cannot run — and then re-describes, because the catalog changed.
func TestSigningOutClearsBindingsAndAsksAgain(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["describe"] = protocol.Describe{Provider: "faux"}
	control := controlWithWorker(t, worker)
	control.bindings["1"] = protocol.ModelBinding{Provider: "gone", ModelID: "m"}
	control.bindings["2"] = protocol.ModelBinding{Provider: "stays", ModelID: "m"}

	if err := control.Logout(context.Background(), "gone"); err != nil {
		t.Fatal(err)
	}
	if got := control.bindings["1"]; got.Provider != "" || got.ModelID != "" {
		t.Errorf("the binding to the provider that left should be cleared: %+v", got)
	}
	if got := control.bindings["2"]; got.Provider != "stays" {
		t.Errorf("another provider's binding should be untouched: %+v", got)
	}
	asked := worker.asked()
	if asked[0].Type != "logout" || asked[0].ProviderID != "gone" {
		t.Errorf("the first command should be the logout, got %+v", asked[0])
	}
	last := asked[len(asked)-1]
	if last.Type != "describe" {
		t.Errorf("a sign-out should re-read the catalog, last command was %q", last.Type)
	}
}

// A sign-out the harness refuses is returned, and nothing is cleared: a binding dropped for a
// logout that did not happen would leave a conversation that cannot run.
func TestARefusedSignOutChangesNothing(t *testing.T) {
	worker := newFakeWorker()
	worker.failure = errors.New("no such provider")
	control := controlWithWorker(t, worker)
	control.bindings["1"] = protocol.ModelBinding{Provider: "p", ModelID: "m"}
	if err := control.Logout(context.Background(), "p"); err == nil {
		t.Fatal("a refused logout should be an error")
	}
	if got := control.bindings["1"]; got.Provider != "p" {
		t.Errorf("the binding should survive: %+v", got)
	}
}

// A sign-in is started and answered with an id before the flow finishes: the browser needs
// something to name the flow with, and the flow's own answers arrive later.
func TestASignInAnswersWithAnIDFirst(t *testing.T) {
	worker := newFakeWorker()
	worker.answer["login"] = map[string]any{}
	worker.answer["describe"] = protocol.Describe{Provider: "faux"}
	control := controlWithWorker(t, worker)
	authID, err := control.Login(context.Background(), "faux", "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if authID == "" {
		t.Fatal("a login must be nameable")
	}
	first, second := authID, ""
	second, _ = control.Login(context.Background(), "faux", "api_key")
	if first == second {
		t.Errorf("two sign-ins should have different ids, both %q", first)
	}
}

// The words a dying harness said are part of the reason a workspace is down: "exit status 1" is
// not something a person can act on, and the line that is one came from the harness.
func TestTheFailureKeepsWhatTheHarnessSaid(t *testing.T) {
	worker := newFakeWorker()
	worker.words = "error: no api key for provider: add one with acornfall provider add"
	control := controlWithWorker(t, worker)
	control.workerFailed(worker, errors.New("exit status 1"))

	if control.WorkerUp() {
		t.Error("a dead harness is down")
	}
	reason := control.WorkerError()
	if !contains(reason, "exit status 1") || !contains(reason, "no api key") {
		t.Errorf("the reason should carry both the exit and the harness's own line: %q", reason)
	}
}

// A notification from a worker that has already been replaced is not this control's problem: the
// process it describes is gone and the one running is fine. Acting on it marked a live workspace
// down and woke the supervisor to start a second harness over the same storage.
func TestALateWordFromAReplacedWorkerIsIgnored(t *testing.T) {
	current := newFakeWorker()
	dead := newFakeWorker()
	control := controlWithWorker(t, current)
	control.workerFailed(dead, errors.New("exit status 1"))
	if !control.WorkerUp() {
		t.Error("a message about a replaced worker should not mark the live one down")
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
