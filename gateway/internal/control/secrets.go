// the vault: what an engine can do with a secret, and the prompts a turn waits on
//
// Part of the control package. The file is named after the job, not the layer: this is the
// one thing it does, and everything it needs from its siblings is a call, not a comment.
package control

import (
	"context"
	"github.com/acornfall/acornfall/gateway/internal/protocol"
	"github.com/acornfall/acornfall/gateway/internal/sandbox"
	"github.com/acornfall/acornfall/gateway/internal/workerpool"
	"path/filepath"
	"strings"
	"time"
)

// SandboxSecret is one secret's delivery state, without its value.
type SandboxSecret struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Scope       string   `json:"scope,omitempty"`
	Hosts       []string `json:"hosts,omitempty"`
	Delivery    string   `json:"delivery,omitempty"`
	Status      string   `json:"status,omitempty"`
	// Placeholder is the literal the guest environment holds on a substitute engine, for
	// the prompt and the list tool. Empty on an environment engine.
	Placeholder string `json:"placeholder,omitempty"`
}

// pendingSecret is one tool's open request. The worker pointer is the process to answer: a
// restart or a crash can make it stale, and the answer then goes nowhere.
type pendingSecret struct {
	requestID      string
	conversationID string
	worker         *workerpool.Worker
	name           string
	description    string
	hosts          []string
}

// SecretPrompt is a pending request as the HTTP layer needs it. The value is never here.
type SecretPrompt struct {
	RequestID      string
	ConversationID string
	Name           string
	Description    string
	Hosts          []string
}

// sandboxSecrets is the vault filtered for one engine: what to deliver, what the interface
// should show, and what could not be read.
type sandboxSecrets struct {
	delivered []sandbox.Secret
	state     []SandboxSecret
	problem   string
}

// secretPlaceholder is the engine's default placeholder for a substitute secret. The
// engine builds it from the name; an environment engine has the real value instead.
func secretPlaceholder(delivery sandbox.Delivery, name string) string {
	if delivery != sandbox.DeliverySubstitute {
		return ""
	}
	return "$MSB_" + name
}

// SecretDelivery is the engine's way of delivering a secret, for the card's validation.
func (c *Control) SecretDelivery() sandbox.Delivery {
	if manager := c.sandboxManager(); manager != nil {
		return manager.SecretDelivery()
	}
	return sandbox.DeliverySubstitute
}

// SecretNeedsHost reports whether this engine can only reach a secret's host when the secret
// names one. It states the rule in the domain's words so the transport does not have to know
// how an engine delivers a value.
func (c *Control) SecretNeedsHost() bool {
	return c.SecretDelivery() == sandbox.DeliverySubstitute
}

// resolveSecrets reads the workspace's effective set and asks what this engine can do with
// each entry. A substitute engine needs an allowed host; an environment engine needs the
// owner to have allowed the value inside. A blocked entry stays visible with its reason.
func (c *Control) resolveSecrets(delivery sandbox.Delivery) sandboxSecrets {
	resolved := sandboxSecrets{}
	if c.opts.Vault == nil {
		return resolved
	}
	entries, err := c.opts.Vault.Resolve(c.opts.Workspace.Path)
	if err != nil {
		resolved.problem = err.Error()
		return resolved
	}
	for _, entry := range entries {
		state := SandboxSecret{
			Name:        entry.Name,
			Description: entry.Description,
			Scope:       string(entry.Scope),
			Hosts:       append([]string(nil), entry.Hosts...),
			Delivery:    delivery.String(),
			Placeholder: secretPlaceholder(delivery, entry.Name),
		}
		switch delivery {
		case sandbox.DeliverySubstitute:
			if len(entry.Hosts) == 0 {
				state.Status = "not delivered: no allowed hosts"
				resolved.state = append(resolved.state, state)
				continue
			}
		case sandbox.DeliveryEnvironment:
			if !entry.AllowEnv {
				state.Status = "not delivered: its value may not enter the sandbox"
				resolved.state = append(resolved.state, state)
				continue
			}
		}
		state.Status = "delivered"
		resolved.state = append(resolved.state, state)
		resolved.delivered = append(resolved.delivered, sandbox.Secret{
			Name:        entry.Name,
			Description: entry.Description,
			Value:       entry.Value,
			Hosts:       append([]string(nil), entry.Hosts...),
			Headers:     entry.Substitution.Headers,
			Query:       entry.Substitution.Query,
			Body:        entry.Substitution.Body,
			AllowEnv:    entry.AllowEnv,
		})
	}
	return resolved
}

