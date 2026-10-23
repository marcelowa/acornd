// model and agent resolution: the defaults chain, pinning, and thinking levels
//
// Part of the control package. The file is named after the job, not the layer: this is the
// one thing it does, and everything it needs from its siblings is a call, not a comment.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
	"github.com/acornfall/acornfall/gateway/internal/workerpool"
)

// Defaults are one layer's answer to "what runs when nothing more specific does". The
// workspace keeps its own, the install keeps its own, and neither is a copy of the other.
type Defaults struct {
	Model    protocol.ModelBinding `json:"defaultModel,omitzero"`
	Thinking string                `json:"defaultThinking,omitempty"`
	Agent    string                `json:"defaultAgent,omitempty"`
}

// ScopeDefaults is one layer's own defaults, before any resolution. Empty means this layer
// says nothing, which is different from it saying what the layer below says.
type ScopeDefaults struct {
	Model    protocol.ModelBinding `json:"model,omitzero"`
	Thinking string                `json:"thinking,omitempty"`
	Agent    string                `json:"agent,omitempty"`
}

// DefaultOrigins names the layer each resolved default came from: which one is in force,
// not just what it is. A value that came from the install is not a value this workspace
// chose, and the difference is what someone looks at this for.
type DefaultOrigins struct {
	Model    string `json:"model,omitempty"`
	Thinking string `json:"thinking,omitempty"`
	Agent    string `json:"agent,omitempty"`
}

// pinBinding is what a conversation would be pinned to: the effective model and the level
// that model can actually express, plus the default agent when one is named and known. It
// reads and decides; nothing here changes anything.
//
// Nothing resolved means nothing to pin: a workspace with no provider keeps inheriting until
// there is something to inherit. A subagent is configured by whatever created it, so pinning
// it here would overwrite that with the defaults.
// GeneralAgent is the agent a conversation runs when nothing named one. The worker ships it as
// a built-in definition, so it can be pinned by name like any other — and an install that
// wants its own general writes `general.md`, which shadows it.
const GeneralAgent = "general"

func (c *Control) pinBinding(conversationID string) (protocol.ModelBinding, bool) {

	if c.isSubagent(conversationID) {
		return protocol.ModelBinding{}, false
	}
	inputs := c.inputsFor(conversationID)
	effective := inputs.effective()
	if effective.Provider == "" || effective.ModelID == "" {
		return protocol.ModelBinding{}, false
	}
	binding := protocol.ModelBinding{
		Provider:      effective.Provider,
		ModelID:       effective.ModelID,
		ThinkingLevel: effective.ThinkingLevel,
	}
	// The agent is pinned by name too: the configured default, or the general one every
	// install has. A definition that does not exist is not pinned — it would fail every open,
	// and the interface has to show a stale default rather than a broken conversation.
	agent := inputs.defaultAgent
	if agent == "" {
		agent = GeneralAgent
	}
	if c.hasAgentDefinition(agent) {
		binding.Definition = agent
	}
	return binding, true
}

// pinOne pins a single conversation, for the one that was just created.
func (c *Control) pinOne(conversationID string) bool {
	binding, ok := c.pinBinding(conversationID)
	if !ok {
		return false
	}
	c.mu.Lock()
	c.bindings[conversationID] = binding
	c.mu.Unlock()
	c.persist()
	return true
}

// pinMissing is the migration for conversations that predate pinning, and it has two parts.
//
// The conversations that never chose are pinned from what resolves at that moment.
// Conversations with a binding are left alone — that is a choice — and so are subagents.
//
// Then, once, the conversations whose binding never named an agent are given the general one.
// Pinning the name is what lets the interface show which agent runs; a conversation that
// clears its definition afterwards is asking to follow the default, and that has to keep
// working, which is why this part happens once rather than on every listing.
func (c *Control) pinMissing(conversations []protocol.ConversationInfo) {
	changed := c.pinGeneralDefinition(conversations)
	if c.pinUnsetBindings(conversations) {
		changed = true
	}
	if changed {
		c.persist()
	}
}

