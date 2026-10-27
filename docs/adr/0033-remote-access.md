# ADR 0033 — A browser reaches the machine through a tunnel we configure

Status: proposed — awaiting the owner's sign-off
Date: 2026-10-30

## Context

[ADR 0032](0032-who-may-call-the-console.md) decided who may call the console and deliberately left
out how a browser reaches the machine at all. The people involved are remote, the machine sits at
home behind a NAT it does not control, and once it has people the console listens on `127.0.0.1`
and nowhere else. So something has to carry a request from a browser to a loopback port, and decide
who is allowed to make one before the request ever arrives.

The constraint that removed the cheapest options was a product one, not a security one: **a family
member installs nothing.** That rejected Tailscale, which is otherwise the best of the lot — a
WireGuard tunnel, a certificate nobody manages, and tailnet identity for free.

| option | why not |
|---|---|
| port forward + our own TLS | the home IP is public, a router rule is somebody's evening, and the identity layer is then ours to build and operate |
| Google IAP + load balancer + connector | a domain, a managed certificate, a connector, a monthly bill — for a signed JWT that is the shape 0032 already verifies |
| Tailscale Serve | every family member installs an app |
| a quick tunnel | the hostname changes on every restart, so the bookmark breaks |
| Tailscale Funnel | public, and no identity at all, which removes the thing being built |
| a self-hosted proxy (Traefik/Authelia) | we would be operating an identity provider |

**Cloudflare Tunnel + Access** is what is left, and it is a good fit rather than a compromise: a
signed assertion we verify the same way, an allowlist that can be driven entirely through an API, a
one-time-PIN identity provider that needs no account of any kind, nothing listening inbound, and a
free tier. The owner already has the domains and the account.

Three things in this repository make it cheaper than the alternatives: the `identity.Email` seam
from 0032 means the verifier is one implementation behind a table, the update tier already downloads
a third-party binary and verifies a checksum ([ADR 0019](0019-self-update.md)), and the readiness and
supervision machinery ([ADR 0024](0024-one-lifecycle.md), [ADR 0009](0009-liveness-and-recovery.md))
is what a long-lived sidecar needs.

## Decision

### 1. The tunnel is the only listener, and the gateway keeps its loopback port

`cloudflared` runs on the machine, dials out, and its remotely-managed ingress sends everything to
`http://127.0.0.1:7777`. No port is opened, no certificate is renewed by us, no address record is
ours to keep correct. The rule from 0032 §8 stands: an install with people refuses a non-loopback
`--host`, because a door on one address is not a door.

### 2. The owner pastes one token, and the console does everything else

A setup screen takes an API token and a subdomain of a zone already in the account. The token is
validated by probing each scope it must carry, and a missing one is named in a sentence rather than
surfacing later as a `403` in a log:

| scope | why |
|---|---|
| Account · Cloudflare Tunnel · Edit | create the tunnel, write its ingress |
| Zone · DNS · Edit, that zone only | the `CNAME` for the hostname |
| Account · Access: Apps and Policies · Edit | the application, its policy |
| Account · Access: Organizations, IdP, Groups · Edit | the group, and the one-time-PIN provider |

Then one `apply` creates, in order: the tunnel; its ingress configuration; a proxied `CNAME`
`<sub>.<zone>` → `<tunnel-id>.cfdargotunnel.com`; the one-time-PIN identity provider, if the account
has none; the Access application for that hostname; an Access group named `acornfall`; and one
policy — allow, when in that group. Every id is written to `<data-dir>/remote.json`, so a second run
is a no-op and a run that died halfway resumes from the ids it already has.

### 3. Declared state, reconciled — not a wizard that ran once

`remote.json` is the intent: `{enabled, hostname, zone, tunnel, app, group, idp}`. A reconciler makes
Cloudflare match it at boot and after any change to People, and repairs drift instead of leaving it
to be discovered. **The group is derived from `people.json`** and is never edited by hand: 0032
decided the console owns the list and the door enforces it, and this is the mechanism that keeps
that true. A person added in the console is in the group; a person revoked is out of it.

What a reconcile did, and anything it could not do, is one line per step in the console and in
`acornfall remote status`.

### 4. The running process holds the tunnel token, not the API token

> [!WARNING]
> The API token can rewrite DNS for the zone and every Access application on the account. It is the
> most powerful credential in the install.

So it is used at setup and reconcile and then offered for deletion (`acornfall remote forget-token`);
what the supervised process holds is the tunnel token, which can run exactly one tunnel and nothing
else. A leaked tunnel token lets someone else be your origin — Access still gates what reaches them,
but it is a real leak, so rotating it is a button and the sentence that says so sits next to it.

### 5. `cloudflared` is ours: pinned, verified, supervised

Downloaded into `<data-dir>/bin/cloudflared` at a pinned version, verified against a checksum, the
same shape the worker tarball already takes ([ADR 0019](0019-self-update.md)). It is restarted on
exit with backoff, and its state is reported by readiness and shown as one line: running, restarting,
or not started and why. A tunnel that dies is a visible degradation; it is not an outage nobody
notices until a family member says the page does not load.

### 6. The verifier is `access`, and it verifies

`ACORNFALL_IDENTITY=access`, keys from `ACORNFALL_IDENTITY_KEYS` (default the team's
`/cdn-cgi/access/certs`), `aud` equal to the Access application's tag, `iss` equal to the team domain,
and every rule in 0032 §2 — expiry, `email_verified`, leeway, cache, refetch on an unknown `kid`.
The plaintext `Cf-Access-Authenticated-User-Email` header is never read, for the same reason IAP's
plaintext header is not.

