package e2e

import (
	"strings"
	"testing"
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