// pinGeneralDefinition gives, once, the general agent to every conversation whose binding
// never named one. It is once because a conversation that clears its definition afterwards is
// asking to follow the default, and that has to keep working.
func (c *Control) pinGeneralDefinition(conversations []protocol.ConversationInfo) bool {
	c.mu.RLock()
	named := c.generalPinned
	c.mu.RUnlock()
	if named || !c.hasAgentDefinition(GeneralAgent) {
		return false
	}
	for _, conversation := range conversations {
		if conversation.Parent != "" {
			continue
		}
		c.mu.RLock()
		existing := c.bindings[conversation.ID]
		c.mu.RUnlock()
		chose := existing.Provider != "" || existing.ModelID != "" || existing.Definition != ""
		if !chose || existing.Definition != "" {
			continue
		}
		existing.Definition = GeneralAgent
		c.mu.Lock()
		c.bindings[conversation.ID] = existing
		c.mu.Unlock()
	}
	c.mu.Lock()
	c.generalPinned = true
	c.mu.Unlock()
	return true
}

// pinUnsetBindings pins the conversations that never chose, from what resolves at that
// moment. A conversation with a binding made a choice and is left alone, and so is a subagent.
func (c *Control) pinUnsetBindings(conversations []protocol.ConversationInfo) bool {
	changed := false
	for _, conversation := range conversations {
		c.mu.RLock()
		existing := c.bindings[conversation.ID]
		c.mu.RUnlock()
		if existing.Provider != "" || existing.ModelID != "" || existing.Definition != "" {
			continue
		}
		binding, ok := c.pinBinding(conversation.ID)
		if !ok {
			continue
		}
		c.mu.Lock()
		c.bindings[conversation.ID] = binding
		c.mu.Unlock()
		changed = true
	}
	return changed
}

// hasAgentDefinition shares definitionExists' leniency: an empty catalog means the
// worker has not described itself yet, not that no definition exists.
func (c *Control) hasAgentDefinition(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return definitionExists(c.catalog.Agents, name)
}

// AgentChange is a partial update. A nil field means "leave it alone" and an
// empty string means "clear it", which a plain struct could not tell apart.
type AgentChange struct {
	Provider      *string
	ModelID       *string
	ThinkingLevel *string
	Definition    *string
}

// SetAgent applies a change to one conversation. Only fields the caller sets
// become explicit: anything left alone keeps inheriting.
func (c *Control) SetAgent(ctx context.Context, conversationID string, change AgentChange) error {
	c.mu.RLock()
	explicit := c.bindings[conversationID]
	c.mu.RUnlock()

	var err error
	if explicit, err = c.modelChoice(explicit, change); err != nil {
		return err
	}
	if explicit, err = c.definitionChoice(explicit, change); err != nil {
		return err
	}
	if explicit, err = c.thinkingChoice(explicit, change); err != nil {
		return err
	}

	effective, err := c.effective(conversationID, explicit)
	if err != nil {
		return err
	}
	var response struct {
		Agent protocol.ResolvedAgent `json:"agent"`
	}
	if err := c.worker.CallInto(ctx, protocol.Command{
		Type:           "open",
		ConversationID: conversationID,
		Agent:          &effective,
	}, &response); err != nil {
		return err
	}

	c.mu.Lock()
	c.bindings[conversationID] = explicit
	resolved := response.Agent
	c.resolved[conversationID] = &resolved
	c.mu.Unlock()
	c.persist()
	return nil
}

// modelChoice folds a provider and model change into the explicit binding. Both are given
// together or neither, and a model the credentials cannot reach is refused rather than stored:
// bound to one, the conversation fails on its next prompt, where the reason for the choice is
// gone. The picker only offers models of authenticated providers, so this refuses what the
// interface cannot ask for.
func (c *Control) modelChoice(explicit protocol.ModelBinding, change AgentChange) (protocol.ModelBinding, error) {
	if change.Provider == nil && change.ModelID == nil {
		return explicit, nil
	}
	provider, modelID := explicit.Provider, explicit.ModelID
	if change.Provider != nil {
		provider = *change.Provider
	}
	if change.ModelID != nil {
		modelID = *change.ModelID
	}
	if (provider == "") != (modelID == "") {
		return explicit, errors.New("provider and modelId must be given together")
	}
	if provider != "" && !c.isAvailable(c.Describe(), protocol.ModelBinding{Provider: provider, ModelID: modelID}) {
		return explicit, unavailableModel(provider, modelID)
	}
	explicit.Provider, explicit.ModelID = provider, modelID
	return explicit, nil
}