// ApplySecrets reconciles a running workspace with the vault after a change. A substitute
// engine rotates live where it can and stores the rest for the next start; the host engine
// cannot change a running process's environment, so its worker is restarted. A stopped
// workspace has nothing to apply: its next start reads the vault.
func (c *Control) ApplySecrets(ctx context.Context, deferRestart bool) error {
	if !c.WorkerUp() {
		return nil
	}
	manager := c.sandboxManager()
	if manager == nil {
		return nil
	}
	if problem := manager.Problem(); problem != nil {
		return problem
	}
	delivery := manager.SecretDelivery()
	resolved := c.resolveSecrets(delivery)
	if resolved.problem != "" {
		c.mu.Lock()
		c.sandbox.Secrets = resolved.state
		c.sandbox.SecretsProblem = resolved.problem
		c.mu.Unlock()
		c.publish(protocol.Frame{Type: "state"})
		return nil
	}
	if delivery == sandbox.DeliveryEnvironment && manager.SecretEnvInWorker() {
		// A value set from a card must not kill the turn that asked for it. If the caller
		// says to defer, or a run is in flight, the worker keeps its old environment, the
		// state says a restart is needed, and the restart happens when the run settles.
		if !deferRestart && !c.AnyRunning() {
			return c.RestartWorker(ctx)
		}
		c.mu.Lock()
		c.sandbox.Secrets = resolved.state
		c.sandbox.SecretsProblem = ""
		c.sandbox.SecretsRestart = true
		c.mu.Unlock()
		c.publish(protocol.Frame{Type: "state"})
		return nil
	}
	change, err := manager.SecretsChanged(ctx, c.opts.Workspace.Path, resolved.delivered)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.sandbox.Secrets = resolved.state
	c.sandbox.SecretsProblem = ""
	c.sandbox.SecretsRestart = change.RestartRequired
	c.mu.Unlock()
	c.publish(protocol.Frame{Type: "state"})
	if change.RestartRequired && !deferRestart && !c.AnyRunning() {
		// Nobody is running: apply it now instead of waiting for a submission that may be
		// hours away. A run in flight, or a card answered during one, waits for that run.
		c.applyPendingSecretRestart(ctx)
	}
	return nil
}

// applyPendingSecretRestart finishes a change the engine deferred while a run was active.
// It runs after a submission has settled every run. A substitute engine is stopped and
// started so it reads the next-start config (the worker's next tool call wakes it); an
// environment engine gets a fresh worker, whose environment is the delivery.
func (c *Control) applyPendingSecretRestart(ctx context.Context) {
	c.lifecycle.Lock()
	c.mu.RLock()
	restart := c.sandbox.SecretsRestart
	c.mu.RUnlock()
	if !restart || c.AnyRunning() {
		c.lifecycle.Unlock()
		return
	}
	manager := c.sandboxManager()
	if manager == nil {
		c.lifecycle.Unlock()
		return
	}
	applied := false
	delivery := manager.SecretDelivery()
	if delivery == sandbox.DeliverySubstitute {
		policy := c.opts.Workspace.SandboxPolicy(c.opts.DataDir, c.opts.UserHome)
		policy.Secrets = c.resolveSecrets(delivery).delivered
		if err := manager.Stop(ctx, c.opts.Workspace.Path); err != nil {
			workerpool.Log("restart sandbox for secrets: %v", err)
		} else if _, err := manager.Produce(ctx, policy, filepath.Dir(c.opts.StoragePath)); err != nil {
			workerpool.Log("restart sandbox for secrets: %v", err)
		} else {
			applied = true
			c.mu.Lock()
			c.sandbox.SecretsRestart = false
			if status, err := manager.Status(ctx, c.opts.Workspace.Path); err == nil {
				c.sandbox.Status = status
			}
			c.mu.Unlock()
		}
	} else if manager.SecretEnvInWorker() {
		if err := c.RestartWorker(ctx); err != nil {
			workerpool.Log("restart worker for secrets: %v", err)
		} else {
			applied = true
		}
	} else {
		resolved := c.resolveSecrets(delivery)
		if _, err := manager.SecretsChanged(ctx, c.opts.Workspace.Path, resolved.delivered); err != nil {
			workerpool.Log("apply secrets for %s: %v", c.opts.Workspace.Name, err)
		} else {
			applied = true
			c.mu.Lock()
			c.sandbox.SecretsRestart = false
			c.mu.Unlock()
		}
	}
	c.lifecycle.Unlock()
	if applied {
		c.resumeAfterSecrets(ctx)
	}
}

