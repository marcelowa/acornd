# ADR 0026 — A rootless host, and the engine a machine actually gets

Status: accepted (2026-10-26), built; vendoring rejected (2026-10-26)

## The problem

acornd was only ever run on a machine where root had prepared things: `apt install bubblewrap
passt iproute2`, and a `/dev/kvm` whose group the user was in. Every piece of that was invisible
until it was missing, and then it was a kernel error in the middle of a turn.

Two questions followed. Can acornd run for a user with no root at all? And what engine does a
workspace get when nobody chose one?

## What needs root, and what does not

Measured on a machine where the gateway ran as an ordinary user:

| Step | Root? |
|---|---|
| user namespaces, mounts, the sandbox itself | **no** — `bwrap` needs only an unprivileged user namespace |
| the sandbox's network, its address, its routes | **no** — `pasta` makes a namespace, and setting it up needs `CAP_NET_ADMIN` there, not here |
| installing into the sandbox, pulling images, running commands | **no** |
| the acornd gateway, its console, its updates | **no** — port 7777 is above 1024 and its folder is the user's |

What does need root, once, and can never be vendored:

| Step | Why | Cost of not doing it |
|---|---|---|
| `kernel.apparmor_restrict_unprivileged_userns=0` (Ubuntu 23.10+) | the kernel refuses unprivileged user namespaces to unconfined binaries | every engine that isolates anything is unavailable; `host` is left |
| `/dev/kvm` readable (`kvm` group) | the microVM engine's device | the microVM engine is unavailable; `bwrap` still works |
| `loginctl enable-linger` | a user service that survives logout | the gateway runs only while a session does |
| installing into `/usr/bin` | not needed at all: the data folder is the user's | — |

## The decision

**1. One command says what the machine needs, and does it** (`acornd setup`), in whichever of
three shapes the machine allows: already root, the plan is shown and applied on a `y`; an ordinary
user, the plan is shown and `sudo` asks for the password; neither, the plan is printed as commands
that can be run. The probes belong to the **account that runs acornd**, never the one typing:
root can open `/dev/kvm` and make a user namespace whatever the policy says, so a check run as
root would report a machine that works while the service's workspaces cannot boot. That account is
the owner of the acornd folder, else the user its systemd unit runs as, else root itself — and
`--for` overrides all three, because a guess that is wrong should be visible in the output rather
than discovered later. When the account is not this process's, root re-executes the same binary
with that account's ids and groups to ask. When a step needs
root the command writes it to a plan, says why, and runs the same binary through `sudo`. The privileged half
reads the plan as data and builds the commands itself, refusing anything outside its own short
vocabulary, so escalation is "these four machine-level steps" rather than "whatever a file said".
`--apply` no longer exists: a second invocation with a flag teaches the wrong habit. iproute2 is
gone entirely: the sandbox's address and routes are set over netlink by `acornd netns` (a
subcommand pasta runs as the sandbox's first process), so a refusal that cannot be installed fails
the start instead of being swallowed by a shell script.

**What the step changes, and for how long.** The user-namespace policy is written to
`/etc/sysctl.d/99-acornd-userns.conf` and not only applied, because a fix that disappears at the
next reboot is worse than one that was never made; `/dev/net/tun` likewise gets a
`/etc/tmpfiles.d` rule so the device is there after a boot. In a container neither is the answer:
the policy and the device belong to whoever started the container, so the command says so and
stops rather than proposing something that cannot work.

**Vendoring the tools is rejected, not deferred.** The alternative was to ship pinned static
`bwrap` and `pasta`, fetched into the data folder and used where the machine has none. It was
considered and dropped for good:

- bubblewrap hard-requires libcap, so a static build needs a static libcap **and** a musl cross
  toolchain for every architecture, built from pinned sources in the release workflow;
- what we build, we own: a vendored CVE in bubblewrap, libcap, or pasta becomes ours to track,
  patch, and re-release, on a project whose whole distribution story is "the distribution's
  packages";
- the price it removes is one `apt install` at setup, once, which the command already names and
  performs.

So the tools come from the distribution, `acornd setup` installs them, and the seamlessness
this ADR promised is one command rather than zero — permanently. If a future machine cannot
install packages at all, that is a different problem (an image to build), not a reason to carry
three C libraries through our own release pipeline.

**2. An engine a machine cannot run is not the default.** The preference order is `bwrap`,
`microsandbox`, `host`: bubblewrap first because it needs no hypervisor, no root, and no device;
the microVM next as the stronger boundary when a machine can give it one; the host last because it
isolates nothing, so it is what is left rather than what is preferred. A workspace that names no
engine gets the first one in that order that can run a workspace **with its network on** — the
driver answers that with `Egress`, separately from `Usable`, because a machine can run a sandbox
without being able to give it a network. An engine that cannot is not a default.

**3. The choice is recorded when the workspace is made**, by the console, the API, and
`acornd workspace add`, each of which asks this machine the same question. An empty field means
"whatever this machine prefers", which is exactly the drift the engine rules forbid: a workspace
that woke up on another machine would silently run somewhere nobody chose.

A record that still names no engine — everything made before this change — is *not* rewritten
automatically: it was running the old default, microsandbox, and resolving it to the new
preference would move a running workspace onto another engine without anybody choosing. Those are
recorded by hand (`acornd workspace list` shows which), and until they are, they follow the
machine's preference, which on a machine that can run bubblewrap means bubblewrap.

## What this trades

The microVM was the default *because* it is the stronger boundary — a shared kernel is a weaker
one. Making bwrap the default says cheap, nesting, and rootless beat boundary strength for the
common case, and the microVM becomes the deliberate choice for anyone who wants the stronger wall.
It also has an effect worth naming: the engine whose egress is fenced (ADR 0025) is now the common
one, and the engine whose network still belongs to the engine is opt-in. That is a side benefit,
not a substitute — the microVM gap is still open (ADR 0025 says so).

## Not in scope

- **No silent privilege.** `acornd doctor` reports what this machine needs; nothing runs `sudo`
  on its own, and no sysctl is written by a daemon.
- **No rootless microVM.** `/dev/kvm` is a device; there is no rootless variant of it.
- **A container host** may deny user namespaces through seccomp, which no acornd change can
  fix; the refusal says so and names the flag.

## Tests

- `TestTierEmptyNameIsWhatTheMachineCanRun` — the default is what this machine can run, network
  and all, and the preference order is bwrap first, host last.
- `TestBwrapEgressProbeSaysWhatToDo` — pasta missing names the package; a refused namespace is
  passed through with its reason.
- `TestBwrapRefusesWhenTheMachineWillNotMakeAUserNamespace` — the refusal names the sysctl.
- `TestAnUnnamedEngineIsRecordedOnce` — a record that named none is written down once, and a
  workspace that named an engine keeps it.