// definitionChoice is strict, unlike resolution: choosing a definition is a request that it
// exist, and the catalog here is what the worker reported. Resolution stays lenient because it
// also runs before the worker has described itself.
func (c *Control) definitionChoice(explicit protocol.ModelBinding, change AgentChange) (protocol.ModelBinding, error) {
	if change.Definition == nil {
		return explicit, nil
	}
	if *change.Definition == "" {
		explicit.Definition = ""
		return explicit, nil
	}
	c.mu.RLock()
	agents := c.catalog.Agents
	c.mu.RUnlock()
	if !agentExists(agents, *change.Definition) {
		return explicit, fmt.Errorf("no agent definition named %q", *change.Definition)
	}
	explicit.Definition = *change.Definition
	return explicit, nil
}

func (c *Control) thinkingChoice(explicit protocol.ModelBinding, change AgentChange) (protocol.ModelBinding, error) {
	if change.ThinkingLevel == nil {
		return explicit, nil
	}
	if err := validateThinkingLevel(*change.ThinkingLevel); err != nil {
		return explicit, err
	}
	explicit.ThinkingLevel = *change.ThinkingLevel
	return explicit, nil
}

// agentExists is the strict answer, and deliberately not definitionExists: an empty catalog
// means the worker has not described itself yet, which resolution forgives and a choice does not.
func agentExists(agents []protocol.AgentSummary, name string) bool {
	for _, agent := range agents {
		if agent.Name == name {
			return true
		}
	}
	return false
}

// effective resolves what a conversation actually runs: an explicit choice, then
// the definition's model, then the install default. Control decides this rather
// than leaving it to the worker, so "is this model inherited?" has one answer.
// resolveInputs is everything the resolution needs, read under one lock so the
// resolver itself never takes one. Nested read locks are not re-entrant here.
type resolveInputs struct {
	explicit        protocol.ModelBinding
	fallbackModel   protocol.ModelBinding
	defaultThinking string
	defaultAgent    string
	resolved        *protocol.ResolvedAgent
	levels          []string
}

// effectiveModel resolves the model alone: what this conversation chose, then what
// its definition names, then the install default.
func (in resolveInputs) effectiveModel() (string, string) {
	if in.explicit.Provider != "" && in.explicit.ModelID != "" {
		return in.explicit.Provider, in.explicit.ModelID
	}
	if in.resolved != nil {
		if binding, ok := parseModelBinding(in.resolved.Model); ok {
			return binding.Provider, binding.ModelID
		}
	}
	return in.fallbackModel.Provider, in.fallbackModel.ModelID
}

// effective folds the three fields, each with the same order: what this
// conversation chose, then what its definition says, then the install default. A
// definition beating the install default is deliberate — a choice made one layer
// down should not be overridden by a global one.
func (in resolveInputs) effective() protocol.ModelBinding {
	effective := in.explicit
	effective.Provider, effective.ModelID = in.effectiveModel()

	// The whole chain, not a pre-merged value: a level the model cannot express
	// must be able to fall through to the next layer rather than being clamped
	// in place, which is what "fall back to the default" means.
	definitionThinking := ""
	if in.resolved != nil {
		definitionThinking = in.resolved.Thinking
	}
	effective.ThinkingLevel = resolveThinking(
		in.levels,
		effective.ThinkingLevel,
		definitionThinking,
		in.defaultThinking,
	)

	// Record what was asked for when the answer differs, so the UI can say why
	// rather than appearing to have ignored the choice.
	requested := effective.ThinkingRequested
	if requested == "" {
		for _, candidate := range []string{in.explicit.ThinkingLevel, definitionThinking, in.defaultThinking} {
			if candidate != "" {
				requested = candidate
				break
			}
		}
	}
	effective.ThinkingRequested = ""
	if requested != "" && requested != effective.ThinkingLevel {
		effective.ThinkingRequested = requested
	}

	if effective.Definition == "" {
		effective.Definition = in.defaultAgent
	}
	// And the general agent is what runs when nothing named one. Naming it here is what lets
	// the interface show an agent for every conversation, including a subagent's: that one's
	// configuration is copied from its creator and never re-applied, so there is nothing
	// else that would ever fill the name in.
	if effective.Definition == "" {
		effective.Definition = GeneralAgent
	}
	return effective
}

