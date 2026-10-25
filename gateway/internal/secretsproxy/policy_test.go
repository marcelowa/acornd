package secretsproxy

import (
	"net/http"
	"strings"
	"testing"
)

// The policy is the part that decides, and it is a pure function of the request and the grants.
// These tests are the table of situations: each one is a request, and the verdict it deserves.

func TestDecideForwardsARequestWithNoPlaceholder(t *testing.T) {
	t.Parallel()
	grants := []Grant{{Name: "TOKEN", Value: "v", Placeholder: "$MSB_TOKEN", Hosts: []string{"example.com"}, Headers: true}}
	header := http.Header{"Authorization": []string{"Bearer something-else"}}

	if got := decide(grants, "example.com", header, "", nil); got.action != forward {
		t.Errorf("a request with no placeholder should be forwarded, got %v", got.action)
	}
}

// A grant with no placeholder cannot match anything: the vault refuses to store a substitute
// secret without one, and this is the second line of that defence.
func TestDecideSkipsAGrantWithNoPlaceholder(t *testing.T) {
	t.Parallel()
	grants := []Grant{{Name: "TOKEN", Value: "v", Hosts: []string{"example.com"}, Headers: true}}
	if got := decide(grants, "example.com", http.Header{}, "", nil); got.action != forward {
		t.Errorf("a grant with no placeholder should match nothing, got %v", got.action)
	}
}

func TestDecideRefusesEveryDisabledLocation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		grant  Grant
		header http.Header
		query  string
		body   []byte
		reason string
	}{
		{
			name:   "header disabled",
			grant:  Grant{Name: "TOKEN", Placeholder: "$MSB_TOKEN", Hosts: []string{"example.com"}, Query: true},
			header: http.Header{"Authorization": []string{"Bearer $MSB_TOKEN"}},
			reason: "header",
		},
		{
			name:   "query disabled",
			grant:  Grant{Name: "TOKEN", Placeholder: "$MSB_TOKEN", Hosts: []string{"example.com"}, Headers: true},
			query:  "api_key=$MSB_TOKEN",
			reason: "query",
		},
		{
			name:   "body disabled",
			grant:  Grant{Name: "TOKEN", Placeholder: "$MSB_TOKEN", Hosts: []string{"example.com"}, Headers: true},
			body:   []byte(`{"token":"$MSB_TOKEN"}`),
			reason: "body",
		},
		{
			name:   "host not named",
			grant:  Grant{Name: "TOKEN", Placeholder: "$MSB_TOKEN", Hosts: []string{"api.example.com"}, Headers: true},
			header: http.Header{"Authorization": []string{"Bearer $MSB_TOKEN"}},
			reason: "not allowed to reach",
		},
	}
	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()
			got := decide([]Grant{one.grant}, "example.com", one.header, one.query, one.body)
			if got.action != refuse {
				t.Fatalf("want a refusal, got %v", got.action)
			}
			if !strings.Contains(got.reason, one.reason) {
				t.Errorf("the reason should mention %q: %q", one.reason, got.reason)
			}
			if got.reason == "" || strings.Contains(got.reason, "SECRET-VALUE") {
				t.Errorf("a reason is a sentence about a name, not a value: %q", got.reason)
			}
		})
	}
}

// The proxy's own credential is not the workspace's business: a token that happened to contain a
// placeholder must neither be rewritten nor refused.
func TestDecideAndRewriteIgnoreTheProxysOwnCredential(t *testing.T) {
	t.Parallel()
	grants := []Grant{{Name: "TOKEN", Value: "real", Placeholder: "$MSB_TOKEN", Hosts: []string{"example.com"}, Headers: true}}
	header := http.Header{"Proxy-Authorization": []string{"Basic $MSB_TOKEN"}}
	if got := decide(grants, "example.com", header, "", nil); got.action != forward {
		t.Fatalf("a placeholder only in Proxy-Authorization should not be substituted, got %v", got.action)
	}
	query, body := "$MSB_TOKEN=1", []byte("$MSB_TOKEN")
	rewrite(header, &query, &body, grants)
	if header.Get("Proxy-Authorization") != "Basic $MSB_TOKEN" {
		t.Errorf("the proxy's own credential was rewritten: %q", header.Get("Proxy-Authorization"))
	}
}

// A grant's enabled locations are the only ones touched, and the ones it does not enable keep
// their text. This is the shape a partially configured secret has to have: header only, and the
// query left exactly as the client wrote it.
func TestRewriteTouchesOnlyTheEnabledLocations(t *testing.T) {
	t.Parallel()
	grants := []Grant{{Name: "TOKEN", Value: "real", Placeholder: "$MSB_TOKEN", Hosts: []string{"example.com"}, Headers: true}}
	header := http.Header{"Authorization": []string{"Bearer $MSB_TOKEN"}}
	query := "left=$MSB_TOKEN"
	body := []byte("$MSB_TOKEN")

	rewrite(header, &query, &body, grants)

	if got := header.Get("Authorization"); got != "Bearer real" {
		t.Errorf("the header should be rewritten: %q", got)
	}
	if query != "left=$MSB_TOKEN" {
		t.Errorf("a disabled query was rewritten: %q", query)
	}
	if string(body) != "$MSB_TOKEN" {
		t.Errorf("a disabled body was rewritten: %q", body)
	}
}
