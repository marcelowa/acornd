# ADR 0022 — youki as a sandbox engine

Status: spike done, decision open
Date: 2026-10-26

## Why this exists

ADR 0012 made microsandbox the engine and left the seam open; ADR 0015 filled it once with
`host`. Two requirements now push for a third engine:

1. **Cheap on resources.** A microVM per workspace costs a boot, a 2 GB cap and a hypervisor.
2. **A workspace that can run acornfall.** That is the ADR 0018 problem restated: today it
   needs nested virtualization, a pinned engine that has no switch, and a 2 CPU / 2 GB guest
   inside a 2 CPU / 2 GB one.

This records a spike, not a decision. Everything below was measured on one machine
(Debian 13, kernel 7.0.14, `bwrap` 0.11 present, no passwordless root) **inside an acornfall
workspace** — a microsandbox guest, whose user is itself in a user namespace
(`uid_map 0 100000 65536`). So the spike ran in the nesting scenario it was testing, one
level deeper than the product would.

## The engine as found

youki 0.7.0, static musl, **7.8 MB** as a vendored tarball from `youki-dev` (it is in no
Debian 13 package, so vendoring is the only route). It is an OCI runtime: `create`, `start`,
`exec`, `kill`, `delete`, `state`, `update`, `spec --rootless`. It has **no** images, no
layer store, no networking, and no rootfs management — those are podman's upper half, and an
engine built on youki owns all of it.

Compared with the incumbent: microsandbox installs 67 MB and needs KVM; youki is 7.8 MB and
needs user namespaces.

## Requirement 1 — a sandbox: yes

`youki spec --rootless` gives user, mount, pid, network, ipc, uts and cgroup namespaces with
a single mapping `0 ↔ 1000`. That mapping is worth naming: **a file written by root inside
lands owned by the host user outside** — ADR 0012's property, kept for free. The workspace is
one bind mount at its own path; nothing else of the host is visible.

| measured | youki | microsandbox (ADR 0012) |
|---|---|---|
| create a sandbox | **38 ms** | 259 ms boot |
| start a stopped one | **6 ms** | 192 ms |
| idle, accounted | **384 KB**, 1 pid | 2 GB is a cap, not a reservation |
| first output chunk | **+18 ms** | +31 ms (SDK) / +3037 ms (CLI) |
| two 3 s commands | 3.02 s — parallel | 3036 ms — parallel |
| exit codes | preserved | preserved |
| background process across execs | survives (dev-server pattern) | survives |

## Requirement 2 — install and run tools: yes, with a rootfs we own

Unpacking `debian:stable-slim` is 29.8 MB compressed, 82 MB on disk. Inside:

| measured | |
|---|---|
| `apt-get update` | 2 s (10.2 MB) |
| `apt-get install curl unzip` | 8.1 s total |
| bun 1.4.4 via its installer | 3.1 s |

Everything lands in that rootfs — 551 MB after the three installs — and the host never sees
it. This is the property microsandbox gave; here it is ours to maintain.

Two things the engine must bake in, both found the hard way:

- **apt needs `APT::Sandbox::User "root"`.** apt drops to `_apt` with `setgroups`, which is
  denied under a single-mapping userns; without the line `apt-get update` dies in 50 ms with
  `Method http has died unexpectedly`.
- **The image is ours.** A registry pull, an unpack, an upper layer per workspace, and a prune
  are unbuilt. `crane` or `umoci` does the unpacking; nothing in youki does.

## Requirement 3 — secrets: yes, as environment delivery

The row ADR 0021 drew for containers: `DeliveryEnvironment`. Values live in the worker, reach
the command's environment, and never enter a config file. Verified: a placeholder variable
arrives, and no host variable leaks in (the joiner clears the environment; the worker is the
only source).

Honest downgrade, already written down in ADR 0021: with egress on, a command can print the
value and send it. Rotation means a restart, which the host engine already reports as
`RestartRequired`.

## Requirement 4 — acornfall inside acornfall: yes, if the inner engine is chosen

| measured | result |
|---|---|
| the acornfall binary inside a youki sandbox | runs |
| acornfall serving in a youki sandbox (`--sandbox-engine host`) | worker ready, workspace started, **HTTP 200** |
| **youki inside youki** | **refused by the kernel** |
| microsandbox inside youki | needs a deliberate `/dev/kvm` bind |

The refusal is worth the words, because it is the one thing this engine cannot do: the inner
container dies mounting `/proc`, `VFS: Mount too revealing`. It is specific to youki as far as
this spike goes — [ADR 0023](0023-bwrap-engine.md) mounts a fresh procfs and nests a bwrap
inside a bwrap at the same depth, and `unshare -U -r -p -f -m --mount-proc` does not fail on
the bare user namespace either. So the kernel rule is about *where* the mount happens, not a
blanket ban on containers in containers, and youki's ordering is the wrong side of it.

So a youki workspace can run acornfall, and the inner acornfall must pick `host` or `bwrap`.
It cannot run its own youki engine inside.

## What youki does not do, and the engine would have to

