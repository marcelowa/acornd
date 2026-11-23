# Architecture

How the parts fit, as the code is now. The why is in the ADRs (`docs/adr/`); this file links them
and does not copy them. The rules are in `docs/engineering/standards.md`.

Three programs make one install:

```
 browser ──HTTP + SSE──► gateway (Go) ──JSONL on stdin/stdout──► worker (bun, one per workspace)
   ui/                   gateway/                                   worker/
                            │                                          │
                            └── starts and supervises ──► sandbox ◄────┘ attaches with the engine's SDK
```

ADR 0002 has the original layer table. Its HTTP table is v0; this file is current.

## The layers

| Layer | Where | Job |
|---|---|---|
| UI | `ui/src` | Draws state, sends intents. Knows URLs only in `ui/src/app/client.ts`. |
| Edge | `gateway/internal/httpapi` | Turns HTTP into calls on `control`. Auth, routes, the SSE stream. Does not know what an engine is (ADR 0032). |
| Domain | `gateway/internal/control` | Workspaces, conversations, defaults, secrets, the worker's life. The only package that knows both the edge's needs and the engines (G17). |
| Engines | `gateway/internal/sandbox` and `sandbox/bwrap`, `sandbox/youki`, `sandbox/microsandbox` | Make, start and stop the sandbox a worker runs in (ADR 0012). |
| Worker | `worker/src` | One harness over one storage. Runs the agent loop and its tools inside the sandbox. |
| Leaves | `identity`, `people`, `vault`, `remote`, `cloudflared`, `update`, `support`, `ready`, `secretsproxy`, `supervise`, `netns`, `layout`, `protocol` | Each does one thing and imports almost nothing. |

`gateway/cmd/acornd` is the one binary: flags, wiring, the command line (`start`, `setup`,
`doctor`, `workspace`, ...). It is the only place allowed to import every layer.

### Who may import whom

`gateway/internal/arch/arch.go` holds the boundary as data, and `arch_test.go` (run by `make lint`)
enforces it:

- `Allowed`: what each package's **production** files may import from this module. Every
  directory with Go files needs a row. A row entry no production file uses fails.
- `AllowedInTests`: what a package's `_test.go` files may import **on top of** its `Allowed` row. A
  test is its own entry point, so it may reach an engine that production may not.
- An entry in both fails: the test-only entry is dropped, because `Allowed` already covers it.

Adding a row or an entry is a decision. Write the reason in the row.

## State in `~/.acornd`

`internal/layout` owns every file name in the install folder (ADR 0041). No other package spells
one. It also owns the two tools a writer needs:

- `layout.Lock(path)`: an exclusive `flock` on `path + ".lock"`, held across one read-modify-write.
  It waits at most `layout.LockBudget` (2 s), then fails and names the file. `layout.LockShared` is
  the reader's side.
- `layout.WriteFile(path, payload)`: writes a temp file beside `path`, syncs, renames over it. A
  reader sees the old file or the new one.

| File | Holds | Written by | Under |
|---|---|---|---|
| `workspaces.json` | which workspaces exist, where | `control.Registry.Change`; the gateway and `acornd workspace` both call it | `layout.Lock` |
| `settings.json` (install, and one per workspace state folder) | default model, thinking, agent, provider models | `control.ChangeDefaults`; the console and `acornd model` | `layout.Lock` |
| `secrets.json` | the vault (ADR 0021) | `internal/vault` | `layout.Lock` / `layout.LockShared` |
| `people.json` | who may use the install (ADR 0032) | `internal/people` | `layout.Lock` |
| `remote.json` | tunnel request and token (ADR 0033) | `internal/remote` | `layout.WriteFile`, no lock |
| `update.json` | self-update settings (ADR 0019) | `internal/update` | none (`os.WriteFile`) |
| `config.json` | install-wide config, mounted read-only into every sandbox (ADR 0014) | acornd never creates it | — |
| `session.sqlite` in `layout.State` | a workspace's conversations | the worker | the worker's storage |
| `drafts.json` in `layout.State` | unsent composer text | `control` | a snapshot under `Control.mu`, then temp file and rename; one writer |

`control.Registry` and `control.ChangeDefaults` are the two read-modify-write paths for
`workspaces.json` and `settings.json`. Before them, a writer saved the copy it had read and erased
a change another process made meanwhile. Do not write those two files any other way.

Lock order inside `control` (comments in `workspaces.go` and `hub.go`): the registry file's lock,
then `Workspaces.mu`, then a `Control`'s `mu`, then the update hub's `mu`. Never the reverse.
`lockorder_test.go` holds one of these edges.

## The wire

