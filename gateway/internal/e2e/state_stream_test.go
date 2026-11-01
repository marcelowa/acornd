package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The picker is filtered in every state the interface receives, not only in the first one. The stream sends a
// state of its own, and before this it carried the worker's whole catalog: the allowlist held on the first frame
// and was gone after the next, which is the frame a phone actually renders (ADR 0032 §6).
func TestEveryStateFrameIsFiltered(t *testing.T) {
	t.Parallel()
	s := start(t)
	// After the start: a fresh install migrates a settings file written before boot into the personal state,
	// and the install's allowlist lives at the data folder, where every state reads it.
	settings := `{"providerModels":{"faux":["not-a-model"]}}`
	if err := os.WriteFile(filepath.Join(s.DataDir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Addr+"/api/stream?workspace=personal", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	// Through the suite's client, so the request carries the proof the console asks for: a stream is an API
	// route like any other (ADR 0036 §3).
	response, err := s.client().http.Do(request)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d", response.StatusCode)
	}

	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var state struct {
			Providers []struct {
				ID     string `json:"id"`
				Models []struct {
					ID string `json:"id"`
				} `json:"models"`
			} `json:"providers"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &state); err != nil {
			t.Fatalf("decode the first state: %v", err)
		}
		for _, provider := range state.Providers {
			for _, model := range provider.Models {
				if model.ID == "faux-1" {
					t.Fatalf("the stream offered %s/%s, which the allowlist does not", provider.ID, model.ID)
				}
			}
		}
		return
	}
	t.Fatal("the stream sent no state")
}
