# ADR 0039 — A sandbox's root is an overlay, not a copy

Status: accepted, built
Date: 2026-11-08
Amended: 2026-11-08, while building it — the overlay only works where bubblewrap is the only
user namespace in the chain. Pasta makes the namespace an egress sandbox lives in, and the kernel
refuses an overlay mount inside it (measured: EPERM, with a writable upper), and bubblewrap will
not join the caller's own namespace (`--userns /proc/self/ns/user` is refused as a non-descendant).
So the overlay serves an egress-less sandbox only; a workspace with egress, or one that disables
nesting, keeps the copy.

## Context

ADR 0023 gave a bwrap workspace a root of its own by copying the unpacked image into the
workspace's state folder; ADR 0030 recorded the bill as the largest gap left in the engine: every
workspace pays the image once, and pays it again whenever it is recreated.

The parts were already there. `imageBase` (`bwrap.go:851`) unpacks an image **once per tag**
into a shared `images/<key>/base` and every workspace copies that tree (`workspaceRootfs`,
`bwrap.go:821-849`). The copy is what a workspace's installs land in, and the marker
`.acornd-rootfs` is what says the copy is finished. So the shared read-only tree this ADR wants
already exists; the per-workspace copy is the thing that costs.

## The spike

Measured in a bwrap workspace on Debian 13, bubblewrap 0.12.0, kernel 7.0.14, ext4 on LVM, with
the gateway's own `debian:stable-slim` base:

| Attempt | Result |
|---|---|
| The copy one workspace gets today | **82,796,930 bytes (79.0 MiB)**, `cp -a` in 131 ms on this disk |
| `bwrap --overlay-src <base> --overlay <upper> <work> /`, then `apt-get update && apt-get install less` | **works, 7.7 s**; `less` answered `/usr/bin/less` |
| The shared base after that install | untouched: no `usr/bin/less` in it |
| The workspace's upper after that install | **22,885,949 bytes (21.8 MiB)** — the delta, apt's lists included |
| A second sandbox over the same base | does not see the first one's install |
| `cp --reflink=always` on the same filesystem | refused: `Operation not permitted` |

Two things follow. The overlay costs the base once for every workspace that shares the image and
then only what a workspace changes; today it costs the base once **per workspace**. And the
filesystem cannot clone, so the copy cannot be made cheap — it is the overlay or nothing.

## Decision

1. **The image base is the lower, one per image tag.** It stays where it is
   (`images/<key>/base`), read-only in practice and shared by every workspace built on it. No
   workspace writes into it.
2. **A workspace gets an upper and a work dir, not a rootfs copy.** Provisioning creates
   `<state>/<workspace>/upper` and `<state>/<workspace>/work` and writes into the upper the two
   things a workspace owns before its first command: the `.acornd-rootfs` marker and the apt line
   that keeps apt from dropping to `_apt` (`APT::Sandbox::User "root"`). Overlayfs merges
   directories, so both appear at the paths they have today.
3. **bubblewrap mounts the overlay at `/`.** The argv gains
   `--overlay-src <base> --overlay <upper> <work> /` where it had `--bind <rootfs> /`; every other
   mount (resolver, devices, workspace, home, run, supervisor) layers on top of it, unchanged.
   The spike ran exactly this shape. It applies where bubblewrap is the only user namespace in
   the chain: pasta makes the one an egress sandbox lives in, and an overlay mount inside it is
   refused, so a workspace with egress keeps a copy — as does one that disables nesting, whose
   `--disable-userns` needs an explicit `--unshare-user`, inside which the overlay is refused too.
4. **The config says which base a workspace was built on.** A workspace's upper only means
   anything against the lower it was made for. The config records the base path; a workspace whose
   image field changes keeps the base it was built on, which is what a copy did (the marker was
   written once and never compared), and the state says so rather than leaving it to be discovered.
