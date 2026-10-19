package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The agent asks for a secret; the stream carries the request, the test answers it through
// the gateway, and the tool continues. The value is in the vault and nowhere in the view.
func TestTheConversationAsksForASecretWithoutShowingItsValue(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	const value = "card-value-that-must-not-be-shown"
	s := start(t, withScript(scriptRule{
		Match: "ask for the card token",
		Tools: []scriptTool{{
			Name: "secret",
			Args: map[string]any{
				"action":      "request",
				"name":        "CARD_TOKEN",
				"description": "the card test token",
				"hosts":       []string{"api.example.com"},
			},
		}},
		Reply: "The token is ready.",
	}))
	c := s.client()
	root := c.waitForConversation("general")

	requests := make(chan string, 1)
	go func() { requests <- c.waitForSecretRequest("general", root) }()
	time.Sleep(200 * time.Millisecond)
	c.prompt("general", root, "ask for the card token")

	var requestID string
	select {
	case requestID = <-requests:
	case <-time.After(10 * time.Second):
		t.Fatal("the secret request never reached the stream")
	}
	if requestID == "" {
		t.Fatal("the secret request had no id")
	}

	path := fmt.Sprintf("/api/workspaces/general/conversations/%s/secrets/%s", root, requestID)
	status, body := c.do(http.MethodPost, path, map[string]any{
		"value":       value,
		"description": "a token set from the card",
		"hosts":       []string{"api.github.com"},
		"scope":       "workspace",
	})
	if status != http.StatusOK {
		t.Fatalf("answer the card: %d %s", status, body)
	}

	view := c.waitForText("general", root, "The token is ready.")
	if strings.Contains(view.all(), value) {
		t.Fatalf("the value reached the transcript:\n%s", view.all())
	}
	if !strings.Contains(view.all(), "CARD_TOKEN") || !strings.Contains(view.all(), "the card test token") {
		t.Errorf("the request should be visible by name and description:\n%s", view.all())
	}

	status, body = c.do(http.MethodGet, "/api/workspaces/general/secrets", nil)
	if status != http.StatusOK {
		t.Fatalf("read the workspace's secrets: %d %s", status, body)
	}
	if strings.Contains(string(body), value) {
		t.Fatalf("the secrets API returned the value:\n%s", body)
	}
	var listed vaultWorkspace
	decodeInto(t, "workspace secrets", body, &listed)
	found := false
	for _, secret := range listed.Own {
		if secret.Name == "CARD_TOKEN" {
			found = true
			if secret.Description != "a token set from the card" {
				t.Errorf("the card's description should be stored, got %q", secret.Description)
			}
			if len(secret.Hosts) != 1 || secret.Hosts[0] != "api.github.com" {
				t.Errorf("the card's hosts should be stored, got %v", secret.Hosts)
			}
		}
	}
	if !found {
		t.Errorf("the workspace should hold the card's secret: %+v", listed.Own)
	}
}

// The agent can ask what exists. The answer carries names and descriptions; the prompt was
// written before the worker started, so this is the live read.
func TestTheConversationListsSecretsWithoutValues(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	const value = "listed-value-that-must-not-be-shown"
	s := start(t,
		withSetup(func(t *testing.T, s *system) {
			writeVault(t, s.Home, []map[string]any{{
				"name":         "LIST_TOKEN",
				"description":  "a token the agent should know about",
				"scope":        "global",
				"value":        value,
				"hosts":        []string{"api.example.com"},
				"substitution": map[string]bool{"headers": true},
				"allowEnv":     true,
				"rotatedAt":    time.Now().UTC().Format(time.RFC3339Nano),
			}})
		}),
		withScript(scriptRule{
			Match: "what secrets are there",
			Tools: []scriptTool{{
				Name: "secret",
				Args: map[string]any{"action": "list"},
			}},
			Reply: "That is the list.",
		}),
	)
	c := s.client()
	root := c.waitForConversation("general")
	c.prompt("general", root, "what secrets are there")
	view := c.waitForText("general", root, "That is the list.")
	if strings.Contains(view.all(), value) {
		t.Fatalf("the value reached the transcript:\n%s", view.all())
	}
	if !strings.Contains(view.all(), "Secrets available to commands here") {
		t.Errorf("the tool should have answered with the list:\n%s", view.all())
	}
	if !strings.Contains(view.all(), "LIST_TOKEN") || !strings.Contains(view.all(), "a token the agent should know about") {
		t.Errorf("the list should carry the name and description:\n%s", view.all())
	}
}

