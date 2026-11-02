# ADR 0037 — A wake goes through control

Status: accepted, built
Date: 2026-11-02

## Context

A substitute secret keeps its value out of the policy file and out of the guest. The
policy holds a source reference such as `{ $msb_env: ACORND_SECRET_0 }`, and the engine
resolves that host variable **at start**. Only the gateway's create or start process has
the value; the worker attaches to the guest and is deliberately never told it
([ADR 0021](0021-a-vault-for-tool-secrets.md)).

The microVM engine stops an idle sandbox after `idle_timeout`. The worker's next tool call
used `connectOrStart`, which made the worker's SDK process the start process — one that
holds neither the vault nor the value. The engine therefore failed config load before any
command ran:

```
invalid config: secret GITHUB_PAT: host environment variable GITHUB_PAT is not set
```

Fail-closed was right, but the only recovery was a person re-entering the value. An idle
timeout turns that into a recurring event (issue #13). The released build names the
logical secret because a secret added after creation is stored by `msb modify` with the
guest name as its source; it is not a different check, and it is not version drift.

## Decision

**A stopped sandbox is started by control, not by the worker.** The worker sends a
`sandbox_wake` frame with an id and waits. Control builds the workspace's current policy
from the vault, calls the same `Produce` path a workspace start uses, and answers the frame
with success or the reason the start failed. Only then does the worker call `connect()` on
the engine — never `connectOrStart` — so a start that control did not perform fails rather
than silently resolving the source from the wrong environment.

The value still exists only in the vault and the start process's environment. The frame
carries an id and, on failure, an error; the answer carries the same. Neither carries a
value, and the policy file still holds only the source reference.

## Alternatives rejected

- **Keep the values in the worker's environment.** It makes `connectOrStart` work, but it
  breaks the one rule that makes substitute delivery worth having: the worker never sees a
  value. A worker bug or a future host-side command would turn that into a leak.
- **Have the worker read the vault.** Same boundary, same reason. The vault is control's.
- **Keep the sandbox awake for the worker's lifetime.** It avoids this wake, but throws away
  the engine timer that is the net under a gateway that died, and leaves a VM running for a
  workspace nobody is using.
- **Let the worker start without the secret and degrade.** A tool that runs without its
  credential is the silent failure this feature exists to prevent; a whole workspace that
  refuses because one secret is unavailable is the outage being fixed.

## Consequences

- An idle stop is invisible to a tool call again: the call waits for control's start, then
  attaches. The wait is bounded, and a start that does not land is the tool's error.
- Control remains the only owner of the sandbox lifecycle, which is what
  [ADR 0024](0024-one-lifecycle.md) already says; the microVM wake was the exception.
- A worker whose control is gone cannot start a sandbox. That is deliberate: it fails with
  the reason instead of starting one with a missing credential.
- The first attach after a worker starts still uses `connectOrStart`, because control has
  just provisioned the sandbox before spawning the worker. The wake path is for the stops
  that happen under a live worker.

## Where it is checked

- `TestAWakeStartsTheSandboxWithTheVaultsValues` — control starts a stopped sandbox with
  the vault's value and answers the worker without it.
- `TestAWakeThatCannotStartSaysWhyAndRunsNothing` — a failed start is the worker's reason.
- `worker/test/microsandbox.test.ts` — a wake asks control before attaching, and a refused
  wake never starts the sandbox in the worker.
