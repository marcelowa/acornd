package control

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcelowa/acornd/gateway/internal/protocol"
)

// The wire is written by hand in three places: here, worker/src/protocol.ts and ui/src/app/wire.ts.
// Nothing compared them, so a field renamed in Go passed every test until something failed at run time.
// These tests write what Go actually sends into fixtures/wire, and the worker's and the interface's tests
// read the same files. A rename or a removal here changes a fixture, and the other two languages fail on it.
//
// UPDATE_FIXTURES=1 rewrites the files; without it a difference fails, so a change to the wire is a change
// somebody saw in the diff.

const fixturesDir = "../../../fixtures/wire"

func checkFixture(t *testing.T, name string, value any) {
	t.Helper()
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "\t")
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixturesDir, name)
	if os.Getenv("UPDATE_FIXTURES") != "" {
		if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\nrun UPDATE_FIXTURES=1 go test ./internal/control to write it", err)
	}
	if !bytes.Equal(want, out.Bytes()) {
		t.Errorf("%s differs from what Go sends now.\nIf the wire changed on purpose, run UPDATE_FIXTURES=1 go test ./internal/control, then update worker/src/protocol.ts and ui/src/app/wire.ts in the same commit (S6).", name)
	}
}

func TestCommandFixturesMatchTheWire(t *testing.T) {
	author := &protocol.Author{Email: "ada@example.com", Name: "Ada", Speaker: "Ada"}
	binding := &protocol.ModelBinding{
		Provider: "anthropic", ModelID: "claude-sonnet-5-5", ThinkingLevel: "high",
		Instructions: "Be brief.", Definition: "general",
	}
	secrets := []protocol.SecretInfo{{
		Name: "GITHUB_TOKEN", Description: "for gh", Scope: "workspace", Hosts: []string{"github.com"},
		Delivery: "substitute", Status: "ok", Placeholder: "ph_123",
	}}
	c := func(kind string) protocol.Command { return protocol.Command{ID: "c1", Type: kind} }
	with := func(kind string, set func(*protocol.Command)) protocol.Command {
		command := c(kind)
		set(&command)
		return command
	}
	conv := func(command *protocol.Command) { command.ConversationID = "7" }

	commands := map[string]protocol.Command{}
	for _, kind := range []string{"describe", "conversations", "create", "agents", "commands", "reload", "usage", "shutdown"} {
		commands[kind] = c(kind)
	}
	for _, kind := range []string{"compact", "compact_preview", "reset", "attach", "detach", "policy", "messages"} {
		commands[kind] = with(kind, conv)
	}
	commands["rename"] = with("rename", func(x *protocol.Command) { conv(x); x.Title = "A title" })
	commands["hide"] = with("hide", func(x *protocol.Command) { conv(x); x.Hidden = true })
	commands["fork"] = with("fork", func(x *protocol.Command) { conv(x); x.At = "12" })
	commands["action"] = with("action", func(x *protocol.Command) {
		conv(x)
		x.Text = "/compact"
		x.Action = "compact"
		x.Author = author
	})
	commands["open"] = with("open", func(x *protocol.Command) { conv(x); x.Agent = binding })
	commands["prompt"] = with("prompt", func(x *protocol.Command) {
		conv(x)
		x.Text = "hello"
		x.WhenBusy = protocol.WhenBusySteer
		x.Attachments = []string{"a1"}
		x.Author = author
	})
	commands["attachment"] = with("attachment", func(x *protocol.Command) {
		x.FileName = "notes.txt"
		x.MIME = "text/plain"
		x.FileSize = 12
		x.TempPath = "/tmp/acornd-upload-1"
	})
	commands["abort"] = with("abort", func(x *protocol.Command) { conv(x); x.Author = author })
	commands["expand"] = with("expand", func(x *protocol.Command) { x.Text = "/review" })
	commands["login"] = with("login", func(x *protocol.Command) {
		x.AuthID = "l1"
		x.ProviderID = "anthropic"
		x.AuthType = "oauth"
	})
	commands["logout"] = with("logout", func(x *protocol.Command) { x.ProviderID = "anthropic" })
	commands["auth_answer"] = with("auth_answer", func(x *protocol.Command) {
		x.AuthID = "l1"
		x.PromptID = "p1"
		x.Value = "abc"
		x.Cancelled = true
	})
	commands["auth_cancel"] = with("auth_cancel", func(x *protocol.Command) { x.AuthID = "l1" })
	commands["secret_answer"] = with("secret_answer", func(x *protocol.Command) {
		x.SecretRequestID = "s1"
		x.Cancelled = true
		x.SecretScope = "workspace"
		x.RestartRequired = true
		x.Reason = "declined"
	})
	commands["secret_list_answer"] = with("secret_list_answer", func(x *protocol.Command) {
		x.SecretRequestID = "s1"
		x.Secrets = secrets
		x.Reason = "vault locked"
	})
	commands["sandbox_wake"] = with("sandbox_wake", func(x *protocol.Command) {
		x.ReplyTo = "f1"
		x.Error = "no sandbox"
	})

	encoded := map[string]json.RawMessage{}
	for kind, command := range commands {
		line, err := command.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		encoded[kind] = line
	}
	checkFixture(t, "commands.json", encoded)
}

