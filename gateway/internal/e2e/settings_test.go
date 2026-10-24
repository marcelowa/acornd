package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The operations the interface makes on a workspace's own configuration, and the ones a person
// makes on the providers. They are small, and each one is a rule: a model that cannot run is
// refused here rather than at the next prompt, a refresh re-asks the harness, and a sign-in is a
// flow whose answers arrive later.

func TestTheDefaultModelIsCheckedWhereTheChoiceIsMade(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	s := start(t)
	c := s.client()

	// The dev provider the harness starts with: `faux/faux-1`, which is what every other test in
	// this suite uses when it needs a model that runs without credentials.

	// A provider without a model is half a binding, and half a binding is not one.
	status, body := c.postStatus("/api/default-model", map[string]any{"provider": "faux"})
	if status != http.StatusConflict {
		t.Errorf("half a binding: status %d, want 409 (%s)", status, body)
	}
	if !strings.Contains(body, "both") {
		t.Errorf("the refusal should say both are needed: %q", body)
	}
	// A model nobody offers is refused here, where the choice was made.
	status, body = c.postStatus("/api/default-model", map[string]any{"provider": "faux", "modelId": "no-such-model"})
	if status != http.StatusConflict {
		t.Errorf("an unavailable model: status %d, want 409 (%s)", status, body)
	}

	// The real one is accepted, and the state says so.
	status, body = c.postStatus("/api/default-model", map[string]any{"provider": "faux", "modelId": "faux-1"})
	if status != http.StatusOK {
		t.Fatalf("a model the harness has: status %d (%s)", status, body)
	}
	state := c.state("general")
	if state.DefaultModel.Provider != "faux" || state.DefaultModel.ModelID != "faux-1" {
		t.Errorf("the state should carry the default, got %+v", state.DefaultModel)
	}

	// Clearing it falls back rather than leaving a workspace that cannot run: the state names a
	// model again, because the fallback chain answered.
	status, body = c.postStatus("/api/default-model", map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("clearing the default: status %d (%s)", status, body)
	}
	if got := c.state("general").DefaultModel; got.Provider == "" || got.ModelID == "" {
		t.Errorf("a cleared default must fall back, got %+v", got)
	}
}

// A refresh re-asks the harness, and the answer is the state: the same shape every other
// workspace operation answers with.
func TestARefreshAsksTheHarnessAgain(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	s := start(t)
	c := s.client()
	status, body := c.postStatus("/api/providers/refresh", nil)
	if status != http.StatusOK {
		t.Fatalf("refresh: %d %s", status, body)
	}
	// And reloading the agent definitions is the same kind of question.
	status, body = c.postStatus("/api/agents/reload", nil)
	if status != http.StatusOK {
		t.Fatalf("reload: %d %s", status, body)
	}
	var agents struct {
		Agents []struct {
			Name string `json:"name"`
		} `json:"agents"`
	}
	c.get("/api/agents", &agents)
	if len(agents.Agents) == 0 {
		t.Error("a described harness has agents, after a reload too")
	}
}

// A sign-in is a flow: the answer is an id, and the flow's own answers arrive later. What the edge
// owes is a refusal for a call that cannot start one, and an id for one that can.
func TestASignInStartsOrIsRefused(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	s := start(t)
	c := s.client()

	// No provider named: the caller's mistake, told before anything starts.
	if status, _ := c.postStatus("/api/providers/login", map[string]any{}); status != http.StatusBadRequest {
		t.Errorf("a login with no provider: status %d, want 400", status)
	}
	if status, _ := c.postStatus("/api/providers/login", map[string]any{"providerId": "faux", "authType": "carrier-pigeon"}); status != http.StatusBadRequest {
		t.Errorf("an auth type this build does not have: status %d, want 400", status)
	}

	// The dev provider has nothing to sign in to, so it answers with an id and then fails in the
	// worker: the edge's job is the id, and the failure must not take the gateway with it.
	status, body := c.postStatus("/api/providers/login", map[string]any{"providerId": "faux", "authType": "api_key"})
	if status != http.StatusAccepted {
		t.Fatalf("a login: status %d (%s)", status, body)
	}
	var started struct {
		AuthID string `json:"authId"`
	}
	if err := json.Unmarshal([]byte(body), &started); err != nil || started.AuthID == "" {
		t.Fatalf("the answer should carry the auth id: %q", body)
	}
	// Still alive afterwards: a sign-in that fails is not a sign-in that crashes the server.
	if got := c.state("general"); got.Workspace.Name == "" {
		t.Error("the gateway should still answer after a failed sign-in")
	}

	// An answer is forwarded to the harness and the harness decides: the edge does not keep a
	// table of flows, so it has nothing to refuse with. What it owes is that the answer arrives and
	// the server is still there afterwards.
	if status, body := c.postStatus("/api/auth/answer", map[string]any{"authId": "auth-nobody", "promptId": "p1", "value": "123456"}); status != http.StatusOK {
		t.Errorf("answering a flow nobody started: status %d (%s)", status, body)
	}
	if status, _ := c.postStatus("/api/auth/answer", map[string]any{"authId": "auth-nobody"}); status != http.StatusBadRequest {
		t.Errorf("an answer with no promptId: status %d, want 400", status)
	}
	if status, _ := c.postStatus("/api/auth/cancel", map[string]any{"authId": "auth-nobody"}); status != http.StatusOK {
		t.Errorf("cancelling a flow nobody started: status %d, want 200", status)
	}
}

// Signing out of a provider this install does not have is answered, not crashed on: the caller
// learns that there was nothing to sign out of.
func TestSigningOutSaysWhatHappened(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	s := start(t)
	c := s.client()
	if status, _ := c.postStatus("/api/providers/logout", map[string]any{}); status != http.StatusBadRequest {
		t.Errorf("a logout with no provider: want 400")
	}
	if status, body := c.postStatus("/api/providers/logout", map[string]any{"providerId": "nobody"}); status != http.StatusOK && status != http.StatusConflict {
		t.Errorf("signing out of a provider nobody has: status %d (%s)", status, body)
	}
}