// QueueSecretContinuation remembers that a conversation's turn ended because a secret
// needs a sandbox restart. Once the restart lands, the control starts the next turn itself;
// the user does not have to.
func (c *Control) QueueSecretContinuation(conversationID, name string) {
	if conversationID == "" || name == "" {
		return
	}
	c.continuationMu.Lock()
	defer c.continuationMu.Unlock()
	for _, existing := range c.continuations[conversationID] {
		if existing == name {
			return
		}
	}
	c.continuations[conversationID] = append(c.continuations[conversationID], name)
}

// resumeAfterSecrets starts the turn the deferred restart interrupted. It is called only
// after no run is active and the sandbox/worker has the new set.
func (c *Control) resumeAfterSecrets(ctx context.Context) {
	c.continuationMu.Lock()
	pending := c.continuations
	c.continuations = map[string][]string{}
	c.continuationMu.Unlock()
	for conversationID, names := range pending {
		info, found := c.conversationInfo(conversationID)
		if !found || info.Hidden {
			continue
		}
		text := "Automatic continuation: the secret"
		if len(names) == 1 {
			text += " " + names[0] + " is"
		} else {
			text += "s " + strings.Join(names, ", ") + " are"
		}
		text += " now available to commands in the sandbox. Continue the task you were doing before you requested it. Use the environment variable"
		if len(names) == 1 {
			text += " " + names[0] + "."
		} else {
			text += "s " + strings.Join(names, ", ") + "."
		}
		if err := c.Prompt(ctx, conversationID, text, protocol.WhenBusyFollowUp, nil); err != nil {
			workerpool.Log("automatic continuation for %s: %v", conversationID, err)
		}
	}
}

// handleSecretRequest turns a tool's request into a card for the people watching. A turn
// with nobody attached is answered at once: there is no one to ask, and a tool blocked
// forever would only burn a run.
func (c *Control) handleSecretRequest(frame protocol.Frame) {
	requestID := frame.ID
	conversationID := frame.ConversationID
	if requestID == "" || conversationID == "" {
		return
	}
	c.mu.RLock()
	worker := c.worker
	c.mu.RUnlock()
	if worker == nil {
		return
	}
	if !c.watchingConversation(conversationID) {
		go c.answerSecret(worker, requestID, true, "", false, "no client is watching this conversation")
		return
	}
	c.secretMu.Lock()
	c.secretRequests[requestID] = &pendingSecret{
		requestID:      requestID,
		conversationID: conversationID,
		worker:         worker,
		name:           frame.Name,
		description:    frame.Description,
		hosts:          frame.Hosts,
	}
	c.secretMu.Unlock()
	c.publish(protocol.Frame{
		Type:           "secret",
		ConversationID: conversationID,
		Secret: &protocol.SecretEvent{
			Kind:           "request",
			RequestID:      requestID,
			ConversationID: conversationID,
			Name:           frame.Name,
			Description:    frame.Description,
			Hosts:          frame.Hosts,
		},
	})
}

