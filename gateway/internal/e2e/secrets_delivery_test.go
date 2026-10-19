package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A secret is created with the workspace's sandbox, the model is told its name, and the guest
// sees only the placeholder. Rotating the value is live; adding a name needs a restart, and
// the state says which happened.
func TestSecretsReachTheSandboxWithoutTheirValues(t *testing.T) {
	t.Parallel()
	maybeSandbox(t)
	const value = "e2e-secret-value"
	const rotated = "e2e-rotated-value"
	s := start(t,
		withSetup(func(t *testing.T, s *system) {
			writeVault(t, s.Home, []map[string]any{{
				"name":         "SPIKE_TOKEN",
				"description":  "a test token",
				"scope":        "global",
				"value":        value,
				"hosts":        []string{"api.example.com", "httpbin.org"},
				"substitution": map[string]bool{"headers": true},
				"allowEnv":     true,
				"rotatedAt":    time.Now().UTC().Format(time.RFC3339Nano),
			}})
		}),
		withScript(
			scriptRule{
				Match: "show the token",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": `printf '%s' "$SPIKE_TOKEN"`,
				}}},
				Reply: "the command ran",
			},
			scriptRule{
				Match: "send the token to httpbin",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": `apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq curl >/dev/null 2>&1 && curl -fsS -H "Authorization: Bearer $SPIKE_TOKEN" https://httpbin.org/headers`,
				}}},
				Reply: "the token went out",
			},
		),
	)
	c := s.client()
	root := c.waitForConversation("general")
	c.prompt("general", root, "show the token")

	view := c.waitForText("general", root, "$MSB_SPIKE_TOKEN")
	if strings.Contains(view.all(), value) {
		t.Fatalf("the value reached the transcript:\n%s", view.all())
	}

	state := c.state("general")
	if state.Sandbox.SecretsProblem != "" {
		t.Fatalf("the vault should be readable: %s", state.Sandbox.SecretsProblem)
	}
	if len(state.Sandbox.Secrets) != 1 || state.Sandbox.Secrets[0].Name != "SPIKE_TOKEN" {
		t.Fatalf("the state should list the delivered secret, got %+v", state.Sandbox.Secrets)
	}
	if state.Sandbox.Secrets[0].Description != "a test token" {
		t.Errorf("the state should carry the description, got %q", state.Sandbox.Secrets[0].Description)
	}
	if state.Sandbox.Secrets[0].Delivery != "substitute" || state.Sandbox.Secrets[0].Status != "delivered" {
		t.Errorf("unexpected delivery state: %+v", state.Sandbox.Secrets[0])
	}
	if state.Sandbox.SecretsRestart {
		t.Error("a secret created with the sandbox should be live")
	}

	// Rotation keeps the name, so the engine takes the new value live.
	c.post("/api/secrets/SPIKE_TOKEN/rotate", map[string]any{"value": rotated}, nil)
	after := c.state("general")
	if after.Sandbox.SecretsRestart {
		t.Error("rotating an existing secret should not need a restart")
	}
	payload, err := os.ReadFile(filepath.Join(s.Home, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), rotated) || strings.Contains(string(payload), value) {
		t.Errorf("rotation did not replace the stored value:\n%s", payload)
	}
	_, body := c.do("GET", "/api/secrets", nil)
	if strings.Contains(string(body), rotated) {
		t.Fatal("the API returned the rotated value")
	}

	// Adding a name cannot be applied to a running sandbox; it waits for the next start.
	c.post("/api/secrets", map[string]any{
		"name":         "SECOND_TOKEN",
		"description":  "another test token",
		"value":        "second-value",
		"hosts":        []string{"api.example.com"},
		"substitution": map[string]bool{"headers": true},
		"allowEnv":     true,
	}, nil)
	added := c.state("general")
	if !added.Sandbox.SecretsRestart {
		t.Error("adding a secret to a running sandbox should say a restart is needed")
	}
	if len(added.Sandbox.Secrets) != 2 {
		t.Errorf("the state should list both secrets, got %+v", added.Sandbox.Secrets)
	}

	// The engine substitutes the placeholder for an allowed host: httpbin echoes the header
	// back, so the real value is proof the network layer used it. This is the value leaving
	// the sandbox in a request, which is what the secret is for; the guest never held it.
	c.prompt("general", root, "send the token to httpbin")
	substituted := c.waitForText("general", root, rotated)
	if !strings.Contains(substituted.all(), "httpbin.org") {
		t.Errorf("the response should be httpbin's echo:\n%s", substituted.all())
	}
}

func writeVault(t *testing.T, home string, secrets []map[string]any) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"version": 1, "secrets": secrets})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "secrets.json"), append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
