package control

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/acornfall/acornfall/gateway/internal/protocol"
)

// Which models this install may use from each provider, chosen by the owner when the provider is connected.
//
// It lives in the install's settings, not on a person: a key is spent by everyone, so what it may be spent on is a
// property of the connection. A provider with no entry is a provider nobody has narrowed — every model it has is
// usable, which is where an install starts.
//
// The list is enforced in two places and offered in a third: a model outside it is refused when it is assigned,
// refused again on every turn (so taking one away stops the conversation that was using it), and never offered in
// the picker. A gate that only filtered the interface would be a gate that can be walked around with a POST.

// Empty reports whether this layer says nothing at all. It is what decides whether the file is kept: a file holding
// only an allowlist is a file with something to say, and deleting it because no default model is set would throw the
// list away.
func (d Defaults) Empty() bool {
	return d.Model == (protocol.ModelBinding{}) && d.Thinking == "" && d.Agent == "" && len(d.ProviderModels) == 0
}

// providerKey is how a provider is named in the settings: lowercase, because two spellings of one provider in a file
// people edit by hand is two entries to keep in step.
func providerKey(provider string) string { return strings.ToLower(strings.TrimSpace(provider)) }

// Allowed answers the models this install may use from a provider. Empty means "all of them".
func (d Defaults) Allowed(provider string) []string {
	if len(d.ProviderModels) == 0 {
		return nil
	}
	return d.ProviderModels[providerKey(provider)]
}

// ModelAllowed reports whether this install may run one model. A provider with no list allows all of its models, and
// an empty list for a provider is the same answer: nobody narrowed it.
func (d Defaults) ModelAllowed(provider, model string) bool {
	allowed := d.Allowed(provider)
	if len(allowed) == 0 {
		return true
	}
	for _, name := range allowed {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(model)) {
			return true
		}
	}
	return false
}

// AllowedText is what a refusal quotes: the models this install may use from that provider, as they were written.
func (d Defaults) AllowedText(provider string) string {
	return strings.Join(d.Allowed(provider), ", ")
}

// ProviderModelsPath is the settings file the list lives in, inside an install folder.
func ProviderModelsPath(dataDir string) string { return filepath.Join(dataDir, "settings.json") }

// SetProviderModels writes one provider's list and answers the whole map. An empty list clears the entry, which is how
// an owner says "every model it has" again.
func SetProviderModels(dataDir, provider string, models []string) (map[string][]string, error) {
	key := providerKey(provider)
	if key == "" {
		return nil, fmt.Errorf("a provider is needed: write it as the id the console shows")
	}
	path := ProviderModelsPath(dataDir)
	defaults, err := LoadDefaults(path)
	if err != nil {
		return nil, err
	}
	cleaned := make([]string, 0, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model)
		if name == "" {
			continue
		}
		cleaned = append(cleaned, name)
	}
	if defaults.ProviderModels == nil {
		defaults.ProviderModels = map[string][]string{}
	}
	if len(cleaned) == 0 {
		delete(defaults.ProviderModels, key)
	} else {
		defaults.ProviderModels[key] = cleaned
	}
	if err := SaveDefaults(path, defaults); err != nil {
		return nil, err
	}
	return defaults.ProviderModels, nil
}

// InstallAllowlist reads the map, or an empty one when the file is not there yet. A file that cannot be read is
// reported rather than treated as "nothing is limited": an allowlist nobody can read is not an allowlist.
func InstallAllowlist(dataDir string) (map[string][]string, error) {
	defaults, err := LoadDefaults(ProviderModelsPath(dataDir))
	if err != nil {
		return nil, err
	}
	if defaults.ProviderModels == nil {
		return map[string][]string{}, nil
	}
	return defaults.ProviderModels, nil
}
