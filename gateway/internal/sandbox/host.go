package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// TestHostEnv enables the host engine. It is the only way to have one: an install without it
// has no host engine at all — not a default, not a choice, not a row in a chooser. The engine
// exists so automated tests can read and write a workspace without paying for a sandbox, and
// it runs commands with the user's own reach, so no install gets it by accident.
const TestHostEnv = "ACORND_TEST_HOST"

// HostEnabled reports whether this process was told to have the host engine, by TestHostEnv
// being set to anything.
func HostEnabled() bool { return strings.TrimSpace(os.Getenv(TestHostEnv)) != "" }

// Host is the engine that does not sandbox anything: commands and file operations happen on
// the host, in the workspace, with the user's own permissions and reach.
//
// It exists because the worker's only door to a workspace is its sandbox engine, so a test
// that only reads a workspace's files would otherwise pay for a sandbox per case. It is off in
// every install — no default, no chooser row, no `--sandbox host` — and only a process told to
// have it (TestHostEnv) can name it. Doctor, the state frame, and the startup log all say it
// is not isolated. See docs/adr/0015-host-engine.md.
type Host struct{}

// NewHost builds the driver. Unlike the microVM engine it installs nothing.
func NewHost() *Host { return &Host{} }

func (h *Host) Name() string { return "host" }

// Version is empty: there is no release to report for "the host you are on".
func (h *Host) Version() string { return "" }

// Isolated reports that nothing separates a command from the host. The state says so, because
// a client that showed a sandbox badge for this engine would be lying.
func (h *Host) Isolated() bool { return false }

// InstallDir and StateDir are empty: this engine has no artifacts and keeps no state. A
// caller with work to store has the workspace and the installation folder.
func (h *Host) InstallDir() string { return "" }
func (h *Host) StateDir() string   { return "" }

// Env is empty: there is no engine store for the worker to find.
func (h *Host) Env() []string { return nil }

func (h *Host) Installed() bool { return true }

func (h *Host) Install(context.Context, func(string, ...any)) error { return nil }

// Usable is always true. It is the reason this engine exists: no hypervisor, no kernel
// facility, no group — just the machine the gateway already runs on.
func (h *Host) Usable() error { return nil }

// Egress is the machine's own network: the host engine runs commands where they already are.
func (h *Host) Egress() error { return nil }

// Nesting reports that there is no sandbox here to nest: the host engine's commands already
// run on the machine itself, with all of it. The refusal is the honest one — a badge saying
// "nested" over an unconfined run would be worse than useless.
func (h *Host) Nesting(context.Context) NestingSupport {
	return Unavailable("the host engine is not a sandbox: commands already run on the machine itself")
}

// List reports nothing: there are no sandboxes to list. Each workspace is its own folder.
func (h *Host) List(context.Context) ([]Instance, error) { return nil, nil }

// Create does nothing. Nothing is made for a workspace that runs on the host; the workspace
// folder is the sandbox.
func (h *Host) Create(context.Context, Policy, string) error { return nil }

// SecretDelivery is the environment: there is no guest to keep a value out of. A value in
// the worker's own process is what a command on the host sees.
func (h *Host) SecretDelivery() Delivery { return DeliveryEnvironment }

// SecretEnvInWorker puts the values in the worker's process environment, which is where the
// host engine's commands read their environment.
func (h *Host) SecretEnvInWorker() bool { return true }

// SecretsChanged cannot change a running process's environment. The worker is restarted,
// which reads the new set on the way up.
func (h *Host) SecretsChanged(context.Context, string, []Secret) (SecretChange, error) {
	return SecretChange{RestartRequired: true}, nil
}

func (h *Host) Start(context.Context, string, []Secret) error { return nil }
func (h *Host) Stop(context.Context, string) error            { return nil }
func (h *Host) Remove(context.Context, string) error          { return nil }

// Engines are the drivers this build can run, by the name the worker matches with its own
// client. There is no default here: an empty name is a caller that never asked the machine
// (Tier.Default is the one place that answers it), and a name nobody implements is refused with
// the list rather than answered with another engine — a run that meant microsandbox and got the
// host would be a security change nobody asked for.
func Engine(dataDir, state, name string) (Driver, error) {
	if make, ok := driverFactories[name]; ok {
		return make(dataDir, state), nil
	}
	switch name {
	case "host":
		if !HostEnabled() {
			return nil, fmt.Errorf("sandbox: the host engine is off: it isolates nothing, so only automated tests enable it (%s=1)", TestHostEnv)
		}
		return NewHost(), nil
	default:
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("sandbox: no engine named (choose one of " + strings.Join(ChoosableEngines(), ", ") + ")")
		}
		return nil, errors.New("sandbox: unknown engine " + name + " (" + strings.Join(ChoosableEngines(), ", ") + ")")
	}
}

// EngineNames are the engines this build offers a workspace, in the order a machine prefers
// them: bubblewrap first because it needs no hypervisor, no root, and no device — then youki,
// which is the same boundary with a policy file — then the microVM, which is the
// stronger boundary when a machine can give it one — then the host, which isolates nothing and
// is last for that reason. It is the order a chooser shows too (ADR 0026). The host is off
// unless this process was told to have it: see ChoosableEngines.
//
// The default order is not this list: youki is selectable per workspace, and it stays out of
// the default until its tier is green on a machine with and without systemd (issue #47 R12).
// See defaultEngines.
func EngineNames() []string { return []string{"bwrap", "youki", "microsandbox", "host"} }

// ChoosableEngines are the engines an install may name or offer, in preference order: every
// engine this build has, minus the host engine unless this process was told to have one. It is
// the list a refusal quotes and the list a chooser shows, so a normal install never presents
// `host` as something to pick.
func ChoosableEngines() []string {
	names := make([]string, 0, len(EngineNames()))
	for _, name := range EngineNames() {
		if name == "host" && !HostEnabled() {
			continue
		}
		names = append(names, name)
	}
	return names
}

// ValidEngine reports whether a workspace may name an engine. The empty string is valid and
// means the install's default; anything else has to be one this install offers, because a
// workspace that named an engine nobody can run would have no sandbox at all.
func ValidEngine(name string) bool {
	if name == "" {
		return true
	}
	return slices.Contains(ChoosableEngines(), name)
}