1. **`exec` is broken rootless.** From a host caller: `EPERM` joining the pid namespace. From
   inside the user namespace: an internal procfs crate error. The workaround is proven and
   small — open every namespace fd **first** (entering the mount namespace hides `/proc`),
   then `setns` user → pid → mnt → net → ipc → uts, then exec — about forty lines (`join.py`
   in the spike). The worker cannot `setns`, so the joiner has to be a binary it calls; the
   gateway binary is the natural home.
2. **Budgets need setup youki does not do.** A user namespace forces youki's systemd cgroup
   manager (`builder_impl.rs`: `use_systemd || user_ns_config.is_some()`), and there is no
   systemd inside a container, so a nested create fails outright. An **absolute**
   `cgroupsPath` selects the cgroupfs v2 manager instead. Real budgets then need the unit
   cgroup empty — `systemd-run --user -p Delegate=yes -p DelegateSubgroup=init` — and the
   controllers enabled by hand, `echo "+memory +pids" > cgroup.subtree_control`, because
   systemd 257 does not do it for a `Delegate=` list. After that a child cgroup takes
   `memory.max` (100 M) and `pids.max` (256) — verified.
3. **Networking.** `network: none` is a netns and costs nothing. Egress needs `pasta` (or
   `slirp4netns`) **plus** an address and route step inside the namespace: pasta serves DHCP
   and does not configure the interface itself — podman has netavark for that.
4. **Kill.** Killing the host-side `exec` leaves the child running inside (reparented to the
   container's init) — the same `+1` leftover the microsandbox CLI gave. It needs a process
   group inside the sandbox, or a kill through the joiner.

## The three words that needed explaining

**Rootfs.** It is the file tree a sandbox sees as `/`. microsandbox builds it from the image
and boots it as a disk. youki does not build anything: `config.json` names a directory and
that directory becomes `/`. An empty one is a sandbox with no `sh` and no `apt`. In the spike
the directory was made by hand — download the `debian:stable-slim` layer (29.8 MB), `tar xzf`
it into `rootfs/` (82 MB), point `root.path` at it. Everything installed afterwards landed in
*that* directory: `curl` from apt, bun in `rootfs/root/.bun`, acornfall in `rootfs/opt`. The
host was never touched. What is unbuilt is the bookkeeping around it — one shared base plus a
private writable layer per workspace, update, prune — which is what podman normally owns.

**Joiner.** A small program that puts a new process into a running sandbox's namespaces: one
`setns()` per namespace (user, pid, mount, network, ipc, uts). `youki exec` should be it, and
rootless it is not. Two rules make a hand-written one work, and both are load-bearing:

```python
fds = {t: open(f"/proc/{pid}/ns/{t}") for t in ["user","pid","mnt","net","ipc","uts"]}
# open every fd FIRST — after entering the mount namespace, /proc is the sandbox's
for t in ["user","pid","mnt","net","ipc","uts"]: setns(fds[t])   # user first, else EPERM
os.execv("/bin/sh", ["sh", "-c", 'exec "$@"', "sh", *cmd])
```

It cannot be JavaScript: the worker is bun, and bun cannot call `setns`. So it is a binary the
worker runs, and the acornfall binary is its natural home — `acornfall sandbox-exec --pid N --
<command>`. It also fixes kill: the joiner is inside the pid namespace and can kill the group.

**Nesting.** Inside an acornfall workspace, a second acornfall starts and wants a sandbox of
its own: a box in a box. Two questions hide in that, and they have different answers. Does
acornfall *run* in a youki box — yes, measured: server started, worker ready, `HTTP 200`. Can
the inner acornfall make a youki box — no. The outer engine decides what the inner one can be:

| outer \\ inner | microsandbox | youki | bwrap | host |
|---|---|---|---|---|
| **microsandbox** (a VM) | needs nested KVM | works | works | works |
| **youki** | needs `/dev/kvm` | **refused** | works | works |

The shape of that table is the finding: the cheap engine can live inside the expensive one,
not inside itself. A VM has its own kernel, so namespaces start clean inside it.

## The numbers, together

| | youki | microsandbox |
|---|---|---|
| install | 7.8 MB, vendored | 67 MB, fetched |
| needs | user namespaces | KVM |
| create / start | 38 ms / 6 ms | 259 ms / 192 ms |
| idle accounted memory | 384 KB | a 2 GB cap |
| first output chunk | +18 ms | +31 ms |
| image | ours to unpack (29.8 MB → 82 MB) | the engine's |
| nesting as the outer engine | acornfall yes, youki no | acornfall yes (needs nested KVM) |

## What this does not settle

- **Whether youki is the engine.** bwrap (smaller still, no cgroups of its own, `--proc`
  problem) and podman (keeps `--image`) were not spiked, and the owner's `apt` requirement is
  what makes the rootfs work necessary at all — language toolchains alone would not have
  needed it.
- **The rootfs lifecycle.** Unpack, update, prune, and whether a workspace's layer is an
  overlay upper or a copied tree. This is the largest unbuilt cost and it is not measurable
  by a spike of the runtime.
- **Ubuntu 24.04+.** Its AppArmor policy gates unprivileged user namespaces; this host is
  Debian 13 with no gate. It would have to be detected and refused with a sentence, like
  every other missing capability.
- **macOS.** Refused by name: the engine is Linux-only, as the seam already allows.