func TestFrameFixturesMatchTheWire(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	auth := func(event protocol.AuthEvent) protocol.Frame {
		return protocol.Frame{Type: "auth", AuthID: "l1", Event: &event}
	}
	frames := map[string]protocol.Frame{
		"response_ok":    {Type: "response", ID: "c1", OK: true, Data: raw(`{"provider":"pi"}`)},
		"response_error": {Type: "response", ID: "c1", Error: "no such conversation"},
		"view": {
			Type: "view", ConversationID: "7", Seq: 3, Value: raw(`{"entries":[]}`),
			Acornd: raw(`{"senders":{"8":{"conversationId":"8","name":"helper","kind":"ask"}}}`),
		},
		"submission":     {Type: "submission", ConversationID: "7", SubmissionID: "u1", Status: "accepted"},
		"secret_request": {Type: "secret_request", ID: "f1", ConversationID: "7", Name: "TOKEN", Description: "for gh", Hosts: []string{"github.com"}},
		"secret_list":    {Type: "secret_list", ID: "f2", ConversationID: "7"},
		"sandbox_wake":   {Type: "sandbox_wake", ID: "f3"},
		"secret_event": {Type: "secret", ConversationID: "7", Secret: &protocol.SecretEvent{
			Kind: "request", RequestID: "s1", ConversationID: "7", Name: "TOKEN", Description: "for gh",
			Hosts: []string{"github.com"}, Scope: "workspace", RestartRequired: true, Reason: "declined",
		}},
		"auth_prompt": auth(protocol.AuthEvent{
			Kind: "prompt", PromptID: "p1", PromptType: "select", Message: "Pick one", Placeholder: "code",
			Options: []protocol.AuthOption{{ID: "a", Label: "A", Description: "first"}},
		}),
		"auth_info":             auth(protocol.AuthEvent{Kind: "info", Message: "hello", Links: []protocol.AuthLink{{URL: "https://example.com", Label: "docs"}}}),
		"auth_auth_url":         auth(protocol.AuthEvent{Kind: "auth_url", URL: "https://example.com/login", Instructions: "open it"}),
		"auth_device_code":      auth(protocol.AuthEvent{Kind: "device_code", UserCode: "ABCD", VerificationURI: "https://example.com/device", IntervalSeconds: 5, ExpiresInSeconds: 900}),
		"auth_progress":         auth(protocol.AuthEvent{Kind: "progress", Message: "waiting"}),
		"auth_prompt_cancelled": auth(protocol.AuthEvent{Kind: "prompt_cancelled", PromptID: "p1"}),
		"auth_done":             auth(protocol.AuthEvent{Kind: "done", ProviderID: "anthropic"}),
		"auth_error":            auth(protocol.AuthEvent{Kind: "error", Message: "denied"}),
	}
	checkFixture(t, "frames.json", frames)
}

