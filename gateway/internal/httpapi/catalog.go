// The range of models a caller is offered, which is not the range the install has.
//
// ADR 0032 §6: a person's list is enforced when a model is assigned, and the picker used to offer every model
// the install could run — a choice that ended in a refusal one press later. Offering only what the caller may run
// is the convenience that comes after the control, never instead of it: the refusal stays where it was, because a
// list that is only filtered in the interface is a list that can be posted around.
package httpapi

import (
	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/protocol"
	"sort"
	"strings"
)

// offerable narrows a state's providers to the models this caller may run, which is the install's allowlist for that
// provider and, when the person has one, their own list on top of it.
//
// The install's list binds the owner too: a whitelist with a hole for its author is a whitelist nobody has tested.
func (s *Server) offerable(state control.State, caller control.Caller) control.State {
	lists, ok := s.modelLists(caller)
	if !ok {
		// A file that cannot be read is not a reason to empty somebody's picker: the refusal at assignment and on
		// every turn is the control, and it still says what it said.
		return state
	}
	providers := make([]protocol.ProviderInfo, 0, len(state.Providers))
	for _, provider := range state.Providers {
		kept := make([]protocol.ModelInfo, 0, len(provider.Models))
		for _, model := range provider.Models {
			if lists.allows(provider.ID, model.ID) {
				kept = append(kept, model)
			}
		}
		provider.Models = kept
		providers = append(providers, provider)
	}
	state.Providers = providers
	return state
}

// modelLists is the two lists a model is judged by, read once per state so a provider's list is not loaded forty times
// for forty providers.
type modelLists struct {
	install  control.Defaults
	personal map[string]bool
	limited  bool
}

// allows is the question both the picker and the refusal ask: the install's list for the provider, and the person's
// own list on top of it when they have one.
func (l modelLists) allows(provider, model string) bool {
	if !l.install.ModelAllowed(provider, model) {
		return false
	}
	if !l.limited {
		return true
	}
	return l.personal[provider+"/"+model]
}

// personalModels is the person's own list for one provider, as written, for the refusal to quote.
func (l modelLists) personalModels(provider string) []string {
	names := make([]string, 0, len(l.personal))
	for key := range l.personal {
		if model, ok := strings.CutPrefix(key, provider+"/"); ok {
			names = append(names, model)
		}
	}
	sort.Strings(names)
	return names
}

// modelLists reads the install's allowlist and, for anybody but the owner, the person's own.
func (s *Server) modelLists(caller control.Caller) (modelLists, bool) {
	install, err := control.InstallAllowlist(s.gate.Dir)
	if err != nil {
		return modelLists{}, false
	}
	lists := modelLists{install: control.Defaults{ProviderModels: install}}
	if caller.Owner || s.gate.People == nil {
		return lists, true
	}
	person, found, err := s.gate.People.Find(caller.Email)
	if err != nil || !found || len(person.Models) == 0 {
		return lists, true
	}
	lists.personal = make(map[string]bool, len(person.Models))
	for _, allowed := range person.Models {
		lists.personal[allowed] = true
	}
	lists.limited = true
	return lists, true
}