// origins names the layers, in the words the settings sheet uses.
const (
	originWorkspace  = "workspace"
	originInstall    = "install"
	originLastResort = "last-resort"
)

// effectiveDefaults resolves what applies to a conversation that has chosen nothing: this
// workspace's own default, then the install's, then what is left when neither has one.
//
// A layer falls through when its choice is not usable — a model the credentials no longer
// offer, an agent definition that has gone — instead of being overwritten, because a choice
// that is remembered applies again when the provider or the file comes back.
func (c *Control) effectiveDefaults() (Defaults, DefaultOrigins) {
	// Read under one lock, decide outside it: isAvailable and the agent catalog are read
	// from the same snapshot, and nothing here blocks.
	c.mu.RLock()
	describe := c.describe
	workspace := Defaults{Model: c.defaultModel, Thinking: c.defaultThinking, Agent: c.defaultAgent}
	install := c.install
	lastResort := c.lastResort
	agents := append([]protocol.AgentSummary(nil), c.catalog.Agents...)
	c.mu.RUnlock()

	resolved := Defaults{}
	origins := DefaultOrigins{}
	switch {
	case c.modelUsable(describe, workspace.Model):
		resolved.Model, origins.Model = workspace.Model, originWorkspace
	case c.modelUsable(describe, install.Model):
		resolved.Model, origins.Model = install.Model, originInstall
	case lastResort.Provider != "" && lastResort.ModelID != "":
		resolved.Model, origins.Model = lastResort, originLastResort
	}
	switch {
	case workspace.Thinking != "":
		resolved.Thinking, origins.Thinking = workspace.Thinking, originWorkspace
	case install.Thinking != "":
		resolved.Thinking, origins.Thinking = install.Thinking, originInstall
	}
	// A definition that is gone is not usable: pointing every inheriting conversation at it
	// would fail them all, where the layer below still works.
	for _, candidate := range []struct {
		name   string
		origin string
	}{{workspace.Agent, originWorkspace}, {install.Agent, originInstall}} {
		if candidate.name == "" {
			continue
		}
		if !definitionExists(agents, candidate.name) {
			continue
		}
		resolved.Agent, origins.Agent = candidate.name, candidate.origin
		break
	}
	return resolved, origins
}

// modelUsable reports whether a layer's model can run here. An empty catalog means the
// worker has not described itself yet, which is not the same as "not available": treating it
// that way would resolve a different model for the moment before the worker answers.
func (c *Control) modelUsable(describe protocol.Describe, binding protocol.ModelBinding) bool {
	if binding.Provider == "" || binding.ModelID == "" {
		return false
	}
	if len(describe.Providers) == 0 {
		return true
	}
	return c.isAvailable(describe, binding)
}

func definitionExists(agents []protocol.AgentSummary, name string) bool {
	if len(agents) == 0 {
		return true
	}
	for _, agent := range agents {
		if agent.Name == name {
			return true
		}
	}
	return false
}

func (c *Control) inputsFor(conversationID string) resolveInputs {
	// Resolved first, because it takes its own lock and nested read locks are not
	// re-entrant here.
	fallback, _ := c.effectiveDefaults()
	c.mu.RLock()
	inputs := resolveInputs{
		explicit:        c.bindings[conversationID],
		fallbackModel:   fallback.Model,
		defaultThinking: fallback.Thinking,
		defaultAgent:    fallback.Agent,
		resolved:        c.resolved[conversationID],
	}
	c.mu.RUnlock()
	// Clamp against the model that will actually run: an inheriting conversation
	// has no explicit model, which is exactly when clamping matters most.
	provider, modelID := inputs.effectiveModel()
	inputs.levels = c.supportedThinking(provider, modelID)
	return inputs
}

func (c *Control) effective(conversationID string, explicit protocol.ModelBinding) (protocol.ModelBinding, error) {
	inputs := c.inputsFor(conversationID)
	inputs.explicit = explicit
	effective := inputs.effective()
	if effective.Provider == "" || effective.ModelID == "" {
		return effective, fmt.Errorf("%w: connect a provider or set a default model", ErrNoProvider)
	}
	return effective, nil
}

