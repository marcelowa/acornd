// Package arch holds the gateway's import boundary as data, so an import that crosses a layer
// fails the build instead of waiting for a reviewer to notice it.
//
// The rows are ADR 0002's table, not this file's opinion: control owns workspaces and the
// worker, the edge translates HTTP into domain calls, and the engine package supervises the
// processes it owns. A package that needs a new edge adds a row and says why — that edit is the
// review, and it is the only way the boundary is allowed to move.
package arch

// Allowed is what each package may import from inside this module, and nothing else. Every
// directory holding Go files needs a row, including test-only ones: an unlisted package fails,
// so adding one is a decision rather than an accident. A row that lists an import the package
// no longer uses fails too, because a permissive row silently outlives the reason for it.
var Allowed = map[string][]string{
	// The entry point constructs every layer; it is the one place allowed to know them all.
	"cmd/acornd": {
		"internal/cloudflared",
		"internal/control",
		"internal/httpapi",
		"internal/identity",
		"internal/netns",
		"internal/people",
		"internal/protocol",
		"internal/remote",
		"internal/ready",
		"internal/sandbox",
		"internal/sandbox/bwrap",
		"internal/sandbox/microsandbox",
		"internal/sandbox/youki",
		"internal/supervise",
		"internal/support",
		"internal/update",
		"internal/vault",
		"web",
	},

	// The domain: it provisions the resources it owns and supervises the worker that uses them.
	// It also asks support for a workspace diagnosis and records the running build in it; both
	// are leaves, and neither knows how a workspace is opened.
	"internal/control": {
		"internal/protocol",
		"internal/sandbox",
		"internal/sandbox/bwrap",
		"internal/sandbox/microsandbox",
		"internal/secretsproxy",
		"internal/support",
		"internal/update",
		"internal/vault",
		"internal/workerpool",
	},

	// The feature suite drives a real gateway binary; it reaches the engine only to install one, and the
	// door's credential file to sign its clients in — a console with no proof answers nobody (ADR 0036 §3).
	"internal/e2e": {
		"internal/identity",
		"internal/sandbox",
		"internal/sandbox/microsandbox",
		"internal/sandbox/youki",
	},

	// The edge translates HTTP into domain calls. It must not learn what an engine is: asking
	// control is the rule, and a second copy of "which engines exist" is how the two drift apart.
	// It reaches `remote` for the two calls the remote-access screen makes: that package is a leaf
	// that talks to Cloudflare, and control does not own a tunnel (ADR 0033 §2).
	// The edge's one look at readiness: the door asks whether this install has a provider key, because a door that
	// opens onto a half-configured console is not a door (ADR 0036 §3). It uses ready's reader rather than a
	// second parse of the same file.
	"internal/httpapi": {
		"internal/control",
		"internal/remote",
		"internal/identity",
		"internal/people",
		"internal/protocol",
		"internal/ready",
		"internal/support",
		"internal/update",
		"internal/vault",
	},

	"internal/arch": {},
	// The connector: a pinned binary, and the process that keeps it dialling out. A leaf — it knows how to
	// fetch a file, hash it, run it and restart it, and nothing about workspaces, people or Cloudflare's API
	// (ADR 0033 §5). The tunnel's token is handed in by cmd, which is the only layer that reads remote.json.
	"internal/cloudflared": {},
	// Who is calling, proven: the token file or a signed assertion. A leaf, because the door is a
	// fact about a request and knows nothing about workspaces or engines (ADR 0032).
	"internal/identity": {},
	"internal/netns":    {},
	// Who may use the install: a list of people, a file, and the rule that nobody unnamed gets in. A
	// leaf: it knows nothing about a workspace or an engine (ADR 0032 §3).
	"internal/people":   {},
	"internal/protocol": {},
	"internal/ready":    {"internal/update"},
	// What carries a browser to the console: the tunnel, its record, and the Access application that admits the
	// book. A leaf — it talks to Cloudflare and to nothing else (ADR 0033).
	"internal/remote": {},
	// One workspace's secrets proxy: loopback, the vault's grants handed in, nothing of this
	// project imported. It is a leaf on purpose — the package that knows the vault stays control.
	"internal/secretsproxy": {},
	// The engines and their drivers. It reaches netns for the test binary's netns helper, which
	// runs the same configuration the product's acornd runs before a sandbox's first process
	// (ADR 0025); the alternative was a second copy of the refused ranges in the test.
	"internal/sandbox":              {"internal/sandbox/sandboxtest"},
	"internal/sandbox/bwrap":        {"internal/netns", "internal/sandbox", "internal/sandbox/sandboxtest", "internal/supervise"},
	"internal/sandbox/microsandbox": {"internal/sandbox"},
	"internal/sandbox/sandboxtest":  {},
	"internal/sandbox/youki":        {"internal/netns", "internal/sandbox", "internal/sandbox/sandboxtest", "internal/supervise"},
	"internal/supervise":            {},
	// The support bundle collector: records, cgroups, and redaction. It knows the sandbox naming
	// scheme, and nothing about control.
	"internal/support":    {"internal/sandbox"},
	"internal/update":     {},
	"internal/vault":      {},
	"internal/workerpool": {"internal/protocol"},
	"web":                 {},
}
