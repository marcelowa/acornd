package e2e

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A sign-in is started and named before the flow finishes, because the interaction arrives as
// frames afterwards and the dialog needs something to call this flow. Signing out re-reads the
// catalog, and refreshing asks the harness again — those two are how the pickers learn that a
// provider came or went.
func TestTheProviderVerbsReachTheHarness(t *testing.T) {
	t.Parallel()
	s := start(t)
	c := s.client()

	var started struct {
		AuthID string `json:"authId"`
		Status string `json:"status"`
	}
	c.post("/api/providers/login", map[string]any{"providerId": "faux", "authType": "api_key"}, &started)
	if started.AuthID == "" {
		t.Error("a sign-in has to be nameable before it finishes")
	}
	if started.Status != "started" {
		t.Errorf("status = %q", started.Status)
	}
	// The flow's own answers are forwarded to the harness, which owns the state machine: the edge
	// reads the fields it needs and says what it was told.
	var answered struct {
		Status string `json:"status"`
	}
	c.post("/api/auth/answer", map[string]any{"authId": started.AuthID, "promptId": "p1", "value": "key"}, &answered)
	if answered.Status != "answered" {
		t.Errorf("answer = %+v", answered)
	}
	c.post("/api/auth/cancel", map[string]any{"authId": started.AuthID}, &answered)
	if answered.Status != "cancelled" {
		t.Errorf("cancel = %+v", answered)
	}

	// Signing out and refreshing both answer with the state, so the screen redraws from one
	// answer instead of asking again.
	var state struct {
		Workspace struct {
			Name string `json:"name"`
		} `json:"workspace"`
	}
	c.post("/api/providers/logout", map[string]any{"providerId": "faux"}, &state)
	if state.Workspace.Name != "personal" {
		t.Errorf("logout answered %+v", state)
	}
	c.post("/api/providers/refresh", map[string]any{}, &state)
	if state.Workspace.Name != "personal" {
		t.Errorf("refresh answered %+v", state)
	}
}

// A stream asked to follow a conversation that is not there says so on the stream and closes: the
// status line is already written, so the refusal has to be an event. The client reconnects, which
// is what an EventSource does with a closed connection, and it works once the reason is gone.
func TestAStreamForAConversationThatIsNotThereSaysSoAndCloses(t *testing.T) {
	t.Parallel()
	s := start(t)
	c := s.client()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/api/stream?workspace=personal&conversation=not-a-conversation", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.stream.Do(request)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}

	// The frame comes, and it carries the reason.
	lines := bufio.NewScanner(response.Body)
	seen := ""
	deadline := time.Now().Add(10 * time.Second)
	for lines.Scan() && time.Now().Before(deadline) {
		line := lines.Text()
		if strings.HasPrefix(line, "data:") && strings.Contains(line, "error") {
			seen = line
			break
		}
	}
	if seen == "" {
		t.Error("the stream should say why it cannot follow that conversation")
	}

	// And it closes rather than hanging: the client is expected to reconnect.
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 64)
		for {
			if _, err := response.Body.Read(buffer); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("the stream stayed open after saying it could not follow the conversation")
	}
}
