package e2e

import (
	"encoding/json"
	"fmt"
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
			writeVault(t, s.DataDir, []map[string]any{{
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
				Match: "ask for the second token",
				Tools: []scriptTool{{
					Name: "secret",
					Args: map[string]any{
						"action":      "request",
						"name":        "SECOND_TOKEN",
						"description": "another test token",
						"hosts":       []string{"httpbin.org"},
					},
				}},
				Reply: "the second card finished",
			},
			scriptRule{
				Match: "send the token to httpbin",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": `apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq curl >/dev/null 2>&1 && curl -fsS -H "Authorization: Bearer $SECOND_TOKEN" https://httpbin.org/headers`,
				}}},
				Reply: "the token went out",
			},
			scriptRule{
				Match: "Automatic continuation",
				Reply: "continued after the secret",
			},
			scriptRule{
				Match: "show both tokens",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": `printf 'one=[%s] two=[%s]' "$SPIKE_TOKEN" "$SECOND_TOKEN"`,
				}}},
				Reply: "both shown",
			},
		),
	)
	c := s.client()
	root := c.waitForConversation("personal")
	c.prompt("personal", root, "show the token")

	view := c.waitForText("personal", root, "$MSB_SPIKE_TOKEN")
	if strings.Contains(view.all(), value) {
		t.Fatalf("the value reached the transcript:\n%s", view.all())
	}

	state := c.state("personal")
	if state.Sandbox.SecretsProblem != "" {
		t.Fatalf("the vault should be readable: %s", state.Sandbox.SecretsProblem)
	}
	if len(state.Sandbox.Secrets) != 1 || state.Sandbox.Secrets[0].Name != "SPIKE_TOKEN" {
		t.Fatalf("the state should list the delivered secret, got %+v", state.Sandbox.Secrets)
	}
	if state.Sandbox.Secrets[0].Description != "a test token" {
		t.Errorf("the state should carry the description, got %q", state.Sandbox.Secrets[0].Description)
	}
	if state.Sandbox.Secrets[0].Placeholder != "$MSB_SPIKE_TOKEN" {
		t.Errorf("the state should name the guest placeholder, got %q", state.Sandbox.Secrets[0].Placeholder)
	}
	if state.Sandbox.Secrets[0].Delivery != "substitute" || state.Sandbox.Secrets[0].Status != "delivered" {
		t.Errorf("unexpected delivery state: %+v", state.Sandbox.Secrets[0])
	}
	if state.Sandbox.SecretsRestart {
		t.Error("a secret created with the sandbox should be live")
	}

	// Rotation keeps the name, so the engine takes the new value live.
	c.post("/api/secrets/SPIKE_TOKEN/rotate", map[string]any{"value": rotated}, nil)
	after := c.state("personal")
	if after.Sandbox.SecretsRestart {
		t.Error("rotating an existing secret should not need a restart")
	}
	payload, err := os.ReadFile(filepath.Join(s.DataDir, "secrets.json"))
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

	// A card answer while the turn is active stores the value for the next start. The tool
	// returns, the run settles, and the control restarts the sandbox then. This is the path
	// the bug report took: the value has to be usable on the next turn, not never.
	requests := make(chan string, 1)
	go func() { requests <- c.waitForSecretRequest("personal", root) }()
	time.Sleep(200 * time.Millisecond)
	c.prompt("personal", root, "ask for the second token")
	var requestID string
	select {
	case requestID = <-requests:
	case <-time.After(10 * time.Second):
		t.Fatal("the second secret request never reached the stream")
	}
	path := fmt.Sprintf("/api/workspaces/personal/conversations/%s/secrets/%s", root, requestID)
	status, body := c.do("POST", path, map[string]any{
		"value":       "second-card-value",
		"description": "another test token",
		"hosts":       []string{"httpbin.org"},
		"scope":       "workspace",
	})
	if status != 200 {
		t.Fatalf("answer the second card: %d %s", status, body)
	}
	cardView := c.waitForText("personal", root, "the second card finished")
	if !strings.Contains(cardView.all(), "will continue automatically") {
		t.Errorf("the tool should say the control will continue:\n%s", cardView.all())
	}

	// The run has settled; the control applies the pending restart and starts the next turn
	// itself. The new turn's script rule is the automatic continuation.
	deadline := time.Now().Add(30 * time.Second)
	for {
		added := c.state("personal")
		if !added.Sandbox.SecretsRestart && len(added.Sandbox.Secrets) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the card's secret was not applied after the run: %+v", added.Sandbox)
		}
		time.Sleep(250 * time.Millisecond)
	}
	c.waitForText("personal", root, "continued after the secret")
	c.waitIdle("personal", root, 60*time.Second)

	// The engine substitutes the placeholder for an allowed host: httpbin echoes the header
	// back, so the real value is proof the network layer used it. This is the value leaving
	// the sandbox in a request, which is what the secret is for; the guest never held it.
	// It also proves the card's secret works after the restart.
	c.prompt("personal", root, "send the token to httpbin")
	substituted := c.waitForText("personal", root, "second-card-value")
	if !strings.Contains(substituted.all(), "httpbin.org") {
		t.Errorf("the response should be httpbin's echo:\n%s", substituted.all())
	}

	c.prompt("personal", root, "show both tokens")
	both := c.waitForText("personal", root, "both shown")
	if !strings.Contains(both.all(), "one=[$MSB_SPIKE_TOKEN] two=[$MSB_SECOND_TOKEN]") {
		t.Errorf("both placeholders should be in the environment after the restart:\n%s", both.all())
	}
	if strings.Contains(both.all(), "two=[second-card-value]") {
		t.Errorf("the secret value reached the guest environment:\n%s", both.all())
	}
}

func writeVault(t *testing.T, dataDir string, secrets []map[string]any) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"version": 1, "secrets": secrets})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "secrets.json"), append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
