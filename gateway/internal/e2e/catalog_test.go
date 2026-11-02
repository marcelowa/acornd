package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The models dialog chooses from the catalog, so the catalog must not be narrowed by the allowlist: a provider
// with nothing allowed yet answered "0 models", and the dialog that would tick the first one had nothing to tick.
func TestTheCatalogIsNotNarrowedByTheAllowlist(t *testing.T) {
	t.Parallel()
	workspaceResources(t)
	s := start(t, withScript(scriptRule{Match: "anything", Reply: "answered"}))
	// The harness grants the scripted provider so its turns run; an install with no grant yet is the state this
	// test is about, and the file is what the catalog reads.
	if err := os.WriteFile(filepath.Join(s.DataDir, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("clear the allowlist: %v", err)
	}
	status, payload := s.client().do(http.MethodGet, "/api/providers/catalog", nil)
	if status != http.StatusOK {
		t.Fatalf("catalog = %d (%s)", status, payload)
	}
	var answer struct {
		Providers []struct {
			ID     string `json:"id"`
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(payload, &answer); err != nil {
		t.Fatalf("catalog is not a provider list: %v (%s)", err, payload)
	}
	for _, provider := range answer.Providers {
		if provider.ID != "faux" {
			continue
		}
		if len(provider.Models) == 0 {
			t.Fatal("the catalog offered no models for a provider with an empty allowlist")
		}
		return
	}
	t.Fatalf("the catalog does not name the scripted provider: %s", payload)
}
