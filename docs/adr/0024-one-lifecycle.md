# ADR 0024 — One lifecycle for every engine, and the supervisor that makes one possible

Status: accepted, built for bwrap; the policy is engine-neutral from here
Date: 2026-10-26

## Context

Building the bwrap engine turned up an asymmetry nobody chose. ADR 0012 gave the Driver four
verbs — create, start, stop, remove — and microsandbox answered all four with a microVM.
Bubblewrap answered none of them: its `List` was empty, `Start` and `Stop` did nothing, and a
"workspace's sandbox" was a view rebuilt for every command.

That cost three things at once:

- **50 ms per command**, 44 of it pasta setting up a network namespace that was thrown away.
- **No daemons.** Every command got a fresh pid namespace, so a server a command started died
  with it. No cron job, no dev server, no watcher.
- **A lifecycle that was a no-op pretending to be one.** `Stop` did nothing, `Status` said
  nothing, and idle eviction stopped a worker while the sandbox it described had no state to
  stop.

## Decision

**Two things live in the word "sandbox", and only one of them needs a lifecycle.**

*The environment* — image, rootfs, `$HOME`, installed dependencies — every engine has, and it
survives restarts. *The instance* — a running thing with namespaces — is the VM in one engine
and a process in another. So:

1. **One contract, every engine:** `absent → created → running → stopped → removed`. The same
   four verbs, the same control-side policy — ensure created, ensure running, stop on idle
   eviction, remove with the workspace, adopt what is still alive after a gateway restart.
2. **The instance is declared, not assumed.** An engine that has none says so and every verb is
   a no-op on purpose; the state frame says "no persistent sandbox" instead of a status that
   means nothing. `host` is that engine.
3. **`Stop` kills processes and never deletes files.** A stopped workspace keeps its image copy,
   its home, and its dependencies; only `Remove` takes them.
4. **Control owns the start, the stop, and the idle rule.** The engine never decides to shut
   something down that control believes is running. A workspace that runs something is exempt:
   `keep_alive`, on by default off, exactly like the general workspace's own exemption.
5. **The instance is engine-neutral**, so it is one program, not one per engine.

## The supervisor

`sandbox-supervisor` is a subcommand of this binary: control binds the binary into the sandbox
and runs it as the sandbox's first process, after pasta when the workspace has egress.

```
control ──spawns──► pasta ──► bubblewrap ──► acornfall sandbox-supervisor
                                                    │  JSON lines over a Unix socket
worker ────────────────connects to the socket───────┘  run / kill / shutdown
```

Why it is a subcommand of this binary rather than a second program: it is already on disk, and
**releases build it `CGO_ENABLED=0`**, so it is static and runs in an image that has no libc at
all — a musl image, or a distroless one. `make build` builds it the same way now, for the same
reason.

What it does, and only that: run an argv, in namespaces it already holds, with exactly the
environment it was sent, streaming its output, killing a whole process group, and reaping what a
command leaves behind. It names no engine. The same program is the answer to youki's broken
rootless `exec` ([ADR 0022](0022-youki-engine.md)), which is why it is a subcommand and not a
file in the bwrap driver.

Three consequences worth writing down:

- **The socket is a path on both sides of a bind**, so it has the 108-character limit ADR 0012
  already lost a day to. It lives in `<state>/run/<hash>/s.sock`, named by a short hash rather
  than by the sandbox's readable name, and the driver refuses with a sentence when even that does
  not fit.
- **A worker restart is not a sandbox stop.** A client that disconnects does not kill its
  commands: the sandbox outlives the process watching it, which is what makes a gateway restart
  something to adopt rather than to clean up after.
- **The sandbox survives the gateway.** It is started in its own session, so a restart finds it
  by pid and socket and adopts it.

## Measured

| | stateless (a sandbox per command) | one sandbox per workspace |
|---|---|---|
| one command | 50 ms | **1.1 ms** |
| twenty shell commands | ~1 s | **22 ms** |
| daemons | impossible | ordinary children of the supervisor |
| pasta's footprint | per command | **1.9 MB**, once |

## Consequences

- The state frame's `sandbox.status` now says `running` or `stopped` for bwrap, and `stopped` is
  a state that starts again rather than one that has to be remade.
- A workspace's sandbox holds one process more than before — which is the point.
- `keep_alive` exists on every workspace, and idle eviction respects it. That is what makes
  "a workspace that runs a cron job" a thing that can be true.
- **microsandbox's own `idle_timeout` is now the second owner of a rule control owns.** It has not
  been removed: a VM stopping itself is safe (the next start adopts and boots it), and removing it
  is a policy change of its own. It should go when the policy is next touched, and this is the
  note that says why.
- The image is still a copy per workspace ([ADR 0023](0023-bwrap-engine.md)); an overlay is the
  next thing that shrinks a workspace, and it is now the largest remaining gap.