| Hop | How | Where |
|---|---|---|
| UI to gateway | `fetch`; every command is a POST | `ui/src/app/client.ts`, routes in `gateway/internal/httpapi/routes.go` |
| Gateway to UI | one SSE stream per client | `gateway/internal/httpapi/stream.go`, read by `ui/src/app/use-stream.ts` |
| Control to worker | JSONL, one object per line, on stdin and stdout; stderr is diagnostics | `gateway/internal/workerpool/worker.go` |
| Worker to sandbox | the engine's SDK | `SandboxEngine` in `worker/src/sandbox.ts` |

The worker's wire is written once per language: `gateway/internal/protocol/protocol.go` and
`worker/src/protocol.ts`. The UI's mirror of the HTTP shapes is `ui/src/app/wire.ts`. Change both
sides in one commit (S6).

**Fixtures hold them together.** `fixtures/wire/` has `commands.json`, `frames.json` and
`state.json`. A Go test (`gateway/internal/control/wire_fixtures_test.go`) builds each from the real
structs and fails if the file differs. `UPDATE_FIXTURES=1 go test ./internal/control` rewrites
them. `worker/test/wire-fixtures.test.ts` and `ui/test/wire-fixtures.test.ts` read the same files
against their own types. A field renamed on one side fails on the other.

**A state frame is a signal, not a transcript.** `control`'s update hub (`hub.go`, `updateHub`)
keeps, per attached client, a queue of events plus two coalescing signals: one for "state moved",
one for "this conversation's view moved". The hub caches the newest view per conversation. A slow
client wakes, reads the newest state and view, and skips what it missed. A queue would drop the
newest view, the one that says the run ended, and the client would never hear it. Storage is the
truth and a reconnect is safe (S7).

## The worker

`createWorker(options, deps)` in `worker/src/worker.ts` builds everything the process holds. It
makes a `WorkerContext` and a `CommandTable`: one handler per command type, so the compiler asks
for a handler when the protocol gains a command. Handlers live in the module of their concept
(`conversations.ts`, `runs.ts`, `attachments.ts`, `commands.ts`, `auth.ts`, ...) and each exports
a function returning a `Pick<CommandTable, ...>`. A few commands answer something that is already
waiting (`auth_answer`, `secret_answer`, `sandbox_wake`, ...). They skip the queue and are listed
in `Bypassing`.

`WorkerDeps` is the process's edges: `openStorage`, `openSandbox`, `now`, `exit` and `write`.
**`deps.write` is the one way a frame leaves the process.** `worker/src/main.ts` only reads
arguments, opens real storage and a real sandbox, and wires `deps.write` to stdout. Tests pass
fakes.

One module per engine implements `SandboxEngine`: `microsandbox.ts`, `bwrap.ts`, `youki.ts`,
`host.ts`. `main.ts` picks one by the `--sandbox-engine` argument control passes.

## The UI

`usePlatform` (`ui/src/app/use-platform.ts`) is a composition of twelve hooks in
`ui/src/app/use-*.ts`: `use-choices`, `use-conversation`, `use-draft`, `use-navigation`,
`use-people`, `use-preferences`, `use-providers`, `use-remote`, `use-session`, `use-stream`,
`use-update`, `use-workspaces`. Each owns its state and returns a slice; the `Platform` interface
is the slices together. Screens and stories see one object. Frames land in the hooks that own
them, and `use-stream` is built after those hooks so it can feed them. `map.ts` turns wire shapes
into view models before a component sees them.

## Sandbox engines

An engine is a `sandbox.Driver` (`gateway/internal/sandbox/manager.go`). It registers itself with
`sandbox.RegisterDriver(name, factory)`, a table guarded by a lock because parallel tests register
while others look up. Each driver package exports `Register()`; `cmd/acornd/main.go` calls it
before anything can choose an engine. Nothing registers in `init` (G3). `control` names an engine
by its string and calls `sandbox.Engine(dataDir, state, name)`.

pasta, which gives a sandbox its network, is told the interface name with `-I eth0`. The constant
is `sandbox.SandboxInterface`. The sandbox's own network setup brings up `netns.Interface`. The
two are equal, and a test in the bwrap package holds them so. `sandbox` may not import `netns`,
which is why there are two.

## Tests

| Tier | Command | Runs |
|---|---|---|
| Fast | `make check` | compiler, UI bundle, unit tests of worker, UI and gateway. No processes. |
| Default | `make test` | `make lint` (static rules, `internal/arch`, analyzers), `make floors`, `make js-test`, then every Go suite with `-race`, feature suite included. No hypervisor. |
| Coverage | `make cover` | the same suites instrumented, merged, judged against `.coveragefloors` |

