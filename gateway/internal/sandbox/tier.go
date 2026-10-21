package sandbox

import (
	"context"
	"strings"
	"sync"
)

// Tier is every engine one install can run, one Manager each. A workspace names its engine,
// so control asks for the manager by that name; a workspace that names none gets the
// install's default, which is what `--sandbox-engine` chooses.
//
// Managers are built the first time an engine is asked for, because opening one installs and
// probes nothing until a workspace actually needs it. A machine with three engines configured
// and one in use pays for one.
type Tier struct {
	home  string
	state string
	log   func(string, ...any)

	mu       sync.Mutex
	managers map[string]*Manager
}

// NewTier holds the install's engines. home and state are passed to each driver: state is the
// engine store override, empty for the default under home.
func NewTier(home, state string, log func(string, ...any)) *Tier {
	return &Tier{home: home, state: state, log: log, managers: map[string]*Manager{}}
}

// For returns the manager for an engine name, building it once. An engine this build does not
// implement is an error, never a fallback to another engine — a workspace that asked for a
// sandbox must not be handed a different one. An empty name is resolved by Default, because
// "which engine" is a question the machine answers, not a constant.
func (t *Tier) For(name string) (*Manager, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = t.Default()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if manager, ok := t.managers[name]; ok {
		return manager, nil
	}
	driver, err := Engine(t.home, t.state, name)
	if err != nil {
		return nil, err
	}
	manager, err := Open(context.Background(), Options{Home: t.home, Driver: driver, Log: t.log})
	if err != nil {
		return nil, err
	}
	t.managers[name] = manager
	return manager, nil
}

// Default is the engine a workspace that names none gets on this machine: the first one it can
// actually run, in EngineNames' order. It probes the drivers themselves, which is cheap — a user
// namespace for bubblewrap, /dev/kvm for the microVM — and it does not build a manager, so
// nothing is installed to answer the question.
//
// The host engine is reachable only when the others are not, and that is deliberate: it isolates
// nothing, so it is what is left, not what is preferred.
func (t *Tier) Default() string {
	for _, name := range EngineNames() {
		driver, err := Engine(t.home, t.state, name)
		if err != nil {
			continue
		}
		if err := driver.Usable(); err != nil {
			continue
		}
		// A workspace's network is on by default, so an engine that cannot carry one is not a
		// default: it would refuse every workspace it was handed.
		if err := driver.Egress(); err != nil {
			continue
		}
		return name
	}
	return "host"
}
