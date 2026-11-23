// Package arch holds the gateway's import boundary as data, so an import that crosses a layer
// fails the build instead of waiting for a reviewer to notice it.
//
// The rows are ADR 0002's table, not this file's opinion: control owns workspaces and the
// worker, the edge translates HTTP into domain calls, and the engine package supervises the
// processes it owns. A package that needs a new edge adds a row and says why — that edit is the
// review, and it is the only way the boundary is allowed to move.
package arch

// Allowed is what each package's production files may import from inside this module, and
// nothing else. Every directory holding Go files needs a row, including test-only ones: an
// unlisted package fails, so adding one is a decision rather than an accident. A row that lists an
// import no production file uses fails too, because a permissive row silently outlives the reason
// for it. The edges only a _test.go file takes are in AllowedInTests, below.
var Allowed = map[string][]string{
	// The entry point constructs every layer; it is the one place allowed to know them all.
	"cmd/acornd": {
		"internal/cloudflared",
		"internal/control",
		"internal/httpapi",
		"internal/identity",
		"internal/layout",
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
	// are leaves, and neither knows how a workspace is opened. It asks layout where the registry, the install's
	// settings and each workspace's state folder are, and writes the registry with layout's lock and write.
	// It names an engine by its string and imports none: cmd/acornd registers the drivers into sandbox's table
	// before anything can choose one (house rule G3, bwrap/register.go).
	"internal/control": {
		"internal/layout",
		"internal/protocol",
		"internal/sandbox",
		"internal/secretsproxy",
		"internal/support",
		"internal/update",
		"internal/vault",
		"internal/workerpool",
	},

	// The feature suite is test-only: it has no production file, so its row is empty. Its edges are in AllowedInTests.
	"internal/e2e": {},

	// The edge translates HTTP into domain calls. It must not learn what an engine is: asking
	// control is the rule, and a second copy of "which engines exist" is how the two drift apart.
	// It reaches `remote` for the two calls the remote-access screen makes: that package is a leaf
	// that talks to Cloudflare, and control does not own a tunnel (ADR 0033 §2).
	// The edge's one look at readiness: the door asks whether this install has a provider key, because a door that
	// opens onto a half-configured console is not a door (ADR 0036 §3). It uses ready's reader rather than a
	// second parse of the same file. It asks layout where the install's settings file is, for the same check.
	"internal/httpapi": {
		"internal/control",
		"internal/layout",
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
	// The install folder's file layout: every file's name, the atomic write, and the lock two processes take
	// before they change a file. A leaf below every package that keeps a file there, so none of them spells a
	// name or locks a file its own way.
	"internal/layout": {},
	"internal/netns":  {},
	// Who may use the install: a list of people, a file, and the rule that nobody unnamed gets in. A
	// leaf: it knows nothing about a workspace or an engine (ADR 0032 §3). It locks and writes its file with
	// layout's lock and write, which the vault and the registry share.
	"internal/people":   {"internal/layout"},
	"internal/protocol": {},
	"internal/ready":    {"internal/update"},
	// What carries a browser to the console: the tunnel, its record, and the Access application that admits the
	// book. A leaf — it talks to Cloudflare and to nothing else (ADR 0033).
	"internal/remote": {"internal/layout"},
	// One workspace's secrets proxy: loopback, the vault's grants handed in, nothing of this
	// project imported. It is a leaf on purpose — the package that knows the vault stays control.
	"internal/secretsproxy": {},
	// The engines and their drivers. A driver names the sandbox interface and nothing else in production.
	// It asks layout where the install's config file is, the one file of the install folder a sandbox mounts.
	"internal/sandbox":              {"internal/layout"},
	"internal/sandbox/bwrap":        {"internal/sandbox", "internal/supervise"},
	"internal/sandbox/microsandbox": {"internal/sandbox"},
	"internal/sandbox/sandboxtest":  {},
	"internal/sandbox/youki":        {"internal/sandbox", "internal/supervise"},
	"internal/supervise":            {},
	// The support bundle collector: records, cgroups, and redaction. It knows the sandbox naming
	// scheme, and nothing about control.
	// It asks layout where the registry is, to copy one workspace's record.
	"internal/support": {"internal/layout", "internal/sandbox"},
	// It asks layout which files a backup copies and which secret stores it scrubs.
	"internal/update": {"internal/layout"},
	// It locks and writes secrets.json with layout's lock and write.
	"internal/vault":      {"internal/layout"},
	"internal/workerpool": {"internal/protocol"},
	"web":                 {},
}

// AllowedInTests is what a package's _test.go files may import from inside this module, on top of
// its Allowed row. A test is its own entry point, so it may reach an engine or a helper that
// production does not. Those edges live here, so a production import of one still fails. Like
// Allowed, a row that lists an import no test file uses fails.
var AllowedInTests = map[string][]string{
	// The domain's tests register the two engines they drive: a test binary is its own entry point,
	// so its TestMain registers the drivers, which production gets from cmd/acornd.
	"internal/control": {"internal/sandbox/bwrap", "internal/sandbox/microsandbox"},
	// The feature suite drives a real gateway binary; it reaches the engine only to install one, and the
	// door's credential file to sign its clients in — a console with no proof answers nobody (ADR 0036 §3).
	"internal/e2e": {"internal/identity", "internal/sandbox", "internal/sandbox/microsandbox", "internal/sandbox/youki"},
	// The sandbox package's tests use the helpers in sandboxtest.
	"internal/sandbox": {"internal/sandbox/sandboxtest"},
	// The engines' tests reach netns for the test binary's netns helper, which runs the same
	// configuration the product's acornd runs before a sandbox's first process (ADR 0025); the
	// alternative was a second copy of the refused ranges in the test.
	"internal/sandbox/bwrap": {"internal/netns", "internal/sandbox/sandboxtest"},
	"internal/sandbox/youki": {"internal/netns", "internal/sandbox/sandboxtest"},
}
