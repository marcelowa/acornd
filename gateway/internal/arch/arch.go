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
	"cmd/acornfall": {
		"internal/control",
		"internal/httpapi",
		"internal/identity",
		"internal/netns",
		"internal/people",
		"internal/protocol",
		"internal/ready",
		"internal/sandbox",
		"internal/supervise",
		"internal/update",
		"internal/vault",
		"web",
	},

	// The domain: it provisions the resources it owns and supervises the worker that uses them.
	"internal/control": {
		"internal/protocol",
		"internal/sandbox",
		"internal/secretsproxy",
		"internal/vault",
		"internal/workerpool",
	},

	// The feature suite drives a real gateway binary; it reaches the engine only to install one.
	"internal/e2e": {
		"internal/sandbox",
	},

	// The edge translates HTTP into domain calls. It must not learn what an engine is: asking
	// control is the rule, and a second copy of "which engines exist" is how the two drift apart.
	"internal/httpapi": {
		"internal/control",
		"internal/identity",
		"internal/people",
		"internal/protocol",
		"internal/update",
		"internal/vault",
	},

	"internal/arch": {},
	// Who is calling, proven: the token file or a signed assertion. A leaf, because the door is a
	// fact about a request and knows nothing about workspaces or engines (ADR 0032).
	"internal/identity": {},
	"internal/netns":    {},
	// Who may use the install: a list of people, a file, and the rule that nobody unnamed gets in. A
	// leaf: it knows nothing about a workspace or an engine (ADR 0032 §3).
	"internal/people":   {},
	"internal/protocol": {},
	"internal/ready":    {"internal/update"},
	// One workspace's secrets proxy: loopback, the vault's grants handed in, nothing of this
	// project imported. It is a leaf on purpose — the package that knows the vault stays control.
	"internal/secretsproxy": {},
	"internal/sandbox":      {"internal/supervise"},
	"internal/supervise":    {},
	"internal/update":       {},
	"internal/vault":        {},
	"internal/workerpool":   {"internal/protocol"},
	"web":                   {},
}
