// The install's model allowlist, and the catalog it is chosen from.
//
// ADR 0032 §6 asked for a model allowlist; the decision taken since is that it belongs to the provider connection
// rather than to a person, because one key is spent by everyone. A person's own list still exists and still narrows
// on top, but the list the admin sets here is the one that decides what a key may be spent on at all.
package httpapi

import (
	"net/http"

	"github.com/acornfall/acornfall/gateway/internal/control"
)

// providerAllowlist answers which models this install may use from each provider. Empty for a provider means every
// model it has.
func (s *Server) providerAllowlist(w http.ResponseWriter, _ *http.Request) {
	allowed, err := control.InstallAllowlist(s.gate.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": allowed})
}

// setProviderAllowlist writes one provider's list. An empty list clears it, which is how the admin says "every model
// it has" again — the difference between "narrowed to nothing" and "not narrowed" is the entry, not an empty array.
func (s *Server) setProviderAllowlist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string   `json:"provider"`
		Models   []string `json:"models"`
	}
	if !decode(w, r, &body) {
		return
	}
	allowed, err := control.SetProviderModels(s.gate.Dir, body.Provider, body.Models)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": allowed})
}

// providerCatalog answers every provider with every model it has, unfiltered: it is what the admin chooses from, and
// narrowing it by the allowlist would make the list impossible to widen again from the screen.
func (s *Server) providerCatalog(w http.ResponseWriter, r *http.Request) {
	ctl, err := s.controlFor(r)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": ctl.StateFor("").Providers})
}
