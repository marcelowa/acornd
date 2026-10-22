package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The host engine delivers through the worker's own environment. That is the environment
// contract a container engine will share: the value is in the sandbox, so a command can
// print it. `allowEnv: false` is the opt-out and the state says the secret was withheld.
func TestHostEngineDeliversSecretsIntoTheWorkerEnvironment(t *testing.T) {
	t.Parallel()
	workspaceResources(t)
	s := start(t,
		withSetup(func(t *testing.T, s *system) {
			writeVault(t, s.DataDir, []map[string]any{
				{
					"name":         "ENV_TOKEN",
					"description":  "a value the host engine may hold",
					"scope":        "global",
					"value":        "host-engine-value",
					"substitution": map[string]bool{"headers": true},
					"allowEnv":     true,
					"rotatedAt":    time.Now().UTC().Format(time.RFC3339Nano),
				},
				{
					"name":         "HIDDEN_TOKEN",
					"description":  "a value that must stay out",
					"scope":        "global",
					"value":        "must-not-appear",
					"substitution": map[string]bool{"headers": true},
					"allowEnv":     false,
					"rotatedAt":    time.Now().UTC().Format(time.RFC3339Nano),
				},
			})
		}),
		withScript(
			scriptRule{
				Match: "read the environment token",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": `printf 'seen=[%s] hidden=[%s]' "$ENV_TOKEN" "$HIDDEN_TOKEN"`,
				}}},
				Reply: "read it",
			},
			scriptRule{
				Match: "read it again",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": `printf 'seen=[%s]' "$ENV_TOKEN"`,
				}}},
				Reply: "read it again",
			},
			scriptRule{
				Match: "ask for a host card token",
				Tools: []scriptTool{{
					Name: "secret",
					Args: map[string]any{
						"action":      "request",
						"name":        "HOST_CARD_TOKEN",
						"description": "a host card token",
						"hosts":       []string{"api.example.com"},
					},
				}},
				Reply: "the host card finished",
			},
			scriptRule{
				Match: "read the host card token",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					"command": `printf 'card=[%s]' "$HOST_CARD_TOKEN"`,
				}}},
				Reply: "the host card is readable",
			},
			scriptRule{
				Match: "Automatic continuation",
				Reply: "continued after the host secret",
			},
		),
	)
	c := s.client()
	root := c.waitForConversation("general")
	c.prompt("general", root, "read the environment token")
	first := c.waitForText("general", root, "read it")
	if !strings.Contains(first.all(), "seen=[host-engine-value]") {
		t.Fatalf("the host engine should put the value in the worker environment:\n%s", first.all())
	}
	if strings.Contains(first.all(), "must-not-appear") {
		t.Fatalf("a secret that may not enter the sandbox was delivered:\n%s", first.all())
	}

	state := c.state("general")
	if state.Sandbox.Engine != "host" || state.Sandbox.Isolated {
		t.Fatalf("this test should be on the host engine, got %+v", state.Sandbox)
	}
	statuses := map[string]string{}
	deliveries := map[string]string{}
	for _, secret := range state.Sandbox.Secrets {
		statuses[secret.Name] = secret.Status
		deliveries[secret.Name] = secret.Delivery
	}
	if statuses["ENV_TOKEN"] != "delivered" || deliveries["ENV_TOKEN"] != "environment" {
		t.Errorf("ENV_TOKEN should be delivered through the environment: %+v", state.Sandbox.Secrets)
	}
	if !strings.Contains(statuses["HIDDEN_TOKEN"], "may not enter") {
		t.Errorf("HIDDEN_TOKEN should say why it was withheld: %+v", state.Sandbox.Secrets)
	}

	// A rotation restarts the worker, because a running process's environment cannot change.
	c.post("/api/secrets/ENV_TOKEN/rotate", map[string]any{"value": "rotated-host-value"}, nil)
	c.prompt("general", root, "read it again")
	second := c.waitForText("general", root, "seen=[rotated-host-value]")
	if !strings.Contains(second.all(), "seen=[rotated-host-value]") {
		t.Fatalf("the restarted worker should have the new value:\n%s", second.all())
	}

	// A card answer must not restart the worker under the turn that asked for it: the value
	// is stored and the state says the next start is what will have it.
	requests := make(chan string, 1)
	go func() { requests <- c.waitForSecretRequest("general", root) }()
	time.Sleep(200 * time.Millisecond)
	c.prompt("general", root, "ask for a host card token")
	var requestID string
	select {
	case requestID = <-requests:
	case <-time.After(10 * time.Second):
		t.Fatal("the host card request never reached the stream")
	}
	path := fmt.Sprintf("/api/workspaces/general/conversations/%s/secrets/%s", root, requestID)
	status, body := c.do("POST", path, map[string]any{
		"value":       "host-card-value",
		"description": "a host card token",
		"hosts":       []string{"api.example.com"},
		"scope":       "workspace",
	})
	if status != 200 {
		t.Fatalf("answer the host card: %d %s", status, body)
	}
	finished := c.waitForText("general", root, "the host card finished")
	if strings.Contains(finished.all(), "host-card-value") {
		t.Fatalf("the host card value reached the transcript:\n%s", finished.all())
	}
	if !strings.Contains(finished.all(), "will continue automatically") {
		t.Errorf("the tool should say the control will continue:\n%s", finished.all())
	}

	// The run settled; the deferred worker restart should have put the value in the new
	// process environment. A prompt after it reads the value from the host sandbox.
	deadline := time.Now().Add(30 * time.Second)
	for {
		after := c.state("general")
		if !after.Sandbox.SecretsRestart {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the host card's secret was not applied: %+v", after.Sandbox)
		}
		time.Sleep(250 * time.Millisecond)
	}
	c.waitForText("general", root, "continued after the host secret")
	c.waitIdle("general", root, 60*time.Second)
	c.prompt("general", root, "read the host card token")
	read := c.waitForText("general", root, "the host card is readable")
	if !strings.Contains(read.all(), "card=[host-card-value]") {
		t.Errorf("the restarted worker should have the card's value:\n%s", read.all())
	}
}