// answerSecretList answers a tool that asked what the workspace has. It is a read: no card,
// no pending state, and no value in the answer.
func (c *Control) answerSecretList(frame protocol.Frame) {
	c.mu.RLock()
	worker := c.worker
	c.mu.RUnlock()
	if worker == nil {
		return
	}
	delivery := c.SecretDelivery()
	resolved := c.resolveSecrets(delivery)
	infos := make([]protocol.SecretInfo, 0, len(resolved.state))
	restarting := c.SecretsRestart()
	for _, secret := range resolved.state {
		status := secret.Status
		if restarting && status == "delivered" {
			status = "stored; waiting for the sandbox restart"
		}
		infos = append(infos, protocol.SecretInfo{
			Name:        secret.Name,
			Description: secret.Description,
			Scope:       secret.Scope,
			Hosts:       secret.Hosts,
			Delivery:    secret.Delivery,
			Status:      status,
			Placeholder: secret.Placeholder,
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = worker.CallInto(ctx, protocol.Command{
		Type:            "secret_list_answer",
		SecretRequestID: frame.ID,
		Secrets:         infos,
		Reason:          resolved.problem,
	}, nil)
}

// SecretPrompt returns an open request by id, for the answer endpoint.
func (c *Control) SecretPrompt(requestID string) (SecretPrompt, bool) {
	c.secretMu.Lock()
	defer c.secretMu.Unlock()
	pending, ok := c.secretRequests[requestID]
	if !ok {
		return SecretPrompt{}, false
	}
	return SecretPrompt{
		RequestID:      pending.requestID,
		ConversationID: pending.conversationID,
		Name:           pending.name,
		Description:    pending.description,
		Hosts:          append([]string(nil), pending.hosts...),
	}, true
}

// ResolveSecret answers the tool and closes the card. The value was already written by the
// HTTP layer; this side carries only the outcome.
func (c *Control) ResolveSecret(requestID string, cancelled bool, scope string, restart bool, reason string) bool {
	c.secretMu.Lock()
	pending, ok := c.secretRequests[requestID]
	delete(c.secretRequests, requestID)
	c.secretMu.Unlock()
	if !ok {
		return false
	}
	c.answerSecret(pending.worker, requestID, cancelled, scope, restart, reason)
	kind := "resolved"
	if cancelled {
		kind = "cancelled"
	}
	c.publish(protocol.Frame{
		Type:           "secret",
		ConversationID: pending.conversationID,
		Secret: &protocol.SecretEvent{
			Kind:            kind,
			RequestID:       requestID,
			ConversationID:  pending.conversationID,
			Name:            pending.name,
			Scope:           scope,
			RestartRequired: restart,
			Reason:          reason,
		},
	})
	return true
}

// cancelSecrets closes every open request for a conversation, or for the whole workspace
// when the conversation is empty. A run that ended, a worker that died, and an update that
// stopped the process all leave no one to answer.
func (c *Control) cancelSecrets(conversationID string) {
	c.secretMu.Lock()
	ids := []string{}
	for id, pending := range c.secretRequests {
		if conversationID == "" || pending.conversationID == conversationID {
			ids = append(ids, id)
		}
	}
	c.secretMu.Unlock()
	for _, id := range ids {
		c.ResolveSecret(id, true, "", false, "the run ended before the secret was set")
	}
}

// dropSecrets closes the cards without waiting for a worker that is gone or going. The tool
// died with the process; there is nobody to answer.
func (c *Control) dropSecrets(conversationID string) {
	c.secretMu.Lock()
	removed := []*pendingSecret{}
	for id, pending := range c.secretRequests {
		if conversationID == "" || pending.conversationID == conversationID {
			removed = append(removed, pending)
			delete(c.secretRequests, id)
		}
	}
	c.secretMu.Unlock()
	for _, pending := range removed {
		c.publish(protocol.Frame{
			Type:           "secret",
			ConversationID: pending.conversationID,
			Secret: &protocol.SecretEvent{
				Kind:           "cancelled",
				RequestID:      pending.requestID,
				ConversationID: pending.conversationID,
				Name:           pending.name,
				Reason:         "the harness restarted before the secret was set",
			},
		})
	}
}

func (c *Control) answerSecret(worker *workerpool.Worker, requestID string, cancelled bool, scope string, restart bool, reason string) {
	if worker == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = worker.CallInto(ctx, protocol.Command{
		Type:            "secret_answer",
		SecretRequestID: requestID,
		Cancelled:       cancelled,
		SecretScope:     scope,
		RestartRequired: restart,
		Reason:          reason,
	}, nil)
}

// SecretsRestart reports whether the last applied change waits for the next sandbox start.
func (c *Control) SecretsRestart() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sandbox.SecretsRestart
}
