# ADR 0038 — A nested acornd can tell, or it cannot nest

Status: accepted, not built
Date: 2026-11-05
Amended: 2026-11-06 — decision 3 re-specified: the worker states nesting from the config it
already reads, because `inheritedEnv` cannot carry a marker the worker never sees. Decision 4
was already built (`withEnv` exists), and the depth measurement gains the capability that
makes it true.

## Context

[ADR 0030](0030-nested-engines.md) settled how a bwrap workspace runs another acornd: the
inner sandbox reuses the user namespace pasta already made, instead of asking bubblewrap for
a third one that cannot write its uid map. It measured the wall —

| Attempt | Result |
|---|---|
| Outer bwrap → inner pasta → inner bwrap, `--unshare-all --uid 0` | `bwrap: setting up uid map: Operation not permitted` |
| Inner bwrap reuses pasta's namespace (no `--unshare-user`, `--share-net`) | works |

— and gated the second row on a marker: the outer bwrap is told to `--setenv ACORND_NESTED 1`
(`bwrap.go:1157`), and the inner acornd reads it when it builds a sandbox
(`bwrap.go:639`).

The marker does not survive the trip. Measured on 2026-11-05, inside a bwrap workspace on
Debian 13, bubblewrap 0.12.0:

| Where | What it says |
|---|---|
| The argv this workspace was created with | `--proc /proc --cap-add ALL --setenv ACORND_NESTED 1 --uid 0 --gid 0 --unshare-all --share-net` |
| `env` inside a tool call in that same workspace | **no `ACORND_NESTED`** |
| An acornd started from that shell | `nested: false`, takes `--unshare-all`, dies with `setting up uid map: Operation not permitted` |

The drop is in the worker, and it is deliberate everywhere except here:

```
worker/src/bwrap.ts:80   const inheritedEnv = ["TERM", "LANG", "LC_ALL", "TZ"] as const;
```

A tool inherits four variables. `ACORND_NESTED` is not among them, so every acornd that a
tool starts — which is every acornd an agent builds, runs, or tests inside a workspace —
believes it is at the top of the machine. It then does the one thing ADR 0030 proved does
not work.

The test harness is not the problem it looks like: `internal/e2e/harness_test.go:258` builds a
curated environment (`PATH`, `HOME`, `ACORND_DATA_DIR`, `ACORND_WORKER`), but `withEnv` at
`:195` appends to it, so a test can state that its gateway is nested. No test does, and none
needs to once the probe lands.

The consequence is that the bwrap tier cannot run where acornd is developed. `make test-bwrap`
inside a bwrap workspace fails on every test that starts a sandbox, with exactly the error
ADR 0030 was written to prevent.

**A second measurement, which constrains the fix but is not this ADR's subject.** The wall is
the *user namespace*, not depth — the kernel allows nesting further than acornd assumed:

| Attempt | Result |
|---|---|
| `bwrap --ro-bind / / --unshare-all -- bwrap --ro-bind / / --unshare-all -- true` | `setting up uid map: Read-only file system` |
| The same with `--proc /proc` at both levels | **depth 3 succeeds** |

The read-only `/proc` from `--ro-bind / /` is what failed, not the third namespace. acornd
already mounts `--proc /proc` unconditionally (`bwrap.go:1192`), so it is correct today; the
row is recorded because the constraint is now load-bearing and nothing says so.

Those rows hold while the process making the next namespace still has `CAP_SETUID` in the one
it is in. Measured in the same workspace on 2026-11-06: a further `bwrap --unshare-all` from
a shell there succeeds (`user:[4026535837]` → `user:[4026535843]`), because the workspace is
created with `--cap-add ALL`; the same call under an outer `--cap-drop ALL` prints the ADR's
error. The namespace count is not what fails `make test-bwrap`; the capabilities at that
point in the chain are.

## Decision

1. **Nesting is observed, not declared.** An acornd reads `/proc/self/uid_map` at startup.
   The initial user namespace maps the whole range (`0 0 4294967295`); every namespace made
   by somebody else maps less. A map narrower than the full range means this process is
   already inside one, and a sandbox it makes must reuse it rather than ask for another.
   `/proc/self/setgroups` reading `deny` corroborates it and is checked when present.
2. **The marker stays, as an override.** `ACORND_NESTED=1` still forces nested and
   `ACORND_NESTED=0` forces top-of-machine, for tests and for a machine that knows better
   than the probe. The environment can overrule the observation in both directions; it can
   no longer *fail* to reach a process and silently change the answer.
3. **The worker states it from the config.** Control already writes `nested` into the
   workspace's config (`bwrap.go:334`), and the worker reads that file; it does not declare
   the field (`bwrap.ts:48-69`) and never sets it in the environment it builds. It sets
   `ACORND_NESTED=1` when the config says nested, before the caller's own variables, so a
   caller that wants the other answer still gets what it asked for. This is a fact the worker
   holds, not a value inherited through layers: the worker is a process beside the sandbox,
   not inside it, so `inheritedEnv` copies the gateway's environment and never the marker the
   sandbox's first process was given.
4. **The harness already has the switch.** `withEnv` (`harness_test.go:195`) appends to the
   curated environment. The probe makes it unnecessary — a gateway started inside a test that
   itself runs inside a sandbox now detects it — but a test that wants the other answer can
   still state it.
5. **A sandbox that may nest mounts a writable proc.** `--proc /proc`, never a read-only
   bind of the host's. Recorded as a constraint on the argv, because the failure it prevents
   is `EROFS` at the first nested start and reads like a kernel refusal.

## What it costs

- **A rootless container now reads as nested.** acornd in rootless podman or a userns-remap
  container will reuse that namespace and share that network, where before it believed it
  was at the top and failed. That is the right behaviour inside a container, and it is a
  change: `doctor` will report a nested workspace on a machine whose owner did not make one.
- The probe is one file read at startup. If it cannot be read, acornd assumes the top of the
  machine and says so; guessing "nested" there would take the reuse path on a machine that
  has no namespace to reuse.
- The marker keeps a second meaning — a test's assertion — so it cannot be removed when the
  probe lands.

## Rejected

- **Curation alone** — add `ACORND_NESTED` to `inheritedEnv` and stop. It fixes today's
  symptom and keeps the defect: the answer still depends on every intervening layer
  remembering to pass a flag. The curated list is the reason this broke, and a curated list
  is what would break it again.
- **A user namespace per level** — what ADR 0030 measured and rejected, and what a nested
  acornd does today when it thinks it is at the top. Subuid ranges could make it viable
  later; no host in this setup has `/etc/subuid` or `newuidmap`, and it is a larger decision
  than this one.
- **microVM for the nested case** — the engine can carry it (a microVM boots inside a bwrap
  workspace), but the direction is bwrap-first, and reaching for a hypervisor to fix a marker
  is the wrong shape of fix.

## Tests

- `TestBwrapObservesNestingFromItsUserNamespace` — a narrow `uid_map` reads as nested.
- `TestBwrapObservesTheTopOfTheMachine` — the full range reads as top, and a published
  `setgroups` reading `deny` corroborates a namespace.
- `TestTheMarkerOverrulesTheObservation` — both directions.
- `TestANestedSandboxTellsItsCommands` — the worker sets the marker from the config, a
  caller can overrule it, and `clearEnv` does not remove it.
- `TestABwrapThatMayNestMountsAWritableProc` — the argv carries `--proc /proc`.

The evidence behind the tables is three commands run inside a bwrap workspace on 2026-11-05;
the reproduction for the failure is `make test-bwrap` there.
