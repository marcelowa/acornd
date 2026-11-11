# ADR 0018 — Disable VM nesting: an opt-out, granted by the engine and the machine

Status: accepted, built — armed by the 0.7.13 pin (2026-11-03): the switch is a flag, and
nesting is on unless the workspace opts out. The engine is told either way
Amended: 2026-11-07 — on bwrap the refusal is enforced by the kernel: a workspace that
disables nesting is built with `--disable-userns --assert-userns-disabled`, so it loses
containers and nested bubblewrap too, not only a hypervisor. Two switches meaning "do not
nest" was rejected; this one is the leaf.
Date: 2026-10-09

## Context

A sandbox cannot hand its guest the virtualization CPU flag by itself. The engine has to ask
its hypervisor for nesting — the flag's presence in the guest is what a nested `/dev/kvm`
needs — and the machine has to be able to grant it: a Linux host with VT-x or AMD-V and
`nested` enabled on its KVM module, or an Apple Silicon M3 or later on macOS 15+.

The engine acornd pins, microsandbox 0.7.6, has no switch at all: its guests nest whenever
the machine can. Its successors 0.7.7 and 0.7.8 added a CPUID mask to their krun crate with
the flag off and no way to ask for it, so their guests never nest — the regression upstream
recorded as [#1778](https://github.com/superradcompany/microsandbox/issues/1778). The
upstream fix is [microsandbox#823](https://github.com/superradcompany/microsandbox/pull/823):
`msb create --nested-virt`, off by default, "startup fails if the host cannot honor an
enabled setting". It was blocked on
[libkrun#155](https://github.com/superradcompany/libkrun/pull/155) and
[libkrunfw#32](https://github.com/superradcompany/libkrunfw/pull/32), and no release carried
it — v0.7.8, the latest, still had no flag. 0.7.6 was pinned on purpose, not left behind: a
sandbox nested meanwhile, while the switch that would make it a choice was still upstream.
v0.7.9 (2026-10-22) shipped #823, and the pin has followed it since: 0.7.14 is the release
acornd runs.

This surfaced from use, not from theory: acornd is developed inside an acornd workspace,
and its own sandbox tier (`make test-sandbox`) needs `/dev/kvm` inside that workspace's guest.

## Decision

A per-workspace `disable_vm_nesting`. Absent or false allows nesting wherever it can be
granted; true refuses it. There is no install-wide switch: a refusal belongs to a workspace, and
a machine-wide one only made sense while every workspace was the same decision.
Nesting is allowed unless refused, and the refusal behaves in three ways:

- **Asked only where it can be granted, and said either way.** The switch is a flag and not a
  field: the engine reads `sandbox.yaml` strictly and has no nesting key in it, so the
  generated file cannot carry this. The create call does, and it always says something —
  `--nested-virt` where the machine can nest, `--nested-virt=false` where it cannot — so no
  engine-side configuration and no engine default decides for a workspace. A default must not
  cost anybody their workspace, and the hosts that cannot nest are common: M1/M2 Macs, cloud
  VMs, CI runners, machines with the host kernel's `nested` off. Asking is what would cost the
  workspace there, since an enabled setting fails the boot; stating off costs nothing.
- **Refused explicitly.** A workspace that turns nesting off is told to the engine anyway,
  `--nested-virt=false`, the way `network: none` is written out so an engine default cannot
  overrule a workspace.
- **Never left to a default.** What is decided is said: `doctor` prints availability and
  the reason it is not, and the workspace state carries whether the next sandbox will nest
  (`nested`) or why it will not (`nestedReason`). A workspace that allows nesting and does
  not get it is a sentence, not a surprise. A workspace that disables nesting carries
  neither: its own setting, visible beside the state, is the reason.
- **Enforced by the kernel where the engine can.** On bwrap, a workspace that disables
  nesting is built with `--disable-userns --assert-userns-disabled`, so a user namespace
  inside its sandbox is refused rather than merely not asked for. `--unshare-all` is not
  enough for bubblewrap's check — `--disable-userns` is refused unless `--unshare-user` is
  named — and `--assert-userns-disabled` turns a kernel that cannot install the filter into
  a failed boot instead of a promise kept silently. It applies where the sandbox has a user
  namespace of its own: a nested sandbox reuses its parent's and cannot carry the flag, and
  a parent that refuses namespaces never starts one.

Nesting is decided when a sandbox is created; changing the setting applies to the next
sandbox, exactly like the image, because the running sandbox is where dependencies live.

## Alternatives

**Nesting as a requirement.** Rejected: upstream's semantics for an enabled setting is that
startup fails when the host cannot honor it. Requiring nesting would mean every sandbox
creation failing on every host that cannot nest — for workspaces that never wanted a VM
inside a VM.

**Disabled by default.** Rejected as the default, though both the flag and the field exist.
The projects that want nesting are the ones this product is for — developing acornd
itself, emulators, container-in-sandbox work — a wrong guess is cheap by construction (the
sandbox boots, the surfaces say why not), and the machine-level answer is one word to
change. The engine's own default is off because it cannot know the host; acornd can look.

**A second switch for user namespaces.** Rejected: `disable_vm_nesting` already promises a
leaf, and a field beside it that also meant "do not nest" would leave the first one
half-enforced and the two ambiguous. A workspace that wants containers inside its sandbox
leaves nesting on.

**Deeper checks than we do.** Deferred: the host checks are deliberately conservative (a
kernel parameter, a chip generation) and answer "no" when they are unsure, which is the safe
direction. The engine's own check remains the authority once the flag is passed.

## Consequences

- The pin carries the switch, so both directions are armed: a workspace that disables
  nesting now gets a sandbox that cannot nest, which is the half 0.7.6 could not honor.
  `doctor` and the state report `nested: true` where the host half agrees, and the reason
  where it does not.
- Nesting is still not in the generated policy file, and cannot be: the pinned engine's
  `sandbox.yaml` refuses unknown fields and has no nesting key. `microsandbox.go` renders it
  into the create call instead, beside `network: none`, and a person reading the policy file
  will not find it there. "Explicit, never a default" is a rule about the create call for this
  setting.
- `acornd doctor` grows a nesting line, text and JSON; the state frame grows two fields.
- The install-wide switch is gone (it was `--disable-vm-nesting`): a workspace decides, and
  a refusal is recorded on the workspace rather than on the machine. The remaining difference
  is still on purpose: acornd's setting says what it refuses,
  the engine's `--nested-virt` says what it does with a sandbox.
- Tests: the machines' answers (Linux KVM parameters, Apple chip and OS version) are pure
  functions, tested on every platform; the create path — the flag, the explicit false, or
  nothing — is pinned against a fake engine. The flag itself reaches a guest in
  `TestALiveSandboxNestsOnlyWhenTheWorkspaceAllowsIt`: a real microVM nests, and a workspace
  that refuses nesting gets a guest with no hypervisor in it.
