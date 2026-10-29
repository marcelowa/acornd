// the worker process: starting it, supervising it, and what control does when it dies
//
// Part of the control package. The file is named after the job, not the layer: this is the
// one thing it does, and everything it needs from its siblings is a call, not a comment.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
	"github.com/acornfall/acornfall/gateway/internal/sandbox"
	"github.com/acornfall/acornfall/gateway/internal/workerpool"
)

// attachToWorker is everything that needs a live harness: start it, ask what it can do, read
// the agent catalog, and attach to a conversation.
func (c *Control) attachToWorker(ctx context.Context) error {
	var describe protocol.Describe
	if err := c.startWorker(ctx); err != nil {
		return err
	}
	if err := c.worker.CallInto(ctx, protocol.Command{Type: "describe"}, &describe); err != nil {
		return fmt.Errorf("describe worker: %w", err)
	}
	c.applyDescribe(describe)

	if _, err := c.Agents(ctx); err != nil {
		return fmt.Errorf("read agent catalog: %w", err)
	}
	persistedActive, err := c.restoreState()
	if err != nil {
		return err
	}
	return c.adoptConversation(ctx, persistedActive)
}

// restoreState is everything that has to be true before a conversation can run: our own
// settings, which hold the defaults and the conversation that was open, and a last-resort
// model so a conversation that never chose one still has something to run.
//
// Shared with recovery on purpose. Recovery used to skip it, which was invisible after a
// crash — the values were already in memory — and meant that a workspace whose *first* start
// failed came back with no model and answered nothing.
func (c *Control) restoreState() (string, error) {
	// Our own state first: it holds the default model this install already uses.
	persistedActive := c.loadSettings()
	if err := c.ensureLastResort(); err != nil && !errors.Is(err, ErrNoProvider) {
		return persistedActive, fmt.Errorf("resolve default model: %w", err)
	}
	return persistedActive, nil
}

// adoptConversation attaches this control to a conversation, creating the root one only when
// the storage is empty.
//
// Listing comes first, and that order is the point: the harness creates its own root
// conversation the first time it is used, so deciding to create one without looking makes a
// second, leaves the root empty, and lands on the copy.
func (c *Control) adoptConversation(ctx context.Context, persistedActive string) error {
	conversations, err := c.listConversations(ctx)
	if err != nil {
		return fmt.Errorf("list conversations: %w", err)
	}
	if len(conversations) == 0 {
		// Nobody is here yet, so the first conversation is the workspace's: it is not a person's landing, and
		// naming one would invent a caller this control has not met (ADR 0034 §7).
		if _, err := c.createConversation(ctx); err != nil {
			return fmt.Errorf("create first conversation: %w", err)
		}
		return nil
	}

	active := persistedActive
	if !hasConversation(conversations, active) {
		active = defaultConversation(conversations)
	}
	// The workspace's fallback, not a landing: this runs before anybody has asked, and it is what a caller with no
	// landing of their own opens on.
	c.mu.Lock()
	c.active = active
	c.mu.Unlock()
	// No conversation has an agent yet and no provider is connected is a normal
	// first run, not a failure.
	if err := c.ensureConversation(ctx, active); err != nil && !errors.Is(err, ErrNoProvider) {
		return err
	}
	return nil
}

