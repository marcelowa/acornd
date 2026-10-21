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

// For returns the manager for an engine name, building it once. The empty name means the
// default engine, microsandbox; an engine this build does not implement is an error, never a
// fallback to another engine — a workspace that asked for a sandbox must not be handed a
// different one.
func (t *Tier) For(name string) (*Manager, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultEngine
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

// DefaultEngine is the engine a workspace that names none runs on.
const DefaultEngine = "microsandbox"
