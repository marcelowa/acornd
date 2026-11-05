package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrNoEngine is what a machine that can run no sandbox answers with. It is not a failed
// probe: both engines that isolate anything are unavailable here, so there is nothing to hand
// a workspace that named none. A caller tells it apart from a bad name because the repair is
// different — a machine to prepare, not a typo.
var ErrNoEngine = errors.New("no sandbox engine can run on this machine")

// Tier is every engine one install can run, one Manager each. A workspace names its engine,
// so control asks for the manager by that name; a workspace that names none gets the manager
// the install opened as its default, and a call with no name at all gets this machine's answer
// (Default).
//
// Managers are built the first time an engine is asked for, because opening one installs and
// probes nothing until a workspace actually needs it. A machine with three engines configured
// and one in use pays for one.
type Tier struct {
	dataDir string
	state   string
	log     func(string, ...any)

	mu       sync.Mutex
	managers map[string]*Manager
}

// NewTier holds the install's engines. home and state are passed to each driver: state is the
// engine store override, empty for the default under home.
func NewTier(dataDir, state string, log func(string, ...any)) *Tier {
	return &Tier{dataDir: dataDir, state: state, log: log, managers: map[string]*Manager{}}
}

// For returns the manager for an engine name, building it once. An engine this build does not
// implement is an error, never a fallback to another engine — a workspace that asked for a
// sandbox must not be handed a different one. An empty name is resolved by Default, because
// "which engine" is a question the machine answers, not a constant — and a machine with no
// answer is ErrNoEngine rather than the unisolated engine.
func (t *Tier) For(name string) (*Manager, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		// This machine's answer, and its refusal when it has none: a workspace that named no
		// engine gets a sandbox or nothing, never whatever happens to be left over.
		chosen, err := t.Default()
		if err != nil {
			return nil, err
		}
		name = chosen
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if manager, ok := t.managers[name]; ok {
		return manager, nil
	}
	driver, err := Engine(t.dataDir, t.state, name)
	if err != nil {
		return nil, err
	}
	manager, err := Open(context.Background(), Options{DataDir: t.dataDir, Driver: driver, Log: t.log})
	if err != nil {
		return nil, err
	}
	t.managers[name] = manager
	return manager, nil
}

// Default is the engine a workspace that names none gets on this machine: the first one it can
// actually run, in the order a machine prefers them. It probes the drivers themselves, which is
// cheap — a user namespace for bubblewrap, /dev/kvm for the microVM — and it does not build a
// manager, so nothing is installed to answer the question.
//
// It never answers with the host engine. That one isolates nothing, so it is what an operator
// names, not what a machine hands out: falling back to it here is how a machine that used to be
// sandboxed starts running unconfined after an upgrade, with nothing on screen saying so. A
// machine that can run neither engine answers with ErrNoEngine and what each one is missing.
func (t *Tier) Default() (string, error) {
	return defaultEngine(t.canRun)
}

// defaultEngine is Default's rule over a machine's answers, written apart so a test can hand it
// a machine that can run nothing: no runner can be made to have no sandbox at all.
func defaultEngine(probe func(name string) error) (string, error) {
	names := defaultEngines()
	missing := make([]string, 0, len(names))
	for _, name := range names {
		if err := probe(name); err != nil {
			missing = append(missing, name+": "+err.Error())
			continue
		}
		return name, nil
	}
	return "", fmt.Errorf("%w: %s", ErrNoEngine, strings.Join(missing, "; "))
}

// canRun is one engine's answer to the two questions a default asks: can this machine run it,
// and can it carry the network a workspace has on unless it says otherwise. The driver's own
// sentence is passed through, so this refusal and doctor's explanation agree.
func (t *Tier) canRun(name string) error {
	driver, err := Engine(t.dataDir, t.state, name)
	if err != nil {
		return err
	}
	if err := driver.Usable(); err != nil {
		return errors.New(strings.TrimPrefix(err.Error(), ErrUnavailable.Error()+": "))
	}
	return driver.Egress()
}

// defaultEngines are the engines a machine may hand to a workspace that named none, in the
// order it prefers them: EngineNames without the host engine. The host stays in EngineNames
// because a test install can still name it, and doctor still reports what it is; it is never
// the answer a machine gives on its own (ADR 0015).
func defaultEngines() []string {
	names := make([]string, 0, len(EngineNames()))
	for _, name := range EngineNames() {
		if name != "host" {
			names = append(names, name)
		}
	}
	return names
}
