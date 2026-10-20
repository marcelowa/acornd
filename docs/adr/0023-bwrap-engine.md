# ADR 0023 — bwrap, spiked against the same list

Status: spike done; bwrap is built as a beta engine, youki is not
Date: 2026-10-26

## Context

[ADR 0022](0022-youki-engine.md) spiked youki against the owner's four requirements.
[ADR 0015](0015-host-engine.md) had already named bwrap as "the better fast engine on Linux …
it reuses the host engine's file operations and changes only `exec`", and deferred the
hardening. This is that spike: the same list, the same machine, the same day, so the numbers
are comparable.

## What bwrap is, and what it is not

It is not a runtime. No `create` / `start` / `exec`, no state directory, no config file, no
binary to vendor: `/usr/bin/bwrap`, **78 KB**, 0.11.0 in Debian 13. It builds a mount and
namespace view from its argv and execs the command. Every command is a sandbox. Nothing about
one survives except what it wrote.

That one fact decides most of what follows.

## Cost

| | bwrap | youki (0022) | microsandbox (0012) |
|---|---|---|---|
| up and down, per command | **6 ms** median (5–8) | create 38 ms + start 6 ms | 259 ms boot |
| what you ship | 78 KB, distro package | 7.8 MB, vendored | 67 MB, fetched |
| what it needs | user namespaces | user namespaces | KVM |

## The four requirements

**1. A sandbox: yes.** `--ro-bind / /` with `--tmpfs /home --tmpfs /root` gives an empty home
and an empty `/root`; `/usr` and `/etc` are read-only (`Read-only file system` on write); the
workspace is bound read-write at its own path; a file written inside lands **owned by the host
user** (`marcelo:marcelo`). The host's `/etc/passwd` stays readable — see the cost section.

**2. Install and run tools: yes, in two shapes.**

- *Host root read-only plus a private `$HOME`.* Language toolchains install into the volume:
  bun 1.4.4, 103 MB, and the host's `~/.bun` was not touched. apt cannot write the host root
  (`Read-only file system`), which is the point.
- *A bound rootfs directory plus `--uid 0`.* The same unpacked `debian:stable-slim` from 0022
  becomes `/`, and apt works inside it: `apt-get install less` completed, the file landed
  owned by `marcelo` outside. dpkg insists on root, which `--uid 0` provides while the
  outside stays the user's own uid. The same `APT::Sandbox::User "root"` line is needed as
  with youki, for the same reason.

**3. Secrets: yes, as environment delivery — but it leaks by default.** bwrap **inherits the
caller's environment**. The first run of the spike printed the host's own token inside the
sandbox. `--clearenv` plus explicit `--setenv` is mandatory, not tidiness.

**4. Nesting: yes, and this is where it wins.** A bwrap inside a bwrap runs, with distinct user
and pid namespaces (`4026535932` → `4026535853`), a fresh procfs, and five pids in each.
youki at the same depth dies mounting `/proc`. So a bwrap workspace can run acornfall and that
acornfall can use bwrap again.

## The five extras

| | measured |
|---|---|
| **budgets** | no cgroups of its own. `systemd-run --user -p MemoryMax=256M -p MemorySwapMax=0 --scope bwrap …` terminates a 400 MB allocation (exit 143) while a 100 MB one completes. Without `MemorySwapMax=0` the limit is silently not hit — swap absorbs it, which cost one wrong conclusion here. |
| **kill** | `--die-with-parent` cascades: two 300 s sleeps died with the sandbox. Without it they survive the sandbox's death. |
| **persistence** | no shared namespace between commands. A dev server started by one command answers `HTTP 200` from outside, but a later command cannot signal it — different pid namespace. youki's `exec` joins the running one. |
| **egress** | `--unshare-net` + `pasta` + `ip`, the same three pieces as youki, with one twist: the sandbox must run `--uid 0` so its user namespace maps `0 → you`. With an identity mapping, a joiner is a plain user inside and `setns(net)` is `EPERM`. That done, pasta attaches (template `eth0`, `192.168.50.251`) and TCP egress works. |
| **ownership** | writes by the sandbox are the user's own files — the property youki gets from its `0↔1000` mapping. |