// startWorker launches the harness process and arranges for its death to be noticed.
//
// It refuses to start a second one. A death is announced twice — the process's own exit, and
// the failure of whatever request found it — and without this the second announcement put a
// second harness over the same storage, which is not a restart, it is two writers.
func (c *Control) startWorker(ctx context.Context) error {
	if c.WorkerUp() {
		return errors.New("a worker is already running")
	}
	command := c.opts.WorkerCommand
	if len(command) == 0 {
		command = defaultWorkerCommand
	}
	// The sandbox is made first, because the worker is told to use it and refuses to run
	// anything without it. A failure is a state, not a stop: the workspace opens, the tools
	// say what is missing, and the rest of the product works.
	c.stage("preparing the sandbox")
	sandboxState := c.provisionSandbox(ctx)
	args := append(c.workerArgs(command[1:]), c.sandboxArgs(sandboxState)...)
	if err := ctx.Err(); err != nil {
		return err
	}
	delivery := c.SecretDelivery()
	delivered := c.resolveSecrets(delivery).delivered
	args = append(args, secretArgs(delivery, delivered)...)
	manager := c.sandboxManager()
	worker := workerpool.New(workerpool.Config{
		Command: command[0],
		Args:    args,
		Dir:     c.opts.WorkerDir,
		Env:     workerEnv(manager, delivered),
	})
	worker.OnFrame(c.onFrame)
	if err := worker.Start(ctx); err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	// Start does not take a context (a worker's lifetime belongs to its owner, not to the
	// request that started it), so closing the window here is what keeps a control that was
	// closed mid-start from leaving a process behind.
	if err := ctx.Err(); err != nil {
		worker.Close()
		return err
	}
	c.mu.Lock()
	c.worker = worker
	c.workerUp = true
	c.workerError = ""
	c.restarting = false
	c.sandbox = sandboxState
	c.mu.Unlock()
	// The identity of the dying worker is part of the notification: a worker that has already
	// been replaced must not be able to mark this control down.
	worker.OnExit(func(err error) { c.workerFailed(worker, err) })
	return nil
}

// defaultWorkerCommand is the harness this gateway ships. A release needs no bun and no
// sources; the development install runs the worker's own entry point.
var defaultWorkerCommand = []string{"bun", "run", "src/main.ts"}

// workerArgs is what every worker is told about its workspace, before anything conditional.
func (c *Control) workerArgs(base []string) []string {
	args := append([]string(nil), base...)
	return append(args,
		"--workspace", c.opts.Workspace.Path,
		"--storage", c.opts.StoragePath,
		"--credentials", c.opts.Credentials,
		"--data-dir", c.opts.DataDir,
	)
}

// sandboxArgs names the sandbox the worker must use, and the engine it is on: the worker
// attaches with that engine's SDK, so naming it is how two sides of one install cannot
// disagree about which engine is in use.
func (c *Control) sandboxArgs(state SandboxState) []string {
	if !state.Tools {
		return nil
	}
	args := []string{"--sandbox", sandbox.Name(c.opts.Workspace.Path)}
	if driver := c.sandboxDriver(); driver != nil {
		args = append(args, "--sandbox-engine", driver.Name())
	}
	return args
}

// secretArgs is the prompt's view of the vault: names, descriptions, hosts and a placeholder.
// A value never travels here — a substitute engine keeps it, and an environment engine hands
// it over in the process environment instead.
func secretArgs(delivery sandbox.Delivery, delivered []sandbox.Secret) []string {
	if len(delivered) == 0 {
		return nil
	}
	metadata := make([]map[string]any, 0, len(delivered))
	for _, secret := range delivered {
		metadata = append(metadata, map[string]any{
			"name":        secret.Name,
			"description": secret.Description,
			"hosts":       secret.Hosts,
			"delivery":    delivery.String(),
			"placeholder": secretPlaceholder(delivery, secret.Name),
		})
	}
	// A secret list that cannot be encoded is better absent than half-sent: the worker then
	// reports the vault empty, which is the same answer it gives when it is.
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil
	}
	return []string{"--secrets", string(encoded)}
}

// workerEnv is the engine's environment, plus the secrets an environment engine delivers into
// the worker's own process. They go last so the shell cannot shadow them.
func workerEnv(manager *sandbox.Manager, delivered []sandbox.Secret) []string {
	env := sandboxEnv(manager)
	if manager != nil && manager.SecretEnvInWorker() {
		for _, secret := range delivered {
			env = append(env, secret.Name+"="+secret.Value)
		}
	}
	return env
}

// supervise owns the worker's lifecycle so restarts cannot multiply. It is the only
// reader of workerExited, so there is exactly one backoff loop however often the
// process dies.
func (c *Control) supervise(ctx context.Context) {
	failures := 0
	for {
		if !c.waitForExit(ctx) {
			return
		}
		// Nothing to restart: the announcement was a duplicate, not a death.
		if c.WorkerUp() {
			continue
		}
		failures += 1
		if failures > maxRestarts {
			// Give up rather than burn the machine on a crash loop. The state still says
			// the harness is down, so the interface is honest about it.
			c.markWorkerLost("the harness process could not be restarted")
			if !c.wait(ctx, restartCooldown) {
				return
			}
			failures = 0
			continue
		}
		if !c.wait(ctx, time.Duration(failures)*time.Second) {
			return
		}
		if c.restart(ctx, failures) {
			failures = 0
		}
	}
}