`make floors` runs `scripts/check-floors.mjs` against the base branch. The coverage and mutation
floors (`.coveragefloors`, `.mutationfloors`) only move up (S10). The script accepts three moves
that look like a fall and are not: a package split, code moved up into a shared package, and code
moved into a package that is new to the tree. Anything else that falls fails.

**Only CI runs** (`.github/workflows/`): `make test-sandbox` (microVM, needs `/dev/kvm`),
`make test-bwrap`, `make test-youki`, `make ui-check` (a browser against a real gateway),
`make mutations` per package, `make security`, and the commit-message check. Go's `-race` needs
cgo, so a machine without it cannot run the default tier's race pass.

## To add

**A worker command**

1. `gateway/internal/protocol/protocol.go` (a field on `Command` if it carries data) and
   `worker/src/protocol.ts` (a member of the `Command` union).
2. A handler in the worker module of its concept, returned in a `Pick<CommandTable, ...>` and
   spread into `table` in `worker/src/worker.ts`. If it answers something already waiting, add it
   to `Bypassing` and `bypass` instead.
3. A method on `Control` that calls `c.worker.Call` (see `Rename` in
   `gateway/internal/control/conversations.go`), a handler and a route in `gateway/internal/httpapi/`,
   a function in `ui/src/app/client.ts`.
4. The fixtures: a row in `wire_fixtures_test.go`, `UPDATE_FIXTURES=1 go test ./internal/control`,
   and a row in `commandShapes` in `worker/test/wire-fixtures.test.ts`.
5. A row in `docs/features.md`.

**A sandbox engine**

1. A package `gateway/internal/sandbox/<name>/` with a `Driver` and a `register.go` exporting
   `Register()` (copy `gateway/internal/sandbox/youki/register.go`).
2. `gateway/cmd/acornd/main.go`: call `<name>.Register()`.
3. `gateway/internal/arch/arch.go`: a row for the new package, and its import in the `cmd/acornd`
   row, each with a reason.
4. `sandbox.EngineNames` in `gateway/internal/sandbox/host.go` (and `defaultEngines` in `tier.go`
   if a machine may pick it on its own).
5. `worker/src/<name>.ts` implementing `SandboxEngine`, and a `case` in `worker/src/main.ts`.
6. Floor rows in `.coveragefloors` and `.mutationfloors`, and a CI tier like `make test-youki`.
7. An ADR (S4).

**An install setting**

1. A field on `Defaults` in `gateway/internal/control/models.go`, and on `ScopeDefaults` if a
   workspace may override it.
2. Read and write it only through `ChangeDefaults`. Resolution of the layers is in the same file.
3. A setter on `Workspaces` (see `SetInstallThinking`) and a route in `gateway/internal/httpapi/routes.go` with its handler
   in `gateway/internal/httpapi/defaults.go`, behind `s.admin`.
4. If the console shows it: the state shape in `control.go`, `ui/src/app/wire.ts`, a function in
   `ui/src/app/client.ts`, and the hook in `ui/src/app/use-choices.ts`. Re-run the state fixture.
5. A row in `docs/features.md`.

## Known gaps

What is still not as the layer table wants. Sizes measured on `main` at d8cbd14, non-test Go.

- **`control.Control` is large.** 161 methods over ten files, and five mutexes: `mu`, `engineMu`,
  `secretMu`, `lifecycle`, `continuationMu`. The package is 7,041 lines.
- **`Workspaces` mixes four jobs:** registry persistence, the engine catalog, idle eviction and
  routing. `workspaces.go` is 1,640 lines with 59 methods on `Workspaces`, three mutex fields
  and a map of per-name start mutexes.
- **`cmd/acornd` is large.** 6,135 lines; the file `gateway/cmd/acornd/main.go` is 741. Setup, doctor and the workspace
  commands live beside the wiring.
- **Engine names leak.** In `control`, `httpapi`, `support` and `cmd/acornd`, ten non-test files name an engine:
  `gateway/internal/control/control.go`, `gateway/internal/control/sandbox.go`,
  `gateway/internal/control/workspaces.go`, `gateway/internal/httpapi/workspaces.go`,
  `gateway/internal/support/support.go`, and five in `gateway/cmd/acornd` (main, doctor, setup,
  setupaccount, workspace). Ten more files sit in `gateway/internal/sandbox` itself. The worker has
  the same shape: `main.ts` switches on the engine name, and `bwrap.ts` (560 lines),
  `microsandbox.ts` (389), `host.ts` (99) and `youki.ts` (36) each hold engine code. A new engine
  touches more than its own package.
- **Two files skip `layout`'s write.** `drafts.json` and `update.json` are written by their
  owners without `layout.WriteFile`; `update.json` has no lock.
