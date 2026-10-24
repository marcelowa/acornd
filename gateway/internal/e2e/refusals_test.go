package e2e

import (
	"strings"
	"testing"
	"time"
)

// The edge's refusals, as they are typed into a browser or a script. Every one of these is a
// request somebody can make and a sentence they get back, and each says *which* mistake it was:
// an unknown workspace, unknown route value, or a body that is not what the route takes. They are
// tested together because the subject is the transport edge's answers, not any one feature.
func TestTheEdgeRefusesWhatItCannotDoAndSaysWhy(t *testing.T) {
	t.Parallel()
	s := start(t)
	c := s.client()

	cases := []struct {
		why    string
		method string
		path   string
		body   any
		status int
		says   string
	}{
		// A workspace named in the path that does not exist is a 404 on every route that has one.
		{"a workspace that is not there", "GET", "/api/workspaces/nowhere/secrets", nil, 404, "no such workspace"},
		{"a secret listing for a workspace that is not there", "POST", "/api/workspaces/nowhere/secrets", map[string]any{"name": "K", "value": "v"}, 404, "no such workspace"},
		{"rotating in a workspace that is not there", "POST", "/api/workspaces/nowhere/secrets/K/rotate", map[string]any{"value": "v"}, 404, "no such workspace"},
		{"updating a workspace that is not there", "POST", "/api/workspaces/nowhere/update", map[string]any{"title": "x"}, 404, "no workspace"},
		// A secret request nobody is waiting for: the answer would go nowhere.
		{"an answer to no secret request", "POST", "/api/workspaces/general/conversations/1/secrets/nope", map[string]any{"value": "v"}, 404, "no secret request"},

		// Providers: a login needs to say which provider and how.
		{"a login with no provider", "POST", "/api/providers/login", map[string]any{"authType": "api_key"}, 400, "providerId"},
		{"a login of a type that does not exist", "POST", "/api/providers/login", map[string]any{"providerId": "faux", "authType": "carrier-pigeon"}, 400, "api_key or oauth"},
		{"a logout with no provider", "POST", "/api/providers/logout", map[string]any{}, 400, "providerId"},
		{"an auth answer with no prompt", "POST", "/api/auth/answer", map[string]any{"value": "v"}, 400, "promptId"},

		// A message to a conversation that does not exist is not a queue nobody drains.
		{"a message to a conversation that is not there", "POST", "/api/workspaces/general/conversations/nope/messages", map[string]any{"text": "hello"}, 409, ""},
		{"an abort of a conversation that is not there", "POST", "/api/workspaces/general/conversations/nope/abort", nil, 409, ""},

		// Body decoding: a body that is not JSON, and one that is JSON of the wrong shape.
		{"a body that is not JSON", "POST", "/api/defaults/model", "not json at all", 400, ""},
		{"an update setting that cannot be done", "POST", "/api/update/settings", map[string]any{"channel": "tree"}, 400, "releases"},
	}
	for _, one := range cases {
		status, body := c.do(one.method, one.path, one.body)
		if status != one.status {
			t.Errorf("%s: %s %s = %d, want %d (%s)", one.why, one.method, one.path, status, one.status, strings.TrimSpace(string(body)))
			continue
		}
		if one.says != "" && !strings.Contains(string(body), one.says) {
			t.Errorf("%s: answered %q, want it to mention %q", one.why, strings.TrimSpace(string(body)), one.says)
		}
	}
}

