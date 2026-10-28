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
)

// offerable narrows a state's providers to the models this caller may run. The owner keeps everything, and a
// person with no list keeps everything too — an empty list is "any model this install has", which is where a
// person starts and what the owner leaves them on.
func (s *Server) offerable(state control.State, caller control.Caller) control.State {
	if caller.Owner || s.gate.People == nil {
		return state
	}
	person, found, err := s.gate.People.Find(caller.Email)
	if err != nil || !found || len(person.Models) == 0 {
		// A record that cannot be read is not a reason to empty somebody's picker: the refusal at assignment is
		// the control, and it still says what it said.
		return state
	}
	providers := make([]protocol.ProviderInfo, 0, len(state.Providers))
	for _, provider := range state.Providers {
		kept := make([]protocol.ModelInfo, 0, len(provider.Models))
		for _, model := range provider.Models {
			if person.Allows(provider.ID, model.ID) {
				kept = append(kept, model)
			}
		}
		provider.Models = kept
		providers = append(providers, provider)
	}
	state.Providers = providers
	return state
}