const (
	// maxRestarts is how many deaths in a row the supervisor treats as a transient failure
	// before it stops trying and says so.
	maxRestarts = 5
	// restartCooldown is how long a crash loop is left alone before one more attempt.
	restartCooldown = 60 * time.Second
)

// waitForExit blocks until the current worker announces its end, or the owner stops.
func (c *Control) waitForExit(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-c.workerExited:
		return true
	}
}

// wait is the one place a deadline is slept on, so the owner's cancellation always wins.
func (c *Control) wait(ctx context.Context, delay time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

// restart tries once. A process that never started announces nothing, so the loop is told
// here rather than waiting for an exit that will not come.
func (c *Control) restart(ctx context.Context, failures int) bool {
	if err := c.startWorker(ctx); err != nil {
		workerpool.Log("worker restart %d failed: %v", failures, err)
		select {
		case c.workerExited <- struct{}{}:
		default:
		}
		return false
	}
	if err := c.recoverWithWorker(ctx); err != nil {
		workerpool.Log("worker restart %d could not be resumed: %v", failures, err)
		return false
	}
	workerpool.Log("worker restarted and resumed")
	return true
}

// markWorkerLost is the state a supervisor that gave up leaves behind.
func (c *Control) markWorkerLost(reason string) {
	c.mu.Lock()
	c.workerError = reason
	c.mu.Unlock()
	c.publish(protocol.Frame{Type: "state"})
}

// workerDied is the difference between a quiet conversation and a dead backend. Without
// it the gateway keeps serving a cached view and everything looks idle and healthy.
func (c *Control) workerFailed(worker workerConn, err error) {
	c.mu.Lock()
	// A notification from a worker that has already been replaced is not this control's
	// problem: the process it describes is gone, and the one running now is fine. Acting on it
	// marked a live workspace down, and the signal woke the supervisor to start a second
	// harness over the same storage — two writers, from a late message about a dead process.
	if c.restarting || c.worker != worker {
		c.mu.Unlock()
		workerpool.Log("worker died: %v (already replaced)", err)
		return
	}
	reason := "the harness process ended"
	if err != nil {
		reason = fmt.Sprintf("the harness process ended: %v", err)
	}
	// What that harness itself said, when it said anything: "exit status 1" is not a reason
	// anyone can act on, and the line that is one is in its output. Read from the worker that
	// died, not from whatever is current by the time this runs.
	if words := worker.LastWords(); words != "" {
		reason = fmt.Sprintf("%s — %s", reason, words)
	}
	c.workerUp = false
	c.workerError = reason
	c.mu.Unlock()
	c.dropSecrets("")
	workerpool.Log("worker died: %s", reason)
	c.publish(protocol.Frame{Type: "state"})
	// The supervisor restarts: work is committed before it is shown, so a fresh process
	// resumes every run from its last commit.
	select {
	case c.workerExited <- struct{}{}:
	default:
	}
}

// recoverWithWorker re-reads what the new process knows and re-attaches the conversation
// a client is looking at, so a restart is invisible apart from the pause.
func (c *Control) recoverWithWorker(ctx context.Context) error {
	var describe protocol.Describe
	if err := c.worker.CallInto(ctx, protocol.Command{Type: "describe"}, &describe); err != nil {
		return err
	}
	c.applyDescribe(describe)
	if _, err := c.Agents(ctx); err != nil {
		return err
	}
	persistedActive, err := c.restoreState()
	if err != nil {
		return err
	}
	// The same adoption as a first start, so a workspace that comes back lands where it was
	// rather than on a conversation it just made.
	active := c.activeConversation()
	if active == "" {
		active = persistedActive
	}
	if err := c.adoptConversation(ctx, active); err != nil {
		return err
	}
	c.publish(protocol.Frame{Type: "state"})
	return nil
}

// watchConversations keeps the rail honest about runs happening elsewhere. A run
// belongs to the harness, not to an attachment, so a conversation you are not
// looking at can be working. Nothing pushes that news, so it is polled -- and
// published only when a signature changes, so a quiet workspace stays quiet.
func (c *Control) watchConversations(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !c.Watching() {
				continue
			}
			if _, err := c.listConversations(ctx); err != nil {
				continue
			}
			if c.stateStale() {
				c.publish(protocol.Frame{Type: "state"})
			}
		}
	}
}