## What it costs

- **The host root is visible read-only** under `--ro-bind / /`. Binding a rootfs directory
  instead gives youki's "our own `/`", at the price of unpacking one per workspace.
- **No cgroups and no `update`.** Budgets belong to systemd; a live change is a new command.
- **No sandbox object.** `SandboxClient` becomes spawn-per-exec and the file operations are
  plain host operations in the workspace — which is what ADR 0015 predicted, and it removes
  the joiner, the create/start/stop state and the engine's own store entirely.
- **Ubuntu 24.04+ gates unprivileged user namespaces** with AppArmor, the well-known bwrap
  failure on that release. It must be detected and refused with a sentence, like every other
  missing capability.
- **Linux only.** macOS still needs the VM.

## The comparison, one table

| | bwrap | youki |
|---|---|---|
| sandbox | yes | yes |
| os and language tools | yes, with a rootfs we bind | yes, with a rootfs it roots at |
| secrets | yes, must clear the environment | yes |
| **nesting** | **yes** | **no** |
| budgets | yes, delegated to systemd | yes, with a delegation setup of its own |
| kill | yes, `--die-with-parent` | leaves the child; needs a group kill |
| exec | spawn a new sandbox | `exec` joins the running one, and joins are broken rootless |
| cost | 6 ms, 78 KB | 44 ms, 7.8 MB |

bwrap covers all four requirements and all five extras, with fewer moving parts, and it is the
only one of the two that nests. youki's advantages are a declarative `config.json` and built-in
cgroup and exec plumbing — plumbing that did not work rootless in this environment.

## What is built

bwrap is an engine a workspace can choose, by name, at creation: `POST /api/workspaces` with
`engine`, `acornfall workspace add --sandbox bwrap`, or the engine switch on the create form.
The choice is stored on the workspace record like its image and its network, and it decides
what `--sandbox-engine` the worker is started with — so one install can hold a microVM
workspace and a bwrap one.

The shape is the one this ADR predicted, and it is small:

| | |
|---|---|
| `gateway/internal/sandbox/bwrap.go` | the Driver. No install (a distribution package), a real `Usable()` probe of a user namespace, `Create` writing one readable config per workspace, and no sandbox object: `List` is empty, `Start` and `Stop` do nothing, `Remove` deletes the config and the private home. |
| `gateway/internal/sandbox/tier.go` | one Manager per engine, built on first use, so a workspace's engine is a lookup rather than an install-wide switch. |
| `worker/src/bwrap.ts` | the client: the config becomes an argv, and the file operations are the host's, unchanged. |
| `worker/src/workspace-files.ts` | the file operations the host and bwrap engines share, extracted so they cannot drift. |

The config a command runs under, written to `<home>/sandboxes/bwrap/<name>.json`, is:

- the host root **read-only**, `/tmp` a tmpfs, `/dev` and `/proc` fresh;
- `<home>` — credentials, state, the engine's own store — hidden behind a tmpfs, with the
  global resource folders bound back **read-only at their own paths**;
- a **private home** per workspace under the engine's folder, which is where a language
  toolchain installs and what `Remove` deletes;
- the workspace bound **read-write at its own path**;
- `--unshare-all`, with `--share-net` only when the workspace's network is on;
- `--die-with-parent`, so a kill reaches the command;
- `--clearenv` and then exactly the environment the worker hands over, which is also how a
  secret arrives (the environment delivery row).

Two things are deliberately missing, and this is a beta because of them:

- **No image and no rootfs.** Commands run on the host's tools, read-only. `apt` inside a
  workspace is the next step, and it is the unpack/overlay work neither engine provides.
- **No persistent sandbox.** Every command is a sandbox, so no daemon outlives it and there is
  no `exec` into a running one. That is the supervisor this ADR's second half describes.

## What this does not settle

- **Whether the cheap engine gets a rootfs at all.** A private `$HOME` covers language tools;
  `apt` needs a rootfs, and a rootfs needs the unpack/update/prune lifecycle that neither
  engine provides.
- **Whether the cheap engine is also the default engine.** Nesting and cost argue for bwrap;
  a rootfs and the OCI config argue for youki; the VM keeps the strongest isolation.
