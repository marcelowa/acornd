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
| **egress** | carried by `pasta`, not by sharing the machine's stack — see below. |
| **ownership** | writes by the sandbox are the user's own files — the property youki gets from its `0↔1000` mapping. |

## Egress is pasta, and the sandbox has no address on your network

A workspace with its network on is a namespace of its own whose traffic pasta forwards through the
host. Measured inside a workspace with an image:

```
interfaces:                    lo eth0          (nothing of the machine's is in there)
getent hosts debian.org:       resolved
TCP to 1.1.1.1:53:             connected
MagicDNS over the tailnet:     100.64.0.15  peer.example.ts.net
```

Two consequences worth stating:

- **The tailnet comes along for free, and that is what `network: on` means.** pasta dials from the
  host side, so the host's routing — `tailscale0`, MagicDNS at `100.100.100.100`, any peer — applies
  to whatever the sandbox dials. Nothing is configured and no key is involved, which also means the
  peers see this machine: a workspace with its network on can reach what this machine can reach, as
  this machine. It is a workspace's own field on its record for that reason, not a detail of the
  engine, and `network: off` leaves nothing to inherit.
- **The machine's `127.0.0.1` is not the sandbox's — since 0.12.2.** It was until then, and that is
  the one claim this ADR made that the code did not hold. pasta's port forwarding defaults to
  `auto`, which mirrors every service the machine listens on into the sandbox's loopback, and its
  gateway translation maps 10.0.2.2 onto that same loopback: a workspace could open
  `127.0.0.1:7777` and talk to the acornfall that ran it, which serves its API with no
  authentication. It could also read every other listener on the machine — sshd, a database, a dev
  server. `-t none -u none -T none -U none --no-map-gw` closes both routes, and
  `TestABwrapSandboxCannotReachTheMachinesLoopback` is what keeps them closed.

What egress still does not do, and nothing in pasta can: it has no per-destination filter. A
workspace whose network is on reaches private ranges, link-local metadata addresses and the tailnet
exactly as far as the machine does. Filtering that is a host firewall with a grant per sandbox,
which needs root at install time and is not built yet; until it is, the honest control is the
workspace's own `network` field, and the dialog says what turning it on grants.

What it costs: pasta's setup, once per workspace rather than once per command — 1.1 ms per command
after that (ADR 0024). ICMP does not work (pasta is a userspace forwarder, so `ping` fails and TCP/UDP do not), and two packages
have to be installed — `passt` for pasta and `iproute2` for the address step — because `Create`
refuses a workspace whose egress it cannot carry rather than quietly putting it on the host's
network.

Running `tailscaled` **inside** a workspace is possible but is a different feature: it needs
`/dev/net/tun` bound in and `--cap-add CAP_NET_ADMIN`, because **bwrap drops every capability by
default** — a sandbox is root with no powers (`CapEff: 0`). Measured: the interface comes up. The
no-privilege alternative is `tailscaled --tun=userspace-networking`, which needs no device and no
capability because applications reach it through a proxy.

## What a host has to grant

Two things a sandbox cannot do for itself, both found by the CI tier rather than by reasoning:

- **A user namespace, and the one it nests inside.** A sandbox with egress runs inside pasta's
  user namespace and makes one of its own. Ubuntu 24.04 gates unprivileged user namespaces
  behind AppArmor: a profile for `/usr/bin/bwrap` is enough for bubblewrap on its own and *not*
  enough for the nested one — the failure is `Couldn't write to /proc/self/uid_map` at create
  time, and the host-wide switch (`kernel.apparmor_restrict_unprivileged_userns=0`) is what makes
  it work. A per-binary profile is worth shipping anyway: it covers the workspace with no egress.
- **A resolver the sandbox can reach.** Binding the machine's `/etc/resolv.conf` is wrong on any
  machine that resolves through a local resolver: systemd-resolved answers on `127.0.0.53`, and
  inside a sandbox `127.0.0.1` is the sandbox's own loopback. The driver binds the machine's
  upstream servers instead, from the file the distribution writes them to, and the machine's own
  file when it names something reachable. The symptom this prevents is an `apt-get update` that
  works on the host and fails inside.

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

- the workspace's own image as `/` — or the host root **read-only** when a workspace has none —
  with `/tmp` a tmpfs and `/dev` and `/proc` fresh;
- `<home>` — credentials, state, the engine's own store — hidden behind a tmpfs, with the
  global resource folders bound back **read-only at their own paths**;
- a **private home** per workspace under the engine's folder, which is where a language
  toolchain installs and what `Remove` deletes;
- the workspace bound **read-write at its own path**;
- `--unshare-all`, with `--share-net` only when the workspace's network is on;
- `--die-with-parent`, so a kill reaches the command;
- `--clearenv` and then exactly the environment the worker hands over, which is also how a
  secret arrives (the environment delivery row).

**The image is the workspace's own disk.** The reference is fetched from the registry once,
verified layer by layer against the digests the manifest names, and unpacked into
`<home>/sandboxes/bwrap/images/<key>/base`. A workspace copies that tree into
`<name>/rootfs` and binds **it** as `/`, as root inside (a single mapping `0 ↔ the user`), with
the machine's resolver bound read-only. Installations then behave the way people expect:

| | |
|---|---|
| a language toolchain in `$HOME` | survives, in the workspace's private home |
| `apt install` | **works**: it is an ordinary write to the workspace's own root |
| `/tmp` | gone with the command, as it should be |
| the machine's files | not in the tree at all; the only root is the workspace's copy |

Measured inside a workspace: `apt-get update` 2 s, `apt-get install -y less` completes, and
`less` is there for the next command. The host's `/usr` never changes.

Two things are still deliberately missing, and this is a beta because of them:

- **A copy, not an overlay.** Each workspace pays its image's size on disk (82 MB for
  `debian:stable-slim`) and a copy at creation. An overlay with a shared read-only lower layer
  is the optimisation, and it needs a mount this engine cannot make for itself.
- ~~No persistent sandbox.~~ **Built since:** the workspace's sandbox is one process, with this
  binary as its first process inside and a socket the worker connects to. A command costs
  **1.1 ms** instead of 50, and a daemon outlives the command that started it. See
  [ADR 0024](0024-one-lifecycle.md).

A bwrap workspace therefore needs a registry once, as the microVM engine needs one for its
first image (ADR 0012). A failed fetch is a refusal — a workspace is created with its image or
not at all — and the e2e tests that create one run under `ACORNFALL_IMAGE_TEST=1`, because
`make test` stays offline.

## What this does not settle

- **Whether the cheap engine gets a rootfs at all.** A private `$HOME` covers language tools;
  `apt` needs a rootfs, and a rootfs needs the unpack/update/prune lifecycle that neither
  engine provides.
- **Whether the cheap engine is also the default engine.** Nesting and cost argue for bwrap;
  a rootfs and the OCI config argue for youki; the VM keeps the strongest isolation.
