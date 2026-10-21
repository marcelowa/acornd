# ADR 0025 — A sandbox's egress is the public internet, unless it was granted more

Status: accepted (2026-10-26)

## The problem

A workspace with its network on reaches whatever the machine that runs it can reach. That is not an
accident of one engine: `pasta`, `slirp4netns`, the microVM's user-mode network — every one of them
is a userspace NAT, which means the sandbox's packets are dialled **from the machine**, and the
machine's routing table, its resolver, and its own identity on every network it belongs to apply to
them.

Measured on a machine that is a Tailscale node, from inside a workspace that had installed nothing:

```
$ bash -c 'exec 3<>/dev/tcp/100.64.0.21/7777 && echo connected'   # a tailnet peer
connected
$ cat /etc/resolv.conf
nameserver 100.100.100.100                                         # MagicDNS
```

No credentials, no proxy, no tailnet client, nothing in the workspace's own record. Peers saw the
machine's node. A workspace could therefore reach private infrastructure — internal services,
admin interfaces, anything listening on the tailnet — that was never granted to it, and could use a
private endpoint for exfiltration where no public egress is involved.

Two reports found this independently, and were right about the effect and (in one case) wrong about
the cause: the sandbox does **not** share the machine's network namespace, it has its own
(`lo`, `eth0` at `10.0.2.15/24`); it inherits the machine's reach because the NAT dials from the
machine.

A related finding came with it, and is fixed separately (ADR 0023): `pasta`'s port forwarding
defaults to `auto`, which mirrored every service the machine listens on into the sandbox's
loopback, and its gateway translation mapped `10.0.2.2` onto the same loopback. That let a workspace
talk to the acornfall that ran it. Both are off now.

## The decision

**Egress means the public internet. Private networks are a grant a workspace either has or does
not, and the default is not.**

A workspace without the grant is given routes inside its own sandbox that refuse every range that
means "inside":

```
10.0.0.0/8  100.64.0.0/10  169.254.0.0/16  172.16.0.0/12  192.168.0.0/16
fc00::/7  fe80::/10
```

A route that says `unreachable` is the whole mechanism: the packet never leaves the sandbox, so
nothing on the machine or its overlay is dialled, and nothing on the machine has to be configured,
no root is needed, and no firewall rule can drift from the workspace's own record. The connected
route to `10.0.2.0/24` is more specific than the refused `10.0.0.0/8`, which is what keeps the
sandbox's own gateway and DNS reachable.

DNS follows from the same decision. A sandbox without the grant cannot read the machine's resolver
file, because that resolver is usually itself on a private address (MagicDNS at `100.100.100.100`,
systemd-resolved at `127.0.0.53`). It reads a file naming `10.0.2.3`, which `pasta` forwards to the
machine's own resolver from the machine's side — a name resolves without a route into anything.

With the grant, nothing is refused and the machine's own resolver file is used: a workspace that was
given the machine's networks is given its resolver too.

## What it costs

- **A workspace that needs the machine's LAN must ask.** That is the point, and it is one switch
  (`--private`, or the dialog's *private networks* toggle) recorded on the workspace, not in an
  engine's configuration.
- **Public IPv6 egress stays.** Only private and link-local IPv6 ranges are refused.
- **ICMP still does not work through `pasta`**, grant or no grant (ADR 0023).

## What it does not solve

- **The microVM engine.** This is built into the bubblewrap invocation, where the sandbox's routes
  are ours to write. A microVM's network belongs to the engine, and `microsandbox` exposes an on/off
  switch rather than a route table. The grant is in the policy for both engines, so the second half
  is the engine's to grow; until then a microVM workspace with its network on reaches what the
  machine reaches.
- **Exfiltration over public endpoints.** A workspace can still send anything it can read to any
  public address, and DNS queries still leave through the machine's resolver as the machine. This
  ADR is about which destinations are reachable, not about what may be said to them.

## Tests

- `TestBwrapRefusesPrivateNetworksWithoutAGrant` — the argv refuses the ranges and carries pasta's
  DNS.
- `TestBwrapAGrantReachesPrivateNetworks` — the grant removes both, and the argv shows it.
- `TestABwrapSandboxCannotReachPrivateNetworks` — the bwrap tier listens on the machine's own private
  address and fails if a workspace can reach it.
- `TestAPrivateNetworksGrantReachesThePolicy` — the workspace's field is the engine's policy.