// The card refuses a substitute secret with no allowed host: storing it would be a secret
// that can never be used. The tool stays waiting, and cancelling closes it.
func TestACardRefusesASecretWithoutHosts(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	s := start(t, withScript(scriptRule{
		Match: "ask for a hostless token",
		Tools: []scriptTool{{
			Name: "secret",
			Args: map[string]any{
				"action":      "request",
				"name":        "NOHOST_TOKEN",
				"description": "a token with nowhere to go",
			},
		}},
		Reply: "the hostless card finished",
	}))
	c := s.client()
	root := c.waitForConversation("general")
	requests := make(chan string, 1)
	go func() { requests <- c.waitForSecretRequest("general", root) }()
	time.Sleep(200 * time.Millisecond)
	c.prompt("general", root, "ask for a hostless token")
	var requestID string
	select {
	case requestID = <-requests:
	case <-time.After(10 * time.Second):
		t.Fatal("the hostless request never reached the stream")
	}
	path := fmt.Sprintf("/api/workspaces/general/conversations/%s/secrets/%s", root, requestID)
	status, body := c.do(http.MethodPost, path, map[string]any{
		"value":       "hostless-value",
		"description": "a token with nowhere to go",
		"hosts":       []string{},
		"scope":       "workspace",
	})
	if status != http.StatusBadRequest || !strings.Contains(string(body), "allowed host") {
		t.Fatalf("a substitute secret with no hosts should be refused, got %d %s", status, body)
	}
	status, body = c.do(http.MethodPost, path, map[string]any{"cancelled": true})
	if status != http.StatusOK {
		t.Fatalf("cancel the hostless card: %d %s", status, body)
	}
	view := c.waitForText("general", root, "the hostless card finished")
	if strings.Contains(view.all(), "hostless-value") {
		t.Fatalf("the rejected value reached the transcript:\n%s", view.all())
	}
	listed := vaultWorkspace{}
	c.get("/api/workspaces/general/secrets", &listed)
	for _, secret := range listed.Own {
		if secret.Name == "NOHOST_TOKEN" {
			t.Error("a rejected secret must not be stored")
		}
	}
}

// An empty list is still an answer: the tool must not receive undefined and fall over, which
// is what an omitted empty JSON array would become.
func TestTheConversationListsNoSecrets(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	s := start(t, withScript(scriptRule{
		Match: "is anything here",
		Tools: []scriptTool{{Name: "secret", Args: map[string]any{"action": "list"}}},
		Reply: "nothing to use",
	}))
	c := s.client()
	root := c.waitForConversation("general")
	c.prompt("general", root, "is anything here")
	view := c.waitForText("general", root, "nothing to use")
	if !strings.Contains(view.all(), "No secrets are available") {
		t.Errorf("the tool should answer with an empty list:\n%s", view.all())
	}
}

// waitForSecretRequest opens a stream and reads until a secret request arrives. It is the
// test's card: the control only shows the request while someone is watching.
func (c *client) waitForSecretRequest(workspace, conversation string) string {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/stream?workspace=%s&conversation=%s", c.base, workspace, conversation), nil)
	if err != nil {
		c.t.Error(err)
		return ""
	}
	response, err := c.stream.Do(request)
	if err != nil {
		c.t.Error(err)
		return ""
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var frame struct {
			Type   string `json:"type"`
			Secret *struct {
				Kind      string `json:"kind"`
				RequestID string `json:"requestId"`
			} `json:"secret"`
		}
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			continue
		}
		if frame.Type == "secret" && frame.Secret != nil && frame.Secret.Kind == "request" {
			return frame.Secret.RequestID
		}
	}
	return ""
}
