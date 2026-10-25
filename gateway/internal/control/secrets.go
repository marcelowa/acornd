// the vault: what an engine can do with a secret, and the prompts a turn waits on
//
// Part of the control package. The file is named after the job, not the layer: this is the
// one thing it does, and everything it needs from its siblings is a call, not a comment.
package control

import (
	"context"
	"fmt"
	"hash/fnv"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
	"github.com/acornfall/acornfall/gateway/internal/sandbox"
	"github.com/acornfall/acornfall/gateway/internal/secretsproxy"
	"github.com/acornfall/acornfall/gateway/internal/workerpool"
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
	worker         workerConn
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

// startSecretsProxy makes sure this workspace has a proxy for its secrets, and answers nil when it
// has none to serve: an engine that substitutes nothing, or a vault that resolved to nothing. A
// proxy that is already listening is given the new grants instead of a second listener, which is
// what makes a rotation live rather than a restart.
func (c *Control) startSecretsProxy(delivery sandbox.Delivery, delivered []sandbox.Secret) (*secretsproxy.Proxy, error) {
	if delivery != sandbox.DeliverySubstitute || len(delivered) == 0 {
		return nil, nil
	}
	grants := grantsFor(delivered)
	c.mu.Lock()
	running := c.proxy
	c.mu.Unlock()
	if running != nil {
		running.SetGrants(grants)
		return running, nil
	}
	dir := filepath.Join(c.opts.DataDir, "proxy")
	name := c.opts.Workspace.Name
	proxy, err := secretsproxy.New(dir, secretsproxy.Config{
		Grants: grants,
		Addr:   fmt.Sprintf("127.0.0.1:%d", proxyPort(c.opts.Workspace.Path)),
		Logf: func(format string, args ...any) {
			workerpool.Log("secrets %s: "+format, append([]any{name}, args...)...)
		},
	})
	if err != nil {
		return nil, err
	}
	// Its own context, not the caller's: the caller is one request, and a proxy that stopped with
	// it would leave the sandbox's forward pointing at a closed port (G1).
	serve, stop := context.WithCancel(context.Background())
	go func() {
		// A proxy that stops is the control's own shutdown, not a failure to report here.
		_ = proxy.Serve(serve)
	}()
	c.mu.Lock()
	c.proxy, c.proxyStop = proxy, stop
	c.mu.Unlock()
	return proxy, nil
}

// secretProxyPolicy is what the engine needs to carry the proxy: the URL a command puts in
// HTTPS_PROXY, and the CA a client inside has to trust.
func (c *Control) secretProxyPolicy(proxy *secretsproxy.Proxy) *sandbox.SecretProxy {
	credential := url.UserPassword("acornfall", proxy.Token())
	return &sandbox.SecretProxy{
		URL:    "http://" + credential.String() + "@" + proxy.Addr(),
		CAFile: secretsproxy.CAPath(filepath.Join(c.opts.DataDir, "proxy")),
	}
}

// grantsFor is the vault's resolved set in the shape the proxy decides with. The placeholder is
// the same literal a microVM workspace shows, so the agent is told one story about a secret.
func grantsFor(delivered []sandbox.Secret) []secretsproxy.Grant {
	grants := make([]secretsproxy.Grant, 0, len(delivered))
	for _, secret := range delivered {
		grants = append(grants, secretsproxy.Grant{
			Name:        secret.Name,
			Value:       secret.Value,
			Placeholder: secretPlaceholder(sandbox.DeliverySubstitute, secret.Name),
			Hosts:       secret.Hosts,
			Headers:     secret.Headers,
			Query:       secret.Query,
			Body:        secret.Body,
		})
	}
	return grants
}

// proxyPort is where a workspace's proxy listens. Derived from the workspace rather than picked,
// because pasta's forward is baked into a sandbox's argv: a gateway that restarts must bind the
// port the sandbox it adopts was booted with, or that workspace's secrets are dead until it is
// recreated.
func proxyPort(workspace string) int {
	sum := fnv.New32a()
	_, _ = sum.Write([]byte(workspace))
	return 20000 + int(sum.Sum32()%40000)
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
	manager, resolved, delivery, ok, err := c.secretsTarget()
	if err != nil || !ok {
		return err
	}
	if resolved.problem != "" {
		c.recordSecrets(resolved.state, resolved.problem, nil)
		return nil
	}
	if delivery == sandbox.DeliveryEnvironment && manager.SecretEnvInWorker() {
		return c.applyWorkerSecrets(ctx, resolved, deferRestart)
	}
	if delivery == sandbox.DeliverySubstitute && manager.SecretProxyInControl() {
		return c.applyProxySecrets(resolved)
	}
	return c.applyEngineSecrets(ctx, manager, resolved, deferRestart)
}

// applyEngineSecrets is the answer for an engine whose own surface takes a change: ask it, record
// what it said, and apply a deferred restart now when nobody is running.
func (c *Control) applyEngineSecrets(ctx context.Context, manager *sandbox.Manager, resolved sandboxSecrets, deferRestart bool) error {
	change, err := manager.SecretsChanged(ctx, c.opts.Workspace.Path, resolved.delivered)
	if err != nil {
		return err
	}
	c.recordSecrets(resolved.state, "", &change.RestartRequired)
	if change.RestartRequired && !deferRestart && !c.AnyRunning() {
		// Nobody is running: apply it now instead of waiting for a submission that may be
		// hours away. A run in flight, or a card answered during one, waits for that run.
		c.applyPendingSecretRestart(ctx)
	}
	return nil
}

// applyProxySecrets is the proxy engine's answer: the grants are swapped under a running proxy, so a
// rotation is live and a command that is running is not interrupted for it. A proxy that cannot
// start is a change that never landed, and the person who saved the value hears about it.
func (c *Control) applyProxySecrets(resolved sandboxSecrets) error {
	if _, err := c.startSecretsProxy(sandbox.DeliverySubstitute, resolved.delivered); err != nil {
		c.recordSecrets(resolved.state, err.Error(), nil)
		return err
	}
	return nil
}

// applyWorkerSecrets is the environment engine's answer: a value in the worker's own process
// cannot change under a running turn, so it either restarts now or says a restart is owed.
func (c *Control) applyWorkerSecrets(ctx context.Context, resolved sandboxSecrets, deferRestart bool) error {
	if !deferRestart && !c.AnyRunning() {
		return c.RestartWorker(ctx)
	}
	restart := true
	c.recordSecrets(resolved.state, "", &restart)
	return nil
}

// secretsTarget is what a change applies to. Not ok means there is nothing to apply it to — no
// worker, or no engine — which is a state and not a failure. An err means the engine is there
// and cannot be used.
func (c *Control) secretsTarget() (*sandbox.Manager, sandboxSecrets, sandbox.Delivery, bool, error) {
	if !c.WorkerUp() {
		return nil, sandboxSecrets{}, sandbox.DeliveryEnvironment, false, nil
	}
	manager := c.sandboxManager()
	if manager == nil {
		return nil, sandboxSecrets{}, sandbox.DeliveryEnvironment, false, nil
	}
	if problem := manager.Problem(); problem != nil {
		return nil, sandboxSecrets{}, sandbox.DeliveryEnvironment, false, problem
	}
	delivery := manager.SecretDelivery()
	return manager, c.resolveSecrets(delivery), delivery, true, nil
}

// recordSecrets writes what the vault resolved and tells whoever is watching. A nil restart
// leaves the pending flag alone: a resolution problem says nothing about one, and clearing it
// would cancel a restart that is still owed.
func (c *Control) recordSecrets(secrets []SandboxSecret, problem string, restart *bool) {
	c.mu.Lock()
	c.sandbox.Secrets = secrets
	c.sandbox.SecretsProblem = problem
	if restart != nil {
		c.sandbox.SecretsRestart = *restart
	}
	c.mu.Unlock()
	c.publish(protocol.Frame{Type: "state"})
}

// applyPendingSecretRestart finishes a change the engine deferred while a run was active.
// It runs after a submission has settled every run. A substitute engine is stopped and
// started so it reads the next-start config (the worker's next tool call wakes it); an
// environment engine gets a fresh worker, whose environment is the delivery.
func (c *Control) applyPendingSecretRestart(ctx context.Context) {
	c.lifecycle.Lock()
	if !c.restartPending() {
		c.lifecycle.Unlock()
		return
	}
	manager := c.sandboxManager()
	if manager == nil {
		c.lifecycle.Unlock()
		return
	}
	applied := c.applyRestart(ctx, manager)
	c.lifecycle.Unlock()
	if applied {
		c.resumeAfterSecrets(ctx)
	}
}

// restartPending is whether a change is waiting, and nothing is running that has to finish
// first.
func (c *Control) restartPending() bool {
	c.mu.RLock()
	restart := c.sandbox.SecretsRestart
	c.mu.RUnlock()
	return restart && !c.AnyRunning()
}

// applyRestart does the kind of restart this engine needs. Three deliveries, three answers:
// a substitute engine rereads its config on the next start, an environment engine needs a
// new worker, and everything else applies in place.
func (c *Control) applyRestart(ctx context.Context, manager *sandbox.Manager) bool {
	delivery := manager.SecretDelivery()
	switch {
	case delivery == sandbox.DeliverySubstitute:
		return c.restartSandboxForSecrets(ctx, manager, delivery)
	case manager.SecretEnvInWorker():
		return c.restartWorkerForSecrets(ctx)
	default:
		return c.reapplySecrets(ctx, manager, delivery)
	}
}

func (c *Control) restartSandboxForSecrets(ctx context.Context, manager *sandbox.Manager, delivery sandbox.Delivery) bool {
	policy := c.opts.Workspace.SandboxPolicy(c.opts.DataDir, c.opts.UserHome)
	policy.Secrets = c.resolveSecrets(delivery).delivered
	if err := manager.Stop(ctx, c.opts.Workspace.Path); err != nil {
		workerpool.Log("restart sandbox for secrets: %v", err)
		return false
	}
	if _, err := manager.Produce(ctx, policy, filepath.Dir(c.opts.StoragePath)); err != nil {
		workerpool.Log("restart sandbox for secrets: %v", err)
		return false
	}
	c.mu.Lock()
	c.sandbox.SecretsRestart = false
	if status, err := manager.Status(ctx, c.opts.Workspace.Path); err == nil {
		c.sandbox.Status = status
	}
	c.mu.Unlock()
	return true
}

func (c *Control) restartWorkerForSecrets(ctx context.Context) bool {
	if err := c.RestartWorker(ctx); err != nil {
		workerpool.Log("restart worker for secrets: %v", err)
		return false
	}
	// The flag stays set: a fresh worker reads the vault on its way up, and that is what
	// clears it.
	return true
}

func (c *Control) reapplySecrets(ctx context.Context, manager *sandbox.Manager, delivery sandbox.Delivery) bool {
	resolved := c.resolveSecrets(delivery)
	if _, err := manager.SecretsChanged(ctx, c.opts.Workspace.Path, resolved.delivered); err != nil {
		workerpool.Log("apply secrets for %s: %v", c.opts.Workspace.Name, err)
		return false
	}
	c.mu.Lock()
	c.sandbox.SecretsRestart = false
	c.mu.Unlock()
	return true
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
	if slices.Contains(c.continuations[conversationID], name) {
		return
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

func (c *Control) answerSecret(worker workerConn, requestID string, cancelled bool, scope string, restart bool, reason string) {
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
