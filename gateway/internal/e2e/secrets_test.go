package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// vaultMeta is the API's contract: everything about a secret except its value.
type vaultMeta struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scope       string   `json:"scope"`
	Workspace   string   `json:"workspace,omitempty"`
	Hosts       []string `json:"hosts"`
	AllowEnv    bool     `json:"allowEnv"`
	Overrides   bool     `json:"overrides,omitempty"`
	Overridden  bool     `json:"overridden,omitempty"`
	Source      string   `json:"source,omitempty"`
}

type vaultList struct {
	Secrets []vaultMeta `json:"secrets"`
}

type vaultWorkspace struct {
	Workspace map[string]string `json:"workspace"`
	Global    []vaultMeta       `json:"global"`
	Own       []vaultMeta       `json:"own"`
	Effective []vaultMeta       `json:"effective"`
}

// A secret is created, listed, overridden by a workspace entry, rotated, and deleted. Every
// response is checked for the value itself: the API contract is metadata only.
func TestSecretsAreStoredAndListedWithoutValues(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	s := start(t)
	c := s.client()

	const value = "ghp-value-that-must-not-leak"
	c.post("/api/secrets", map[string]any{
		"name":         "GITHUB_TOKEN",
		"description":  "read-only token for releases",
		"value":        value,
		"hosts":        []string{"api.github.com", "github.com"},
		"substitution": map[string]bool{"headers": true},
		"allowEnv":     false,
	}, nil)

	status, body := c.do(http.MethodGet, "/api/secrets", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/secrets: %d %s", status, body)
	}
	if strings.Contains(string(body), value) {
		t.Fatalf("the API returned the value:\n%s", body)
	}
	var listed vaultList
	decodeInto(t, "secrets", body, &listed)
	if len(listed.Secrets) != 1 {
		t.Fatalf("expected one global secret, got %+v", listed.Secrets)
	}
	global := listed.Secrets[0]
	if global.Name != "GITHUB_TOKEN" || global.Scope != "global" || global.Description == "" {
		t.Errorf("metadata is wrong: %+v", global)
	}
	if len(global.Hosts) != 2 || global.AllowEnv {
		t.Errorf("hosts or allowEnv are wrong: %+v", global)
	}

	file := filepath.Join(s.DataDir, "secrets.json")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("the vault file should exist: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the vault file should be 0600, got %o", mode)
	}
	payload, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), value) {
		t.Error("the value should be in the store at rest")
	}

	// A workspace entry with the same name wins, and both lists say so.
	c.post("/api/workspaces/personal/secrets", map[string]any{
		"name":         "GITHUB_TOKEN",
		"description":  "this workspace's token",
		"value":        "workspace-value",
		"hosts":        []string{"api.github.com"},
		"substitution": map[string]bool{"headers": true},
		"allowEnv":     false,
	}, nil)
	status, body = c.do(http.MethodGet, "/api/workspaces/personal/secrets", nil)
	if status != http.StatusOK {
		t.Fatalf("GET workspace secrets: %d %s", status, body)
	}
	if strings.Contains(string(body), value) || strings.Contains(string(body), "workspace-value") {
		t.Fatalf("the workspace view returned a value:\n%s", body)
	}
	var view vaultWorkspace
	decodeInto(t, "workspace secrets", body, &view)
	if len(view.Global) != 1 || !view.Global[0].Overridden {
		t.Errorf("the global entry should be marked overridden: %+v", view.Global)
	}
	if len(view.Own) != 1 || !view.Own[0].Overrides {
		t.Errorf("the workspace entry should be marked as an override: %+v", view.Own)
	}
	if len(view.Effective) != 1 || view.Effective[0].Source != "workspace" {
		t.Errorf("the effective set should hold the workspace entry: %+v", view.Effective)
	}

	// Rotation changes only the value, and the old one is gone from the store.
	c.post("/api/secrets/GITHUB_TOKEN/rotate", map[string]any{"value": "rotated-value"}, nil)
	payload, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), value) {
		t.Error("rotation should replace the old value")
	}
	if !strings.Contains(string(payload), "rotated-value") {
		t.Error("rotation should write the new value")
	}
	_, body = c.do(http.MethodGet, "/api/secrets", nil)
	if strings.Contains(string(body), "rotated-value") {
		t.Fatal("a rotated value must not come back out")
	}

	// Deleting the workspace entry uncovers the global one; deleting that empties the list.
	status, body = c.do(http.MethodDelete, "/api/workspaces/personal/secrets/GITHUB_TOKEN", nil)
	if status != http.StatusOK {
		t.Fatalf("DELETE workspace secret: %d %s", status, body)
	}
	view = vaultWorkspace{}
	c.get("/api/workspaces/personal/secrets", &view)
	if len(view.Effective) != 1 || view.Effective[0].Source != "global" {
		t.Errorf("the global entry should be visible after the override is gone: %+v", view.Effective)
	}
	status, body = c.do(http.MethodDelete, "/api/secrets/GITHUB_TOKEN", nil)
	if status != http.StatusOK {
		t.Fatalf("DELETE global secret: %d %s", status, body)
	}
	listed = vaultList{}
	c.get("/api/secrets", &listed)
	if len(listed.Secrets) != 0 {
		t.Errorf("the vault should be empty, got %+v", listed.Secrets)
	}
}

