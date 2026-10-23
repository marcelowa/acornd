// the sandbox engine this workspace runs on, and the steps that bring one up
//
// Part of the control package. The file is named after the job, not the layer: this is the
// one thing it does, and everything it needs from its siblings is a call, not a comment.
package control

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/sandbox"
	"github.com/acornfall/acornfall/gateway/internal/workerpool"
)

// SandboxState says what the agent's tools can reach here. When Tools is false the tools
// refuse rather than running on the host, and Problem says why, because a silent fallback
// would be worse than a refusal. Isolated says whether the engine separates a command from
// the host: false is the host engine, and the interface has to be able to say so.
type SandboxState struct {
	Engine   string `json:"engine,omitempty"`
	Version  string `json:"version,omitempty"`
	Status   string `json:"status,omitempty"`
	Tools    bool   `json:"tools"`
	Isolated bool   `json:"isolated"`
	Problem  string `json:"problem,omitempty"`
	// Nested says the next sandbox will nest: nesting was not disabled and the machine can
	// grant it — like an image change, nesting applies when a sandbox is created.
	// NestedReason says why not when a workspace that allows nesting cannot get it, so it
	// is visible rather than a surprise. A workspace that disables nesting carries neither.
	Nested       bool   `json:"nested,omitempty"`
	NestedReason string `json:"nestedReason,omitempty"`
	// Secrets is the workspace's effective set as the engine delivers it. Blocked entries
	// stay in the list with the reason, so the interface never hides a secret someone set.
	// SecretsRestart is true when the engine could not put the change into the running
	// sandbox; the next start will. SecretsProblem is why the vault could not be read.
	Secrets        []SandboxSecret `json:"secrets,omitempty"`
	SecretsRestart bool            `json:"secretsRestart,omitempty"`
	SecretsProblem string          `json:"secretsProblem,omitempty"`
}

// sandbox is the manager for this workspace's engine: the workspace's own choice when its
// record names one, the install's default when it does not. An engine this build does not
// implement is a problem the state reports, never a fallback to another engine — a workspace
// that asked for a sandbox must not quietly be handed a different one.
func (c *Control) sandboxManager() *sandbox.Manager {
	c.engineMu.Lock()
	defer c.engineMu.Unlock()
	if c.engineDone {
		return c.engineManager
	}
	c.engineDone = true
	// Read the record under the record's own lock: SetWorkspace may be rewriting it.
	c.mu.RLock()
	engine := c.opts.Workspace.Engine
	c.mu.RUnlock()
	if c.opts.Sandboxes != nil && strings.TrimSpace(engine) != "" {
		manager, err := c.opts.Sandboxes.For(engine)
		if err != nil {
			c.engineProblem = err
			return nil
		}
		c.engineManager = manager
		return manager
	}
	c.engineManager = c.opts.Sandbox
	return c.engineManager
}

// engineList is what a new workspace may run on. Nil when this install has no tier: an empty
// list and "no sandbox at all" are different states, and the interface shows them differently.
func (c *Control) engineList() []EngineInfo {
	if c.opts.Engines == nil {
		return nil
	}
	return c.opts.Engines()
}

// sandboxDriver is the engine behind this workspace, or nil.
func (c *Control) sandboxDriver() sandbox.Driver {
	if manager := c.sandboxManager(); manager != nil {
		return manager.Driver()
	}
	return nil
}

// sandboxProblem is why this workspace has no manager, when it named an engine this build
// does not implement. Nil is the ordinary answer.
func (c *Control) sandboxProblem() error {
	c.engineMu.Lock()
	defer c.engineMu.Unlock()
	return c.engineProblem
}

// sandboxEnv is what the worker needs to reach the engine's sandboxes, or nothing when there
// is no sandbox tier at all.
func sandboxEnv(manager *sandbox.Manager) []string {
	if manager == nil || manager.Driver() == nil {
		return nil
	}
	return manager.Driver().Env()
}