// A secret is written, listed, rotated and deleted in a workspace, and the listing tells the three
// scopes apart: what the install has, what this workspace has, and what a command here would
// actually receive. The value goes in and never comes back out — not in the listing, not in the
// answer to a write.
func TestAWorkspaceSecretIsWrittenListedRotatedAndDeleted(t *testing.T) {
	t.Parallel()
	s := start(t)
	c := s.client()

	var written map[string]any
	c.post("/api/workspaces/general/secrets", map[string]any{
		"name":        "TOKEN",
		"description": "for the api",
		"value":       "s3cret",
		"hosts":       []string{"api.example.com"},
	}, &written)
	if strings.Contains(strings.Join(anyStrings(written), " "), "s3cret") {
		t.Errorf("a value must never come back out: %v", written)
	}

	var listed struct {
		Global    []map[string]any `json:"global"`
		Own       []map[string]any `json:"own"`
		Effective []map[string]any `json:"effective"`
	}
	c.get("/api/workspaces/general/secrets", &listed)
	if len(listed.Own) != 1 || listed.Own[0]["name"] != "TOKEN" {
		t.Fatalf("own = %v", listed.Own)
	}
	if len(listed.Effective) != 1 {
		t.Errorf("effective = %v", listed.Effective)
	}

	// A rotation replaces the value and says when, without touching the rest of the entry.
	var rotated struct {
		Secret struct {
			Name  string `json:"name"`
			Hosts []string
		} `json:"secret"`
	}
	c.post("/api/workspaces/general/secrets/TOKEN/rotate", map[string]any{"value": "n3w"}, &rotated)
	if rotated.Secret.Name != "TOKEN" {
		t.Errorf("rotation answered %+v", rotated)
	}
	if len(rotated.Secret.Hosts) != 1 {
		t.Errorf("a rotation keeps the entry's hosts, got %v", rotated.Secret.Hosts)
	}

	// The same name at the install's scope shadows nothing here and appears in both lists, which
	// is the difference the screen draws.
	c.post("/api/secrets", map[string]any{"name": "GLOBAL", "description": "d", "value": "v"}, nil)
	c.get("/api/workspaces/general/secrets", &listed)
	if len(listed.Global) != 1 || len(listed.Own) != 1 || len(listed.Effective) != 2 {
		t.Errorf("global %d, own %d, effective %d", len(listed.Global), len(listed.Own), len(listed.Effective))
	}

	status, body := c.do("DELETE", "/api/workspaces/general/secrets/TOKEN", nil)
	if status != 200 && status != 204 {
		t.Errorf("delete = %d %s", status, body)
	}
	c.get("/api/workspaces/general/secrets", &listed)
	if len(listed.Own) != 0 {
		t.Errorf("after the delete, own = %v", listed.Own)
	}
}

// anyStrings flattens an answer into its values, so a test can ask whether a secret leaked into it
// without caring about its shape.
func anyStrings(value any) []string {
	var out []string
	switch typed := value.(type) {
	case map[string]any:
		for _, one := range typed {
			out = append(out, anyStrings(one)...)
		}
	case []any:
		for _, one := range typed {
			out = append(out, anyStrings(one)...)
		}
	case string:
		out = append(out, typed)
	}
	return out
}

// A workspace is edited through one endpoint, and every field it takes is checked before anything
// is written: an unknown engine, a network that is neither on nor off, and a trust flag this build
// does not have are each refused with the values that would have worked.
func TestEditingAWorkspaceChecksEveryField(t *testing.T) {
	t.Parallel()
	s := start(t)
	c := s.client()

	refusals := []struct {
		why  string
		body map[string]any
		says string
	}{
		{"an engine that does not exist", map[string]any{"engine": "qemu"}, "sandbox engine"},
		{"a network that is neither", map[string]any{"network": "maybe"}, `"on" or "off"`},
		{"a trust flag this build does not have", map[string]any{"trusted": true}, "trust"},
	}
	for _, one := range refusals {
		status, body := c.do("POST", "/api/workspaces/general/update", one.body)
		if status != 400 {
			t.Errorf("%s: %d %s", one.why, status, body)
			continue
		}
		if !strings.Contains(string(body), one.says) {
			t.Errorf("%s: answered %q, want it to mention %q", one.why, strings.TrimSpace(string(body)), one.says)
		}
	}

	// What it does accept is written and answered with the whole list, so the screen redraws from
	// one answer.
	var listed struct {
		Workspaces []struct {
			Name  string `json:"name"`
			Title string `json:"title"`
			Image string `json:"image"`
		} `json:"workspaces"`
	}
	c.post("/api/workspaces/general/update", map[string]any{"title": "The general one", "image": "alpine:3.20"}, &listed)
	found := false
	for _, workspace := range listed.Workspaces {
		if workspace.Name == "general" {
			found = true
			if workspace.Title != "The general one" {
				t.Errorf("title = %q", workspace.Title)
			}
			if workspace.Image != "alpine:3.20" {
				t.Errorf("image = %q", workspace.Image)
			}
		}
	}
	if !found {
		t.Fatal("the answer should list the workspace that was edited")
	}
}