func TestStateFixtureMatchesTheWire(t *testing.T) {
	binding := protocol.ModelBinding{
		Provider: "anthropic", ModelID: "claude-sonnet-5-5", ThinkingLevel: "high", ThinkingRequested: "xhigh",
		Instructions: "Be brief.", Definition: "general",
	}
	scope := ScopeDefaults{Model: binding, Thinking: "high", Agent: "general"}
	yes := true
	workspace := WorkspaceState{
		Name: "home", Path: "/home/ada/work", Title: "Home", Personal: true, Kind: "personal",
		Members: []Member{{Email: "ada@example.com", Role: RoleOwner}}, Image: "alpine", Network: "open",
		Private: true, KeepAlive: true, Engine: "bwrap", DisableVMNesting: &yes, DisableDevices: &yes,
		Running: true, CanAdminister: true, Status: "ready", Stage: "attaching", Error: "none yet",
	}
	conversation := Conversation{
		ID: "7", Title: "First", Root: true, Hidden: true, Running: true, LastEntryID: "12",
		UpdatedAt: 1700000000000, Tokens: 10, Cost: 0.5, Parent: "1", Children: []string{"8"},
		Model: "anthropic/claude-sonnet-5-5", Thinking: "high", CreatedBy: "1",
	}
	layer := protocol.LayerTrace{Layer: "global", Path: "/etc/acornd/defaults.json", Contributes: json.RawMessage(`{"model":"x"}`)}
	state := State{
		Workspace: workspace, Workspaces: []WorkspaceState{workspace}, WorkspacesDir: "/home/ada/work",
		Engines: []EngineInfo{{
			Name: "bwrap", Default: true, Isolated: true, Installed: true, Version: "0.11", Substitutes: true, Problem: "none",
		}},
		Harness:      "pi",
		Capabilities: protocol.Capabilities{Steer: true, Subagents: true, Sandbox: "bwrap"},
		Conversation: &conversation, Conversations: []Conversation{conversation},
		Agent: binding,
		Resolved: &protocol.ResolvedAgent{
			Name: "general", Description: "d", Source: "built-in", Model: "m", Thinking: "high",
			Instructions: "i", Skills: []string{"s"}, AllowTools: []string{"read"}, DenyTools: []string{"bash"},
			Guards: []protocol.Guard{{Tool: "bash", Arg: "command", Pattern: "rm", Outside: true, Reason: "no"}},
			Layers: []protocol.LayerTrace{layer},
		},
		DefaultModel: binding, DefaultThinking: "high", DefaultAgent: "general",
		DefaultsOrigin:    DefaultOrigins{Model: "install", Thinking: "workspace", Agent: "install"},
		WorkspaceDefaults: scope, InstallDefaults: scope,
		Inherited: Inherited{Model: true, Thinking: true, Agent: true},
		Subagent:  true, Policy: json.RawMessage(`{"canAsk":true}`),
		Draft: &Draft{Text: "half", Attachments: []DraftAttachment{{ID: "a1", Name: "n.txt", Size: 3, MIME: "text/plain"}}, UpdatedAt: 1700000000001},
		Can:   Can{MakeRoom: true, Create: true, Fork: true, Hide: true, Rename: true, Model: true, Administer: true},
		Role:  "owner", Admin: true, WorkerUp: true, WorkerError: "died",
		Sandbox: SandboxState{
			Engine: "bwrap", Version: "0.11", Status: "ready", Tools: true, Isolated: true, Problem: "none",
			Nested: true, NestedReason: "ok",
			Secrets: []SandboxSecret{{
				Name: "TOKEN", Description: "d", Scope: "workspace", Hosts: []string{"github.com"},
				Delivery: "substitute", Status: "ok", Placeholder: "ph",
			}},
			SecretsRestart: true, SecretsProblem: "locked",
		},
		Update: UpdateSummary{Current: "1.0.0", Latest: "1.1.0", Available: true, Failed: "x", RestartRequired: true},
		Agents: []protocol.AgentSummary{{
			Name: "general", Description: "d", Source: "built-in", Layer: "global",
			Resolved: protocol.ResolvedAgent{Name: "general", Skills: []string{}, Layers: []protocol.LayerTrace{}},
		}},
		Skills:   []protocol.SkillSummary{{Name: "s", Description: "d"}},
		Defaults: []protocol.LayerTrace{layer},
		Providers: []protocol.ProviderInfo{{
			ID: "anthropic", Name: "Anthropic", Authenticated: true, Source: "env", AuthType: "api_key",
			AuthTypes: []string{"api_key", "oauth"},
			Models:    []protocol.ModelInfo{{ID: "m", Name: "M", ContextWindow: 200000, ThinkingLevels: []string{"off", "high"}}},
		}},
		Seq: 9,
	}
	checkFixture(t, "state.json", state)
}