5. **A machine that cannot overlay keeps the copy.** Overlayfs inside a user namespace needs Linux
   5.11 or later, and a filesystem that supports it. `Prepare` probes once — mount a throwaway
   overlay with bwrap, or read the kernel's answer — and records `overlay` or `copy` in the
   config. The fallback is the code that runs today, so an old kernel loses a disk win and nothing
   else. The probe is the same shape as pasta's: a capability that is measured, not assumed.

## What it costs

- **The win is smaller than the spike promised.** The 79.0 MiB per workspace is real, and only an
  egress-less workspace stops paying it. A workspace with egress keeps the copy: the namespace
  pasta makes is not one the kernel will mount an overlay in. Closing that gap means putting
  bubblewrap outside pasta, which is a different network layering and a different ADR.
- **The root is no longer a tree.** A workspace's files are split between the shared base and its
  upper: `du` on the workspace folder no longer says how big its root is, `ls` shows the merged
  view only from inside, and backing up a workspace folder alone no longer restores its sandbox —
  it restores the delta and needs the base beside it.
- **A base outlives the workspaces built on it**, as the image cache already does. Something has
  to collect it, or the cache grows one tree per image tag ever used. `acornd workspace purge` is
  the natural place, with the same rule the image cache has: a base nobody names can go.
- **Overlayfs has a visible shape.** Inode numbers are overlay's, not the image's; hard links
  between a lower file and an upper file cannot be made; a tool that assumes one device under `/`
  sees two. The sandbox tier runs apt, installs, and a nested microVM, which is the evidence that
  the ordinary tools do not care.
- **A write that is a copy is a copy.** Installing into a workspace's upper costs what it did
  before, minus the untouched base. The win is the base, not the delta.
- **Removal gets simpler and sharper.** `Remove` deletes `upper` and `work`; the base stays for
  the next workspace on that image. There is no half-copied tree to clean up, which also removes
  the reason the copy was written whole and replaced.

## Rejected

- **Reflink (`FICLONE`)** — the cheapest real copy where the filesystem supports it, and this
  machine's does not (`Operation not permitted` on ext4). A best-effort `cp --reflink=auto` would
  be a silent fallback to the copy on most Linux installations, which is a win that depends on a
  filesystem nobody chooses for this.
- **A hard-link farm** — cheap and wrong: an in-place write in one workspace would change every
  other workspace's copy of that file, and package managers do write in place.
- **A FUSE copy-on-write layer** — a daemon per sandbox and a new failure mode under it, to avoid
  a mount the kernel already provides.
- **Podman's overlay graph** — a storage driver with its own privileges and layout, for one
  feature; acornd would inherit a dependency it does not otherwise need.
- **Keeping the copy and deduplicating later** — nothing on this machine can deduplicate after
  the fact, so the bill stays.

## Tests

- `TestBwrapSharesTheImageBaseWithAnOverlay` — the base is the lower, the workspace's two files
  are in the upper, and the argv mounts them together with no explicit user namespace.
- `TestBwrapCopiesTheImageWhenTheSandboxHasEgress` — pasta's namespace is not one an overlay
  mount survives, so an egress workspace gets the copy.
- `TestBwrapLeafKeepsTheCopyForItsUserns` — the kernel-enforced leaf needs `--unshare-user`, and
  the copy comes with it.
- `TestBwrapKeepsTheCopyAWorkspaceAlreadyHas`, `TestBwrapKeepsTheBaseAWorkspaceWasBuiltOn` — the
  two compatibility rules: an old workspace keeps its tree, and a delta keeps the base it was made
  against.
- `TestBwrapCreateWritesWhatTheWorkspaceRunsUnder` — the fallback, with a machine that refuses
  the overlay: the copy, its marker, and apt's line.
- The bwrap tier — six tests that install, write, substitute a secret, refuse private networks and
  run a nested egress-less sandbox — runs with the overlay where the guard allows it and with the
  copy everywhere else; that is the proof the sandbox is still a sandbox.
- The spike's own numbers are the table above, measured with the argv in decision 3 and the
  gateway's `debian:stable-slim` base; it is not a test because it tests bubblewrap.