// An update asked for while a conversation is answering is refused with a conflict, and the reason:
// the default is to wait for the run, and stopping it is a decision somebody has to make.
func TestAnUpdateWaitsForRuns(t *testing.T) {
	t.Parallel()
	source := withUpdateSource(t)
	s := start(t, source, withSlowModel(), withScript(scriptRule{Match: "keep going", Reply: "a long answer"}))
	c := s.client()
	root := c.waitForConversation("general")
	defer c.watch("general", root)()
	c.prompt("general", root, "keep going")
	// The run has to be running before the update is asked for: the gate reads what is running now,
	// and a prompt that has been accepted but not started is not a run yet.
	c.waitRunning("general", root, 30*time.Second)

	status, body := c.do("POST", "/api/update/apply", map[string]any{})
	if status != 409 {
		t.Fatalf("apply while a run is active: %d %s", status, body)
	}
	if !strings.Contains(string(body), "run") {
		t.Errorf("the refusal should say a run is in the way: %q", body)
	}
}

// Every route that works inside a workspace refuses one that is not there, in the same words and
// with the same status. It is one rule in one place — the edge resolves the workspace before the
// handler runs — and this is the test that keeps it that way: a route that reached its handler
// with a missing workspace would be a nil dereference or, worse, an answer about the wrong one.
func TestEveryWorkspaceRouteRefusesAWorkspaceThatIsNotThere(t *testing.T) {
	t.Parallel()
	s := start(t)
	c := s.client()

	routes := []struct {
		method string
		path   string
		body   any
		// want is 409 everywhere but the status route, which answers 404: it is the one route that
		// is *about* a workspace existing, so "not found" is its own answer rather than a conflict
		// with something else.
		want int
	}{
		{"GET", "/api/workspaces/nowhere/state", nil, 409},
		{"GET", "/api/workspaces/nowhere/status", nil, 404},
		{"GET", "/api/workspaces/nowhere/conversations", nil, 409},
		{"POST", "/api/workspaces/nowhere/conversations", nil, 409},
		{"GET", "/api/workspaces/nowhere/agents", nil, 409},
		{"GET", "/api/workspaces/nowhere/commands", nil, 409},
		{"GET", "/api/workspaces/nowhere/usage", nil, 409},
		{"POST", "/api/workspaces/nowhere/conversations/1/messages", map[string]any{"text": "hello"}, 409},
		{"POST", "/api/workspaces/nowhere/conversations/1/draft", map[string]any{"text": "hi"}, 409},
		{"POST", "/api/workspaces/nowhere/conversations/1/abort", nil, 409},
		{"POST", "/api/workspaces/nowhere/attachments", nil, 409},
		{"POST", "/api/workspaces/nowhere/providers/login", map[string]any{"providerId": "faux", "authType": "api_key"}, 409},
		{"POST", "/api/workspaces/nowhere/providers/logout", map[string]any{"providerId": "faux"}, 409},
		{"POST", "/api/workspaces/nowhere/providers/refresh", nil, 409},
		{"POST", "/api/workspaces/nowhere/auth/answer", map[string]any{"promptId": "p", "value": "v"}, 409},
		{"POST", "/api/workspaces/nowhere/auth/cancel", map[string]any{"promptId": "p"}, 409},
		{"POST", "/api/workspaces/nowhere/default-model", map[string]any{"provider": "faux", "modelId": "m"}, 409},
		{"GET", "/api/workspaces/nowhere/conversations/1/policy", nil, 409},
	}
	for _, route := range routes {
		status, body := c.do(route.method, route.path, route.body)
		if status != route.want {
			t.Errorf("%s %s = %d, want %d (%s)", route.method, route.path, status, route.want, strings.TrimSpace(string(body)))
		}
	}
}