// watchedConversations lists the distinct conversations clients are attached to.
func (c *Control) watchedConversations() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := map[string]bool{}
	list := make([]string, 0, len(c.attachments))
	for id, count := range c.attachments {
		if count > 0 && !seen[id] {
			seen[id] = true
			list = append(list, id)
		}
	}
	if c.active != "" && !seen[c.active] {
		list = append(list, c.active)
	}
	return list
}

// WorkerCall is an escape hatch for small read-only commands, so a diagnostic does
// not need a method and an endpoint of its own.
func (c *Control) WorkerCall(ctx context.Context, kind, conversationID string) (json.RawMessage, error) {
	worker, err := c.currentWorker()
	if err != nil {
		return nil, err
	}
	return worker.Call(ctx, protocol.Command{Type: kind, ConversationID: conversationID})
}

// currentWorker is the live harness, or a refusal. A control that failed to open has none;
// an endpoint asked anyway should answer that, not dereference nil.
func (c *Control) currentWorker() (workerConn, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.worker == nil {
		return nil, errors.New("the harness is not running")
	}
	return c.worker, nil
}

// WorkerUp reports whether this workspace's harness process is running.
// WorkerError is what went wrong when the harness did not come up, in its own words.
func (c *Control) WorkerError() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.workerError
}

func (c *Control) WorkerUp() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.workerUp
}

// watchingConversation reports whether a client is looking at this conversation. A card in
// another conversation's tab does not answer the tool that asked here.
func (c *Control) watchingConversation(conversationID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, watched := range c.subscriberConversation {
		if watched == conversationID {
			return true
		}
	}
	return false
}

// Watching reports whether a client is attached to any of this workspace's conversations.
func (c *Control) Watching() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.subscribers) > 0
}

// Busy reports whether any conversation here is running. An eviction must not stop a
// worker mid-run, even when nobody is watching it.
func (c *Control) Busy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, info := range c.conversations {
		if info.Running {
			return true
		}
	}
	return false
}

// stopWorker terminates the worker process and leaves everything else:
// the sandbox stays warm for whoever boots next, and the storage keeps
// its record. The update tier calls it before wiping sandboxes (so no
// orphan resurrects them) and before self-restart (so no orphan keeps
// serving old code). The next open starts a fresh worker.
func (c *Control) stopWorker() {
	c.mu.Lock()
	worker := c.worker
	c.restarting = true
	c.workerUp = false
	c.mu.Unlock()
	if worker != nil {
		c.dropSecrets("")
		worker.Close()
	}
}

// RestartWorker replaces the harness with one that reads the current environment. The old
// worker is stopped first; its exit notification is ignored because it is no longer the
// current process, and the new one is recovered the same way a supervised restart is.
func (c *Control) RestartWorker(ctx context.Context) error {
	c.stopWorker()
	if err := c.startWorker(ctx); err != nil {
		select {
		case c.workerExited <- struct{}{}:
		default:
		}
		return err
	}
	return c.recoverWithWorker(ctx)
}

// Refresh re-reads the worker's provider catalog and adopt a usable agent
// binding for the active conversation when the current one is unusable.
func (c *Control) Refresh(ctx context.Context) error {
	var describe protocol.Describe
	if err := c.worker.CallInto(ctx, protocol.Command{Type: "describe"}, &describe); err != nil {
		return err
	}
	c.applyDescribe(describe)
	if err := c.ensureLastResort(); err != nil && !errors.Is(err, ErrNoProvider) {
		return err
	}
	// Every conversation somebody lands on: a catalog change is a change to what they will run next, and a
	// workspace's fallback is not the only one after ADR 0034 §7.
	for _, active := range c.landedConversations() {
		if err := c.ensureConversation(ctx, active); err != nil && !errors.Is(err, ErrNoProvider) {
			return err
		}
	}
	return nil
}
