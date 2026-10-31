# ADR 0030 — Nested engines: a bwrap workspace can run another acornd

Status: accepted, built
Date: 2026-10-29

## Context

A bwrap workspace is how acornd is developed: `acornd-dev-01` holds the repository,
an agent runs in it, and the natural next step is that the checked-out acornd runs inside
the workspace. The host engine would make that trivial, but it is an escape hatch for tests:
the working version must be an honest acornd with its own sandbox and its own network.
An agent inside a bwrap workspace reported that `/dev/net/tun` and `/dev/kvm` were missing,
that nested pasta failed, and that a pre-fix probe could stall for ten minutes. The report
was right about the failure path and wrong about the host: on `the host` the workspace was
already carrying egress through the host's pasta, `/dev/net/tun` was present and `0666`, and
`Tier.Default()` chose bwrap. The missing pieces were inside the guest.

## The spike

Measured on `the host`, Debian 13, bubblewrap 0.12.0, pasta `0.0~git20250503.587980c`:

| Attempt | Result |
|---|---|
| Outer bwrap binds `/dev/net/tun`, root-mapped, no caps | `CapEff: 0`; `unshare -Ur` cannot write `uid_map`; pasta prints `Couldn't configure user mappings` and exits 0 |
| Add `CAP_SETUID/CAP_SETGID/CAP_NET_ADMIN/CAP_SYS_ADMIN` | still fails; the bounding set is those caps only |
| Add `--cap-add ALL` | `unshare -Ur` succeeds; nested pasta creates its namespace cleanly |
| Outer bwrap → inner pasta → inner bwrap with `--unshare-all --uid 0` | `bwrap: setting up uid map: Operation not permitted` |
| Inner bwrap **reuses pasta's user namespace** (no `--unshare-user`, no `--uid/--gid`; `--unshare-pid --unshare-uts --unshare-ipc --unshare-cgroup --share-net`) | **works**: inner netns `4026536116` ≠ host `4026533760`, `uid_map 0 0 1`, `curl https://1.1.1.1` → 301 in 0.1s |
| `open /dev/kvm`, `KVM_GET_API_VERSION`, `KVM_CREATE_VM`, `KVM_CREATE_VCPU` inside a bwrap | all succeed, with and without `--cap-add ALL` |
| `msb run --net public debian:stable-slim -- /bin/echo nested-msb-ok` inside a bwrap with the devices, caps and the host's store | `nested-msb-ok` in 0.77s |

Two conclusions: the device bind is necessary but not sufficient, and the third user
namespace is the wall. The fix is caps in the outer sandbox and **reuse** of the namespace
pasta already made, not another unshare inside it.

## Decision

1. **Devices on by default.** A bwrap workspace binds the host's `/dev/net/tun` and
   `/dev/kvm` when they exist, and adds `--cap-add ALL` and `--setenv ACORND_NESTED=1`.
   The caps are what nested pasta needs; the marker is how the inner acornd knows it is
   inside a sandbox. A workspace can withdraw them: `disable_devices` in the registry, the
   API, `acornd workspace add --no-devices`, or the same field on an update. Absent means
   on. A missing device is skipped, not a failed boot.
2. **The nested bwrap reuses pasta's user namespace.** `ACORND_NESTED=1` makes the bwrap
   engine record `nested: true`. With a network, it runs pasta and then bubblewrap without
   `--unshare-user` or `--uid/--gid`, with the individual unshare flags for pid, uts, ipc
   and cgroup, and `--share-net`. The sandbox's user namespace is the one pasta made; the
   sandbox still has its own network namespace. With the network off it keeps its own user
   namespace, which is the shape the nested lifecycle tests already used.
3. **A probe must not trust a zero exit when pasta says it failed.** `probePastaWithin`
   refuses output containing `Couldn't configure user mappings` or
   `Couldn't write to /proc/self/uid_map`, because pasta can print those and still run the
   command. Without this the inner acornd would choose bwrap and then have no network.
4. **Removing a sandbox stops it first.** `Bwrap.Remove` used to delete the config and the
   files while the supervisor, its bubblewrap, and its commands kept running. The lifecycle
   test called `Remove` after `Start` and passed because `List` reads only configs; it left
   three processes per run. Remove is now Stop then delete, and the test asserts the process
   is gone.
5. **The fast tier does not depend on the machine's KVM.** `/dev/kvm` is gathered by the
   caller and passed into `ready.Check`; `TestSatisfiedInstallIsQuiet` states the fact
   instead of reading it. A bwrap workspace can run `make check` on a machine without a
   hypervisor, which is the same workspace the nested acornd is developed in.
6. **The microVM boundary is decided by the same grant.** `Bwrap.Nesting` reports available
   when the machine has `/dev/kvm`; the control hides it when the workspace withdrew the
   devices. A real microVM boots inside the bwrap with the host's `msb` and store; the
   sandbox-tier test is `TestAMicroVMStartsInsideABwrapSandbox`.

## Security

- The caps are in the **outer sandbox's user namespace**, not the host's. The outer bwrap is
  still the boundary; a nested acornd is a guest of it.
- A nested sandbox's egress is the outer workspace's egress: the outer pasta and its routes
  decide what is reachable, so the outer grant is the ceiling and an inner private grant
  cannot exceed it.
- `/dev/kvm` is a virtualization grant. `disable_devices` withdraws it with the tun device.
  The outer workspace's `--refuse-private` still fences the network.
- The inner acornd's console has no token by default and binds inside the outer netns.
  The owner reaches it with Tailscale inside the workspace or a deliberate forward, not by
  the host's tailnet address.

## Consequences

- The working version can be developed and run with its real bwrap engine, not `host`.
- A running sandbox keeps the shape it booted with. `Produce` now refreshes the config for
  the next start before it adopts a running sandbox, so an upgrade that changes a baked
  setting no longer needs the sandbox removed by hand; a stopped sandbox starts from the new
  config. The device grant is baked at creation, so a change to `disable_devices` rebuilds
  the sandbox and stops the worker rather than leaving a guest with devices the record says
  it does not have.
- Nested rootfs copies still cost disk; the bwrap image copy remains the largest gap
  (ADR 0023).
- The UI does not expose `disable_devices` yet; the CLI, API, and registry do.

## Tests

- `TestBwrapBindsDevicesAndGrantsNestedEnginesTheCaps`
- `TestBwrapNestedEgressReusesPastaUserNamespace`
- `TestBwrapConfigRecordsNestedFromTheParent`
- `TestBwrapClaimsNestingWhenKVMIsBound`, `TestBwrapDoesNotClaimNestingWithoutKVM`
- `TestAPastaThatCannotMapItsUserNamespaceIsRefused`
- `TestBwrapHasALifecycleLikeEveryOtherEngine` (Remove stops the process)
- `TestDevicesAreOnByDefaultAndTheWorkspaceCanWithdrawThem`
- `TestAWorkspaceUpdateRebuildsTheSandboxWhenDevicesChange`
- `TestSatisfiedInstallIsQuiet` with the hypervisor fact in its input
- `TestAMicroVMStartsInsideABwrapSandbox` (sandbox tier: a real microVM, the host's `msb` and
  store)
- `TestProduceRefreshesTheConfigOfAnExistingSandbox`, `TestBwrapPrepareWritesTheCurrentConfig`

The live chain on `the host` is the evidence behind the table above.