func TestSecretRefusals(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	s := start(t)
	c := s.client()

	cases := []struct {
		name string
		path string
		body map[string]any
		want string
	}{
		{
			name: "lowercase name",
			path: "/api/secrets",
			body: map[string]any{"name": "token", "description": "d", "value": "v"},
			want: "uppercase",
		},
		{
			name: "reserved name",
			path: "/api/secrets",
			body: map[string]any{"name": "PATH", "description": "d", "value": "v"},
			want: "reserved",
		},
		{
			name: "no description",
			path: "/api/secrets",
			body: map[string]any{"name": "TOKEN", "value": "v"},
			want: "description",
		},
		{
			name: "no value",
			path: "/api/secrets",
			body: map[string]any{"name": "TOKEN", "description": "d"},
			want: "value",
		},
		{
			name: "any host",
			path: "/api/secrets",
			body: map[string]any{"name": "TOKEN", "description": "d", "value": "v", "hosts": []string{"*"}},
			want: "every host",
		},
		{
			name: "unknown workspace",
			path: "/api/workspaces/missing/secrets",
			body: map[string]any{"name": "TOKEN", "description": "d", "value": "v"},
			want: "no such workspace",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			status, body := c.postStatus(test.path, test.body)
			if status != http.StatusBadRequest && status != http.StatusNotFound {
				t.Fatalf("expected a refusal, got %d %s", status, body)
			}
			if !strings.Contains(body, test.want) {
				t.Errorf("refusal should mention %q, got %q", test.want, body)
			}
			if strings.Contains(body, "value") && test.want != "value" {
				t.Errorf("a refusal should not echo a value: %q", body)
			}
		})
	}

	status, body := c.do(http.MethodPost, "/api/secrets/MISSING/rotate", map[string]any{"value": "v"})
	if status != http.StatusNotFound {
		t.Errorf("rotating a missing secret should be 404, got %d %s", status, body)
	}
	status, body = c.do(http.MethodDelete, "/api/secrets/MISSING", nil)
	if status != http.StatusNotFound {
		t.Errorf("deleting a missing secret should be 404, got %d %s", status, body)
	}
}

// The CLI writes the same file the gateway reads. A running gateway picks it up on the next
// list because it does not cache the vault, and the lock keeps the two writers apart.
func TestSecretCLIWritesTheVault(t *testing.T) {
	t.Parallel()
	notSandbox(t)
	s := start(t)
	c := s.client()

	add := exec.Command(gatewayBin, "secret", "add", "--data-dir", s.DataDir, "--global",
		"--description", "created by the CLI", "--hosts", "api.example.com", "CLI_TOKEN")
	add.Stdin = strings.NewReader("cli-first-value\n")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("secret add: %v\n%s", err, out)
	}

	list := exec.Command(gatewayBin, "secret", "list", "--data-dir", s.DataDir)
	out, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("secret list: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "CLI_TOKEN") || !strings.Contains(string(out), "created by the CLI") {
		t.Errorf("list should show the name and description:\n%s", out)
	}
	if strings.Contains(string(out), "cli-first-value") {
		t.Errorf("list must not show the value:\n%s", out)
	}

	var listed vaultList
	c.get("/api/secrets", &listed)
	if len(listed.Secrets) != 1 || listed.Secrets[0].Name != "CLI_TOKEN" {
		t.Errorf("the gateway should see the CLI's secret: %+v", listed.Secrets)
	}

	rotate := exec.Command(gatewayBin, "secret", "rotate", "--data-dir", s.DataDir, "--global", "CLI_TOKEN")
	rotate.Stdin = strings.NewReader("cli-second-value\n")
	if out, err := rotate.CombinedOutput(); err != nil {
		t.Fatalf("secret rotate: %v\n%s", err, out)
	}
	payload, err := os.ReadFile(filepath.Join(s.DataDir, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "cli-second-value") || strings.Contains(string(payload), "cli-first-value") {
		t.Errorf("rotation did not replace the value:\n%s", payload)
	}
}
