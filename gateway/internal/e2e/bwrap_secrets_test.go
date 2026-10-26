package e2e

import (
	"strings"
	"testing"
	"time"
)

// A bwrap workspace's secret reaches an API without the guest ever holding it: the environment holds
// the placeholder, the proxy the gateway runs for the workspace swaps in the value for the host the
// secret names, and the sandbox trusts that proxy because its bundle was installed.
//
// This is the tier that can show it. The fast suite proves the proxy against a fake origin and the
// driver's argv against a fake runner; what is proved here is that the halves are wired to each
// other — pasta's forward, the trust bundle, HTTPS_PROXY, and a real client in a real namespace.
func TestABwrapWorkspaceSubstitutesASecretAtItsProxy(t *testing.T) {
	t.Parallel()
	bwrapEngine(t)
	const value = "e2e-bwrap-proxy-value"
	const placeholder = "$MSB_E2E_TOKEN"
	s := start(t,
		withSetup(func(t *testing.T, s *system) {
			writeVault(t, s.DataDir, []map[string]any{{
				"name":         "E2E_TOKEN",
				"description":  "a token a command uses",
				"scope":        "global",
				"value":        value,
				"hosts":        []string{"httpbin.org"},
				"substitution": map[string]bool{"headers": true},
				"allowEnv":     false,
				"rotatedAt":    time.Now().UTC().Format(time.RFC3339Nano),
			}})
		}),
		withScript(
			scriptRule{
				Match: "use the token",
				Tools: []scriptTool{{Name: "bash", Args: map[string]any{
					// The first printf is what the guest holds; the request is what the proxy does
					// with it. httpbin echoes the headers it received, so the answer is the proof
					// that the upstream saw the value and the guest never did.
					//
					// apt goes direct: its own traffic is not what this test is about, and a package
					// fetch that had to present the proxy's session token would be a second thing
					// that could fail for a reason of its own.
					"command": `printf 'in-the-sandbox:[%s] ' "$E2E_TOKEN"; ` +
						`(command -v curl >/dev/null 2>&1 || (` +
						`apt-get -o Acquire::http::Proxy="DIRECT" update -qq >/dev/null 2>&1 && ` +
						`apt-get -o Acquire::http::Proxy="DIRECT" install -y -qq curl >/dev/null 2>&1)); ` +
						`curl -sS --max-time 30 -H "Authorization: Bearer $E2E_TOKEN" https://httpbin.org/headers | tr -d '\n'`,
				}}},
				Reply: "the request ran",
			},
		),
	)
	c := s.client()
	root := c.waitForConversation("personal")

	c.prompt("personal", root, "use the token")
	got := c.waitForText("personal", root, "the request ran")
	output := got.lastToolResult()

	if !strings.Contains(output, "in-the-sandbox:["+placeholder+"]") {
		t.Fatalf("the guest should hold the placeholder, got %q", output)
	}
	if !strings.Contains(output, value) {
		t.Fatalf("the upstream should have received the value, got %q", output)
	}
	// The failure that this test exists to catch looks like a successful command: curl failing to
	// reach the proxy, or the proxy failing to trust the host, still prints something.
	for _, failure := range []string{"Connection refused", "proxy connect", "Could not resolve", "certificate"} {
		if strings.Contains(output, failure) {
			t.Fatalf("the request should have reached the upstream, got %q", output)
		}
	}
}
