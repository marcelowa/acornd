# ADR 0026 — A rootless host, and the engine a machine actually gets

Status: accepted (2026-10-26), partly built

## The problem

acornfall was only ever run on a machine where root had prepared things: `apt install bubblewrap
passt iproute2`, and a `/dev/kvm` whose group the user was in. Every piece of that was invisible
until it was missing, and then it was a kernel error in the middle of a turn.

Two questions followed. Can acornfall run for a user with no root at all? And what engine does a
workspace get when nobody chose one?

## What needs root, and what does not

Measured on a machine where the gateway ran as an ordinary user:

| Step | Root? |
|---|---|
| user namespaces, mounts, the sandbox itself | **no** — `bwrap` needs only an unprivileged user namespace |
| the sandbox's network, its address, its routes | **no** — `pasta` makes a namespace, and setting it up needs `CAP_NET_ADMIN` there, not here |
| installing into the sandbox, pulling images, running commands | **no** |
| the acornfall gateway, its console, its updates | **no** — port 7777 is above 1024 and its folder is the user's |

What does need root, once, and can never be vendored:

| Step | Why | Cost of not doing it |
|---|---|---|
| `kernel.apparmor_restrict_unprivileged_userns=0` (Ubuntu 23.10+) | the kernel refuses unprivileged user namespaces to unconfined binaries | every engine that isolates anything is unavailable; `host` is left |
| `/dev/kvm` readable (`kvm` group) | the microVM engine's device | the microVM engine is unavailable; `bwrap` still works |
| `loginctl enable-linger` | a user service that survives logout | the gateway runs only while a session does |
| installing into `/usr/bin` | not needed at all: the data folder is the user's | — |

## The decision

**1. One command says what the machine needs** (`acornfall setup`), and the machine-level parts
of it are one more (`sudo acornfall setup --apply`). The probes are the user's, run as the user:
root can always make a user namespace, so asking as root answers a different question. iproute2 is
gone entirely: the sandbox's address and routes are set over netlink by `acornfall netns` (a
subcommand pasta runs as the sandbox's first process), so a refusal that cannot be installed fails
the start instead of being swallowed by a shell script.

**Still open: shipping the tools.** `bwrap` and `pasta` should be pinned static binaries fetched
into the data folder, used when the machine has none, and refreshed with the release. That is not
built yet, and it is not a small piece of CI: bubblewrap hard-requires libcap, so a static build
needs a static libcap and a musl cross toolchain for both architectures **built from pinned
sources in the release workflow**, which we then have to keep patched. Until it exists, the tools
come from the distribution, `setup` names the package, and this ADR's honesty is that the
"seamless install" is one command rather than zero.

**2. An engine a machine cannot run is not the default.** The preference order is `bwrap`,
`microsandbox`, `host`: bubblewrap first because it needs no hypervisor, no root, and no device;
the microVM next as the stronger boundary when a machine can give it one; the host last because it
isolates nothing, so it is what is left rather than what is preferred. A workspace that names no
engine gets the first one in that order that can run a workspace **with its network on** — the
driver answers that with `Egress`, separately from `Usable`, because a machine can run a sandbox
without being able to give it a network. An engine that cannot is not a default.

**3. The choice is recorded when the workspace is made**, by the console, the API, and
`acornfall workspace add`, each of which asks this machine the same question. An empty field means
"whatever this machine prefers", which is exactly the drift the engine rules forbid: a workspace
that woke up on another machine would silently run somewhere nobody chose.

A record that still names no engine — everything made before this change — is *not* rewritten
automatically: it was running the old default, microsandbox, and resolving it to the new
preference would move a running workspace onto another engine without anybody choosing. Those are
recorded by hand (`acornfall workspace list` shows which), and until they are, they follow the
machine's preference, which on a machine that can run bubblewrap means bubblewrap.

## What this trades

The microVM was the default *because* it is the stronger boundary — a shared kernel is a weaker
one. Making bwrap the default says cheap, nesting, and rootless beat boundary strength for the
common case, and the microVM becomes the deliberate choice for anyone who wants the stronger wall.
It also has an effect worth naming: the engine whose egress is fenced (ADR 0025) is now the common
one, and the engine whose network still belongs to the engine is opt-in. That is a side benefit,
not a substitute — the microVM gap is still open (ADR 0025 says so).

## Not in scope

- **No silent privilege.** `acornfall doctor` reports what this machine needs; nothing runs `sudo`
  on its own, and no sysctl is written by a daemon.
- **No rootless microVM.** `/dev/kvm` is a device; there is no rootless variant of it.
- **A container host** may deny user namespaces through seccomp, which no acornfall change can
  fix; the refusal says so and names the flag.

## Tests

- `TestTierEmptyNameIsWhatTheMachineCanRun` — the default is what this machine can run, network
  and all, and the preference order is bwrap first, host last.
- `TestBwrapEgressProbeSaysWhatToDo` — pasta missing names the package; a refused namespace is
  passed through with its reason.
- `TestBwrapRefusesWhenTheMachineWillNotMakeAUserNamespace` — the refusal names the sysctl.
- `TestAnUnnamedEngineIsRecordedOnce` — a record that named none is written down once, and a
  workspace that named an engine keeps it.