// stage reports what this workspace's start is doing, to whoever is watching. Nil-safe: a
// Control built by a command or a test has nobody to tell.
func (c *Control) stage(step string) {
	if c.opts.OnStage != nil {
		c.opts.OnStage(step)
	}
}

// provisionSandbox makes sure this workspace has a sandbox and reports what the tools may
// reach. It never fails the worker: a machine without a hypervisor is a supported state, and
// the tools refuse rather than running unconfined.
func (c *Control) provisionSandbox(ctx context.Context) SandboxState {
	manager := c.sandboxManager()
	if manager == nil {
		return c.sandboxRefusal()
	}
	report := manager.Report()
	state := SandboxState{Engine: report.Engine, Version: report.Version, Isolated: report.Isolated}
	if problem := manager.Problem(); problem != nil {
		state.Problem = problem.Error()
		return state
	}
	delivery := manager.SecretDelivery()
	resolved := c.resolveSecrets(delivery)
	policy := c.opts.Workspace.SandboxPolicy(c.opts.DataDir, c.opts.UserHome)
	policy.OnStage = c.stage
	policy.Secrets = resolved.delivered
	if _, err := manager.Produce(ctx, policy, filepath.Dir(c.opts.StoragePath)); err != nil {
		state.Problem = err.Error()
		workerpool.Log("sandbox for %s: %v", c.opts.Workspace.Name, err)
		return state
	}
	state.Secrets = resolved.state
	state.SecretsProblem = resolved.problem
	if delivery == sandbox.DeliverySubstitute && resolved.problem == "" {
		c.reconcileStoredSecrets(ctx, manager, policy, resolved, &state)
	}
	// Whether the sandbox that is running nests is what it was created with; this says what
	// the next one gets, and why it cannot when it cannot. A workspace that disables nesting
	// carries neither: its own setting, visible beside this state, is the reason.
	if !policy.DisableVMNesting {
		state.Nested, state.NestedReason = nestingFor(ctx, manager)
	}
	if status, err := manager.Status(ctx, c.opts.Workspace.Path); err == nil {
		state.Status = status
	}
	state.Tools = true
	return state
}

// sandboxRefusal is why there is no sandbox, when there is not one: an engine this build has
// and cannot use is a different answer from one it was never given.
func (c *Control) sandboxRefusal() SandboxState {
	if problem := c.sandboxProblem(); problem != nil {
		return SandboxState{Problem: problem.Error()}
	}
	return SandboxState{Problem: "no sandbox is configured: tool execution is disabled"}
}

// reconcileStoredSecrets brings a sandbox that was here before a secret changed up to date. No
// worker is attached yet, so this is the safe moment to restart it: the first turn should not
// begin with an environment that predates the vault.
func (c *Control) reconcileStoredSecrets(
	ctx context.Context,
	manager *sandbox.Manager,
	policy sandbox.Policy,
	resolved sandboxSecrets,
	state *SandboxState,
) {
	change, err := manager.SecretsChanged(ctx, policy.Workspace, resolved.delivered)
	if err != nil {
		state.SecretsProblem = err.Error()
		return
	}
	if !change.RestartRequired {
		return
	}
	if err := manager.Stop(ctx, policy.Workspace); err != nil {
		state.SecretsRestart = true
		return
	}
	if _, err := manager.Produce(ctx, policy, filepath.Dir(c.opts.StoragePath)); err != nil {
		state.SecretsRestart = true
		workerpool.Log("apply stored secrets to %s: %v", c.opts.Workspace.Name, err)
		return
	}
	if status, err := manager.Status(ctx, policy.Workspace); err == nil {
		state.Status = status
	}
}

// nestingFor is what the next sandbox gets, and the reason when the machine cannot do it.
func nestingFor(ctx context.Context, manager *sandbox.Manager) (nested bool, reason string) {
	support := manager.Nesting(ctx)
	if support.Available {
		return true, ""
	}
	return false, support.Reason
}