### 7. The door admits exactly the group, and there is no other mode

There is no "open" setting and no waiting room — 0032 removed `pending`, and a policy that admits
every verified email would have nothing to do with the person it admitted. Access lets in the group
and nobody else; the gateway refuses an email it does not know even if Access lets it through, so a
mistake at the edge is a refusal rather than an account.

### 8. One button that checks the door, from outside

The last step of setup fetches the public hostname and expects a `302` to the team's login domain;
the console shows the result. It is the same assertion the live test makes, so the button cannot rot
without a test failing.

## What the family member experiences

1. Opens a link the owner sent, in any browser.
2. A Cloudflare page asks for their email address.
3. Cloudflare emails them a code; they paste it.
4. The console opens on their **Personal** workspace, with a model already chosen.

No account, no password, no app, and nothing to remember but the bookmark.

## What it costs

- **A domain, about ten dollars a year.** No monthly fee, no certificate, no load balancer.
- **Cloudflare terminates TLS, so it sees the conversation bytes in flight.** It stores nothing, and
  it is the same fact that IAP carried; it is stated rather than implied.
- **Remote access depends on a third party.** If Cloudflare is down, family cannot reach the machine.
  The owner is not affected: the local door is a file and a loopback port.
- **One more process to supervise and one more binary to trust.** Pinning plus a checksum is the
  mitigation, and it is the same mitigation that already exists.
- **One more state file, and a reconciler that has to be correct.** This is the part most likely to
  produce a confusing bug, which is why the plan for it is tested against a fake client rather than
  discovered in production.

## Alternatives

**Do it without a tunnel: forward a port and run Caddy.** Nothing monthly, but the home IP is
public, a router rule has to survive a router firmware update, and the identity layer becomes ours —
which is the whole of 0032 again, with more surface.

**IAP, as originally proposed.** Rejected on plumbing, not on principle: the assertion it hands over
is the one this ADR verifies, and everything around it (load balancer, managed certificate,
connector, bill) reaches a machine at home worse than a tunnel does.

**Tailscale, as the technical best.** Rejected on friction, recorded because it becomes the right
answer the day the people involved already have it installed.

**An OAuth app instead of a pasted token — spiked 2026-10-30, real but not first.** Cloudflare
supports third-party OAuth clients on the free plan, created in the dashboard or through
`POST /accounts/{id}/oauth_clients`; they are **private by default**, and a private client may only
be authorized by members of the same account, so none of them needs the domain-ownership check.
Scopes are API token permission names and are listed by `GET /oauth/scopes`. Only the authorization
code flow is supported — no device flow, no client credentials — with PKCE and
`token_endpoint_auth_method: none` for a client that cannot keep a secret, which is what a desktop
tool is. The grant is then revocable from the owner's own authorization list, which is a better story
than a standing API token.

It is not first for one reason: **a reconciler needs a credential that outlives an hour.** Cloudflare's
pages do not say whether an OAuth grant issues a refresh token, and a tunnel that reconciles at boot
and after every change cannot depend on a browser re-authorization. So the paste ships, the OAuth
path is a later addition behind the same client interface, and the one thing to measure before
building it is the token's lifetime and its refresh behaviour.

## Verification

**The fast tier (`make test`)** — everything that does not need Cloudflare:

- The provisioner's plan against a fake client: the exact sequence, the same sequence twice creating
  nothing, a resume after a `5xx` reusing the ids it wrote, and each scope probe's refusal sentence.
- Reconciler: a group derived from `people.json`; a person revoked is removed from it; drift is
  repaired on the next pass; a failed step is reported and the rest is not skipped silently.
- Golden fixtures — sanitized real responses — so a shape change in the API fails a test rather than
  a family member's login.
- `TestTheApiTokenIsNotNeededToKeepTheTunnelUp`, `TestACloudflaredCrashIsReportedAsDegraded`,
  `TestASetupThatDiedHalfwayResumes`.

**The interface (`make ui-check`)**: pasting a token names each missing scope, and the setup's last
step shows the door's answer.

**The live tier (`make test-tunnel`, `ACORNFALL_TUNNEL_TEST=1`)** — a throwaway zone and a token from
the environment; it fails rather than skips when the variable is set, the same rule
`ACORNFALL_IMAGE_TEST` follows:

- Creates the tunnel, application, group, and an **Access service token** with a policy that admits
  it, then asserts from outside the machine:
  `TestThePublicHostnameRefusesAnUnnamedCaller` — no credential is `302` to the team domain **and the
  gateway never saw the request**; `TestAServiceTokenInTheGroupReachesTheGateway` — `200` from
  `/healthz`; `TestTheTunnelComesDownCleanly` — no orphaned application, policy, group, or DNS record.
- Runs on a schedule rather than on every change, like the engine tiers, because it needs the network
  and a real account.

## Not in this ADR

- **More than one hostname**, and per-person hostnames. One door, one hostname, until someone can say
  why two.
- **Other Access identity providers** (Google, GitHub). That is a policy change at the edge and zero
  lines of Go; the one-time PIN is chosen because it needs no account setup for anybody.
- **Email sent by acornfall.** The door proves the mailbox; the invitation is a message the owner
  sends. Sending mail would mean owning a sender reputation.
- **Self-hosted tunnel servers**, and any deployment where the machine has a public address.