// thinkingLevels is the ordered scale, weakest first.
var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// resolveThinking picks the level to run, given an ordered list of preferences:
// what this conversation chose, then what its definition says, then the install
// default.
//
//  1. the first preference the model supports;
//  2. otherwise the closest supported level below the most specific preference;
//  3. otherwise the lowest level the model offers.
//
// Step 3 exists because "below" can be empty: a model whose only level is `high`
// cannot honour a request for `off`, and the honest answer is the level it has,
// not a setting that silently does nothing. An empty result means no preference
// was expressed, and the model's own default stands.
func resolveThinking(levels []string, preferred ...string) string {
	reference := firstPreference(preferred)
	if reference == "" {
		return ""
	}
	if len(levels) == 0 {
		// An unknown model has no declared levels, so nothing to resolve against.
		return reference
	}
	supported := levelSet(levels)
	for _, candidate := range preferred {
		if candidate != "" && supported[candidate] {
			return candidate
		}
	}
	// Nothing preferred is available: the closest level below the most specific
	// preference, then the lowest the model has.
	if closest := closestBelow(reference, supported); closest != "" {
		return closest
	}
	return lowestThinking(levels)
}

// firstPreference is the most specific level anyone expressed: the conversation's, then the
// definition's, then the install default.
func firstPreference(preferred []string) string {
	for _, candidate := range preferred {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func levelSet(levels []string) map[string]bool {
	supported := make(map[string]bool, len(levels))
	for _, level := range levels {
		supported[level] = true
	}
	return supported
}

// closestBelow is the most specific level the model has, below the one asked for. Empty means
// the model offers nothing lower, and the caller falls back to its lowest.
func closestBelow(reference string, supported map[string]bool) string {
	best := ""
	for _, level := range thinkingLevels {
		if level == reference {
			break
		}
		if supported[level] {
			best = level
		}
	}
	return best
}

// lowestThinking returns the weakest level in a set, ordered by the scale rather
// than by the order the provider happened to list.
func lowestThinking(levels []string) string {
	lowest := ""
	lowestRank := len(thinkingLevels) + 1
	for _, level := range levels {
		rank := len(thinkingLevels)
		for index, known := range thinkingLevels {
			if known == level {
				rank = index
				break
			}
		}
		if rank < lowestRank {
			lowest, lowestRank = level, rank
		}
	}
	return lowest
}

// supportedThinking reads the levels a model declares, from the worker's catalog.
func (c *Control) supportedThinking(provider, modelID string) []string {
	if provider == "" || modelID == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, info := range c.providers {
		if info.ID != provider {
			continue
		}
		for _, model := range info.Models {
			if model.ID == modelID {
				return model.ThinkingLevels
			}
		}
	}
	return nil
}

// SetDefaultThinking changes the level used when neither the conversation nor its
// definition names one. It is validated against the scale, not against one model:
// control clamps per model when it applies.
func (c *Control) SetDefaultThinking(ctx context.Context, level string) error {
	if err := validateThinkingLevel(level); err != nil {
		return err
	}
	c.mu.Lock()
	c.defaultThinking = level
	c.mu.Unlock()
	return c.reapplyInheriting(ctx)
}

// SetDefaultAgent changes which definition conversations run when they have none
// of their own. An empty name means the layered defaults only.
func (c *Control) SetDefaultAgent(ctx context.Context, name string) error {
	if name != "" {
		c.mu.RLock()
		found := false
		for _, agent := range c.catalog.Agents {
			if agent.Name == name {
				found = true
				break
			}
		}
		c.mu.RUnlock()
		if !found {
			return fmt.Errorf("no agent definition named %q", name)
		}
	}
	c.mu.Lock()
	c.defaultAgent = name
	c.mu.Unlock()
	return c.reapplyInheriting(ctx)
}

// reapplyInheriting re-applies every conversation that still follows the defaults,
// so a change to them is visible without a restart. Conversations that chose keep
// their choice.
func (c *Control) reapplyInheriting(ctx context.Context) error {
	c.mu.RLock()
	ids := make([]string, 0, len(c.conversations))
	for _, conversation := range c.conversations {
		ids = append(ids, conversation.ID)
	}
	c.mu.RUnlock()
	var firstErr error
	for _, id := range ids {
		if err := c.reapply(ctx, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	c.persist()
	c.publish(protocol.Frame{Type: "state"})
	return firstErr
}

// loadInstallDefaults reads the install-wide layer.
func (c *Control) loadInstallDefaults() {
	install, err := LoadDefaults(filepath.Join(c.opts.DataDir, "settings.json"))
	if err != nil {
		workerpool.Log("install defaults unreadable, using this workspace's: %v", err)
		return
	}
	c.mu.Lock()
	c.install = install
	c.mu.Unlock()
}

// SetInstallDefaults replaces the install-wide layer, which is what every workspace falls
// back to when it has no default of its own.
func (c *Control) SetInstallDefaults(ctx context.Context, defaults Defaults) error {
	c.mu.Lock()
	c.install = defaults
	c.mu.Unlock()
	return c.reapplyInheriting(ctx)
}

// LoadDefaults reads a defaults file. A missing one is an empty layer, not an error: most
// installs never set one, and the layers below still resolve.
func LoadDefaults(path string) (Defaults, error) {
	var defaults Defaults
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaults, nil
	}
	if err != nil {
		return defaults, err
	}
	if err := json.Unmarshal(payload, &defaults); err != nil {
		return Defaults{}, fmt.Errorf("%s: %w", path, err)
	}
	return defaults, nil
}

// SaveDefaults writes a defaults file, atomically and privately.
func SaveDefaults(path string, defaults Defaults) error {
	payload, err := json.MarshalIndent(defaults, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(payload, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// SetDefaultModel changes what conversations without an explicit model run.
func (c *Control) SetDefaultModel(ctx context.Context, provider, modelID string) error {
	if (provider == "") != (modelID == "") {
		return errors.New("a model default needs both a provider and a model id")
	}
	if provider == "" {
		// Clearing this workspace's choice is not the same as having none: it falls back to
		// the install's default or the last resort instead of being left unusable.
		c.mu.Lock()
		c.defaultModel = protocol.ModelBinding{}
		c.mu.Unlock()
		return c.reapplyInheriting(ctx)
	}
	if !c.isAvailable(c.Describe(), protocol.ModelBinding{Provider: provider, ModelID: modelID}) {
		return unavailableModel(provider, modelID)
	}
	c.mu.Lock()
	c.defaultModel = protocol.ModelBinding{Provider: provider, ModelID: modelID}
	c.mu.Unlock()
	return c.reapplyInheriting(ctx)
}

// ensureLastResort works out what runs when no layer chose a model, so the first prompt
// always has something. It writes nothing: see the field it fills.
func (c *Control) ensureLastResort() error {
	binding, err := c.deriveLastResort(c.Describe())
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.lastResort = binding
	c.mu.Unlock()
	return nil
}

// parseModelBinding reads the "provider/modelId" form the overlay, the stored configuration,
// and ACORNFALL_MODEL all share.
func parseModelBinding(value string) (protocol.ModelBinding, bool) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return protocol.ModelBinding{}, false
	}
	return protocol.ModelBinding{Provider: parts[0], ModelID: parts[1]}, true
}

// validateThinkingLevel refuses a name outside the scale. Control clamps to what a model
// supports when it applies, so this is only about a level that could never mean anything.
func validateThinkingLevel(level string) error {
	if level == "" || slices.Contains(thinkingLevels, level) {
		return nil
	}
	return fmt.Errorf("unknown thinking level %q; expected one of %s", level, strings.Join(thinkingLevels, ", "))
}

// unavailableModel is the one refusal for a model the credentials do not reach, so the three
// places that refuse one say the same thing.
func unavailableModel(provider, modelID string) error {
	return fmt.Errorf("%s/%s is not available with the current credentials", provider, modelID)
}

func (c *Control) reapply(ctx context.Context, conversationID string) error {
	c.mu.RLock()
	explicit := c.bindings[conversationID]
	c.mu.RUnlock()
	effective, err := c.effective(conversationID, explicit)
	if err != nil {
		return err
	}
	var response struct {
		Agent protocol.ResolvedAgent `json:"agent"`
	}
	if err := c.worker.CallInto(ctx, protocol.Command{
		Type:           "open",
		ConversationID: conversationID,
		Agent:          &effective,
	}, &response); err != nil {
		return err
	}
	c.mu.Lock()
	resolved := response.Agent
	c.resolved[conversationID] = &resolved
	c.mu.Unlock()
	return nil
}

// Agents returns the overlay catalog: every definition and what it resolves to.
func (c *Control) Agents(ctx context.Context) (protocol.AgentsData, error) {
	var data protocol.AgentsData
	if err := c.worker.CallInto(ctx, protocol.Command{Type: "agents"}, &data); err != nil {
		return protocol.AgentsData{}, err
	}
	c.mu.Lock()
	c.catalog = data
	c.mu.Unlock()
	return data, nil
}

// Reload re-reads definitions and config from disk, then re-applies every
// conversation's binding so an edited file takes effect without a restart.
func (c *Control) Reload(ctx context.Context) error {
	if _, err := c.worker.Call(ctx, protocol.Command{Type: "reload"}); err != nil {
		return err
	}
	if _, err := c.Agents(ctx); err != nil {
		return err
	}
	// A definition may have been renamed or removed. Resolution skips one that is gone and
	// falls through to the layer below, so nothing is erased here: the choice survives and
	// applies again if the file comes back.
	c.mu.RLock()
	gone := c.defaultAgent != "" && !definitionExists(c.catalog.Agents, c.defaultAgent)
	c.mu.RUnlock()
	if gone {
		workerpool.Log("default agent %q is gone; using the next layer down", c.defaultAgent)
	}
	return c.reapplyInheriting(ctx)
}

// lastResortModels are tried in order when no layer has chosen a model, so an install with
// credentials always runs something deliberate rather than whatever is listed first. It is a
// preference, not a rule: a model nobody offers is skipped.
var lastResortModels = []string{"deepseek-v4.1-flash"}

// deriveLastResort picks what runs when no layer chose a model: ACORNFALL_MODEL, then the
// preferred models, then the first authenticated provider's first model. Only the flag and
// the preference are decisions; the end of the line is a guess, and it is reported as the
// last resort rather than being stored as though someone had chosen it.
func (c *Control) deriveLastResort(describe protocol.Describe) (protocol.ModelBinding, error) {
	if model := strings.TrimSpace(c.opts.Model); model != "" {
		binding, ok := parseModelBinding(model)
		if !ok {
			return protocol.ModelBinding{}, fmt.Errorf("ACORNFALL_MODEL must be provider/modelId, got %q", model)
		}
		return binding, nil
	}
	if binding, ok := preferredModel(describe); ok {
		return binding, nil
	}
	if binding, ok := firstAuthenticated(describe); ok {
		return binding, nil
	}
	return protocol.ModelBinding{}, fmt.Errorf(
		"%w: run `acornfall provider add <provider> <api-key>` or connect one in the UI", ErrNoProvider,
	)
}

// preferredModel is the decision part of the last resort: a model this install asked for by
// name, and only from a provider whose credentials are there to run it.
func preferredModel(describe protocol.Describe) (protocol.ModelBinding, bool) {
	for _, wanted := range lastResortModels {
		for _, provider := range describe.Providers {
			if !provider.Authenticated {
				continue
			}
			for _, model := range provider.Models {
				if model.ID == wanted {
					return protocol.ModelBinding{Provider: provider.ID, ModelID: wanted}, true
				}
			}
		}
	}
	return protocol.ModelBinding{}, false
}

// firstAuthenticated is the guess at the end of the line: the first provider that can run
// something, and the first model it lists.
func firstAuthenticated(describe protocol.Describe) (protocol.ModelBinding, bool) {
	for _, provider := range describe.Providers {
		if !provider.Authenticated || len(provider.Models) == 0 {
			continue
		}
		return protocol.ModelBinding{Provider: provider.ID, ModelID: provider.Models[0].ID}, true
	}
	return protocol.ModelBinding{}, false
}

func (c *Control) isAvailable(describe protocol.Describe, binding protocol.ModelBinding) bool {
	for _, provider := range describe.Providers {
		if provider.ID != binding.Provider || !provider.Authenticated {
			continue
		}
		for _, model := range provider.Models {
			if model.ID == binding.ModelID {
				return true
			}
		}
	}
	return false
}
