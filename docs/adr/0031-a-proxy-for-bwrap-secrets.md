# ADR 0031 — A proxy for bwrap secrets

Status: accepted, built — the proxy, its delivery through pasta, the sandbox's trust store, and a
live rotation; the resolver and the DNS pin are not built
Date: 2026-10-29

## Context

ADR 0021 gave the vault two deliveries: `substitute` on a microVM engine, where the guest holds a
placeholder and the value never enters it, and `environment`, where the value is in the guest and
`allowEnv` is the only switch. bwrap took the second (ADR 0023), and the code says so plainly
(`Bwrap.SecretDelivery` is `DeliveryEnvironment`, `SecretEnvInWorker` is true). What that means in
practice was not written down, so it was measured:

| Claim | Where it is true |
|---|---|
| The gateway's whole environment is inside the workspace, readable at `/proc/<supervisor>/environ` — `Start` runs `exec.Command` with no `Env`, and no `--clearenv` is passed | `gateway/internal/sandbox/bwrap.go:565`, `:878` |
| A vault value is in the environment of **every** command for as long as the worker lives — `clearEnv` needs `inheritEnv: false`, which no call site passes | `worker/src/bwrap.ts:149`, `worker/src/sandbox-env.ts:111` |
| `allowEnv: false` is all-or-nothing: no "usable by a command, unreadable by the agent" | `gateway/internal/control/secrets.go` |

ADR 0023 line 191 says the sandbox starts with "`--clearenv` and then exactly the environment the
worker hands over". The code did not do the `--clearenv` half, and the supervisor it starts is the
reader of the rest. Both are fixed here, for the reason the table gives.

Four implementations of the same feature were read, and they agree on the shape: a placeholder in
the guest, the value host-side, substitution at the boundary, an allowlist by host.

| | Where the substitution lives | What was taken |
|---|---|---|
| microsandbox 0.7.6 | its own userspace netstack (`smoltcp`, `msb_krun`); `secrets/handler.rs` is 7343 lines of HTTP/1.1 + chunked + HTTP/2 HPACK | not portable; its **five gates** are the model |
| zerobox | Codex's network proxy (`rama-http`, a managed MITM CA) plus a 198-line patch, of which ~6 lines are the feature | the **CA lifecycle** and the seam |
| nono | a standalone proxy crate with a route list, phantom tokens, and a `file://` credential source | the **session token**, the injection vocabulary |
| veil / agent-vault | local proxies for one agent's environment | shape-preserving placeholders, response scrubbing |

And pasta, which the engine already requires, turns out to carry the whole transport: measured with
the driver's own flags, `-T 18443` delivers the namespace's own `127.0.0.1:18443` to a host
listener, `-U 53:5353` does the same for DNS, and with no gateway and no routes at all those two
ports are the only egress there is.

## Decision

**A proxy, per workspace, on loopback, inside the gateway.** `internal/secretsproxy` serves one
workspace's grants: a command reaches it through `HTTPS_PROXY`, presents a session token, and gets
its placeholder replaced for a host the secret names. `github.com/elazarl/goproxy` supplies the
CONNECT, the per-host certificate, and the HTTP/2 handling; the policy is ours.

**Five gates become four, now.** A placeholder becomes a value only when all of these hold:

1. **Host match** — the destination is one the grant names, exact or a leading `*.` (the vault's
   grammar; a bare `*` matches nothing here, so a mistake upstream fails closed).
2. **TLS identity** — the value is substituted only inside intercepted TLS, or on a request that
   arrived as plain HTTP to a host the grant names. There is no "opaque TLS" path that substitutes:
   a secret is never handed to a connection the proxy cannot see into.
3. **Authority alignment** — the request's `Host` is the destination the CONNECT named (`goproxy`
   sets it from the target, and the policy compares that name), so a forged `Host` header does not
   move the value to another destination.
4. **Location scope** — header, query and body are each enabled per secret; a placeholder found in a
   disabled location is a **refusal**, not a pass.

5. **DNS pin.** A granted name is resolved once, an answer inside the machine — loopback, a private
   range, link-local, a cloud metadata address — is refused with `403` before anything is sent, and
   the connection goes to the address that was checked rather than to a second lookup. The first
   sketch of this gate put a resolver in the guest (`-U 53:<port>`); building it showed the premise
   was the microVM's, not a proxy's, and slice 3 below says why.

**Refusal is block-and-log, with a name and a host.** The client gets `403` and one sentence naming
the secret and the destination; the gateway's log gets the same sentence; the value is in neither,
and a test asserts both strings.

**Interception is the exception.** A CONNECT to a host no grant names is **tunnelled**, not
intercepted: a client that pins a certificate keeps working for everything that is not a secret's
destination. The price is honest and tested: a placeholder sent to a host no secret names travels
as text — which is not a leak, because the placeholder is not the value — and the destination
rejects it. A host that does have a grant is intercepted, and there a foreign secret's placeholder
*is* refused.

**The CA is the install's, not the workspace's.** One file, `<dir>/ca.pem`, mode `0600`, holding the
key and the certificate; created once, with `os.Link` so a second gateway starting at the same
moment reads the first one's file instead of replacing it. A file that is over 64 KiB, readable by
other users, or not a CA is **refused**, not regenerated: what is on disk is what the workspaces
trust, and silently replacing it would break every one of them. RSA 2048 for the CA and a leaf
signed per host, cached per proxy.

**The session token is the boundary a loopback port needs.** Every workspace gets 256 bits, minted
per proxy, accepted as `Bearer` or as the password of the proxy URL — the two forms a real client
can send — and compared in constant time. A proxy with no token is not a proxy, it is a
substitution service for anything on the machine.

**The package is a leaf.** `internal/secretsproxy` imports nothing of this project: control owns the
vault, decides which secrets a workspace has, and hands the grants in (the arch row says so). The
proxy never reads `secrets.json`, and the value it holds is one it was given.

**pasta carries it.** The sandbox's `HTTPS_PROXY` points at `127.0.0.1:<port>`, and the driver's
argv gains `-T <port>` next to the `-T none` of today. Nothing of the machine's loopback is exposed
by that: measured, `-T 18443` opens exactly one port and leaves `127.0.0.1:2222` refused. The driver
picks the port when it writes the sandbox's config, and a workspace with `network: off` gets no
proxy at all — the proxy is the host's own egress, and a workspace with no network must not have one.

**The sandbox stops inheriting acornfall's environment.** `Start` gives the sandbox's own processes
three variables — a `PATH`, the workspace's private `HOME`, a locale — and the bwrap argv carries
`--clearenv` before anything sets one. The worker starts each command with the handful a shell needs
(`TERM`, `LANG`, `LC_ALL`, `TZ`), whatever the caller passes, the workspace's own home and path, and
the proxy when there is one. Measured before this: any command could read the gateway's whole
environment at `/proc/<pid>/environ`.

**The value stops being in the environment.** On a workspace with the proxy, delivery is
`substitute`: the guest's environment holds `Grant.Placeholder`, the worker never receives the
value, and `SecretEnvInWorker` becomes false for bwrap. The placeholder is a per-secret field with
`$MSB_<NAME>` as the default, so a microVM workspace and a bwrap workspace tell the agent the same
story.

## Not decided here

- **The routeless sandbox.** `-U 53:<port>` today would make proxy-only egress possible — the sandbox
  has no route and two loopback ports — which retires ADR 0025's route refusals. That is a bigger
  change than this one and gets its own ADR; this one only notes that the door is available.
- **The DNS pin for a client that does not use the proxy.** The gate covers what the proxy forwards.
  A command that ignores `HTTPS_PROXY` and dials a host directly is outside it, and outside the
  substitution too — there is no value in that request to protect.
- **Plain HTTP.** The proxy substitutes on it (a plain request is visible), but whether a workspace
  should be allowed to send a secret over `http://` at all is a policy question ADR 0021 never asked.
- **Response scrubbing, per-route rate limits, and an audit log.** All three exist in the prior art;
  none is needed for the value to stop leaking.
- **Rotation** in a running sandbox: today a change to the vault restarts the worker on an
  environment engine. With a proxy the grants live in the proxy, so a rotation is a grant swap and
  the next request sees it; that lands with the wiring.

## Slices

1. **Built.** `internal/secretsproxy`: the CA, the policy, the proxy, the token, and the tests below.
   With it, the environment: `--clearenv`, a three-variable sandbox process, and a command that
   inherits a shell's handful rather than the gateway's whole environment.
2. **Built.** Delivery: control starts one proxy per workspace before the sandbox, the policy
   carries its URL and its CA, the driver writes `-T <port>` into pasta's argv and binds a trust
   bundle over the image's roots, and bwrap reports `substitute` delivery. The worker sets
   `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` and `NODE_EXTRA_CA_CERTS` for every command.
3. **Built, differently from this ADR's first sketch.** The pin was written as a resolver the guest
   talks to (`-U 53:<port>`, `nameserver 127.0.0.1`). Building it showed the premise was the microVM's:
   a proxied client never resolves the host it asks for — it hands the name to the proxy — so the
   guest's lookups are not where a secret's address comes from. The pin lives in the proxy instead: a
   granted name is resolved once, cached for five minutes, refused if the answer is inside the
   machine, and dialled at the address that was decided on. `-U 53` stays unbuilt, and its reason is
   recorded: it would give the guest's own traffic a resolver we run, a change to every workspace's
   DNS, in exchange for a signal this design does not need.
4. **Built with slice 2**, and proved in the bwrap tier, because it was cheaper than a restart: a rotation is `SetGrants` under the
   running proxy, and bwrap's `SecretsChanged` answers "no restart". The inspector's wording is
   slice 3's.

Two things the build decided that this ADR did not:

- **The port is derived from the workspace**, not picked per start. pasta's forward is baked into
  the sandbox's argv, so a gateway that restarts has to bind the port the sandbox it adopts was
  booted with. A workspace whose derived port is taken has no proxy and says so, rather than a
  sandbox forwarding into nothing.
- **Only an engine that asks for it gets a proxy.** `Manager.SecretProxyInControl` is an optional
  capability on the driver: bwrap implements it, and a microVM engine — which substitutes on its own
  egress path — does not, so control does not open a second listener to hold a value twice.

## Tests

- `TestAPlaceholderBecomesTheValueForAnAllowedHost` — through a real MITM, the upstream sees the
  value and the workspace's proxy token is not forwarded.
- `TestASecretSentToAHostItDoesNotNameIsRefused`, `TestAPlaceholderInADisabledLocationIsRefused` —
  refusal for the wrong host, the wrong location, and the reason names the secret.
- `TestAPlaceholderOnAHostWithNoSecretTravelsAsText` — the honest limit of tunnelling, and the value
  still does not travel.
- `TestAHostWithNoGrantIsTunnelledNotIntercepted` — a client trusting only the origin's certificate
  gets through, which is what "not intercepted" means.
- `TestTheBodyIsRewrittenWithItsLength`, `TestTheQueryIsRewritten` — body and query surfaces.
- `TestTheProxyRefusesAClientWithoutItsToken` (no token, wrong token, wrong scheme, Bearer, Basic,
  on the wire), `TestAuthorisedAcceptsOnlyTheTwoFormsAClientCanSend` — the loopback boundary.
- `TestARefusalNamesTheSecretAndNeverTheValue` — the refusal and the log, both grepped for the value.
- `TestTheCAIsMadeOnceAndKept`, `TestTwoCallersStartingAtOnceGetOneCA`,
  `TestACAFileOtherUsersCanReadIsRefused`, `TestAnUnreadableCAIsRefusedRatherThanReplaced`,
  `TestACAFileTooLargeToBeOneIsRefused`, `TestACertificateThatMayNotSignIsRefused` — the trust anchor.
- `TestARequestThatIsNotAProxyRequestIsRefused`, `TestAnOversizedBodyIsRefusedForASecretHost`,
  `TestTheProxyReportsAFailureAfterItsListenerIsGone` — the edges a proxy meets.
- `TestASecretIsNotSentToAnAddressInsideTheMachine`, `TestTheConnectionGoesToTheAddressThatWasResolved`,
  `TestOneAnswerServesEveryRequestInsideTheWindow`, `TestWhatCountsAsInsideTheMachine` — the pin.
- `TestABwrapWorkspaceSubstitutesASecretAtItsProxy` — the bwrap tier: a real workspace prints the
  literal it holds and asks httpbin for the headers it received, which is the value. This is the
  test that found the gap the fast suite could not: the guest held nothing at all, because control
  had stopped putting values in the worker's environment and nothing put the placeholder in its
  place.

## Alternatives

**Fix the environment delivery instead.** Write the `--clearenv` ADR 0023 already promises and narrow
what a command inherits. Adopted, but as *half* the answer — it removes the ambient leak, and it
leaves the value readable by every command, which is the property the vault exists to remove.

**Port microsandbox's handler.** Rejected: it is a userspace netstack, and 7.3k lines of hand-written
HTTP framing that Go's `net/http` already is.

**Adopt nono as a sidecar.** The closest thing to a drop-in: a published crate, prebuilt Linux
binaries, `file://` credentials, and a rate limiter. Rejected for now, not forever: its primary mode
is a base URL the client talks to, so `curl -H "Authorization: Bearer $MSB_GITHUB"` stops working,
and it would be the second runtime in the gateway's trust boundary. If the proxy grows features that
nono already has, it is the thing to revisit.

**Fork zerobox's patched Codex proxy.** Rejected: a fork of a fork, in Rust, to add a trait and two
call sites we can have in Go for ~350 lines.

**mitmproxy or Envoy.** Rejected in the investigation: no Debian package for either here, a second
runtime, and Envoy's credential injector injects a header from SDS rather than substituting a
placeholder.

**No proxy: keep `environment` and document it.** Rejected because the doc would say "every command
can read every secret", which is the failure the vault was built to end.

## Consequences

- **The gateway has its first third-party Go dependency.** `go.mod` had none; this adds
  `github.com/elazarl/goproxy` v1.9.2, with `golang.org/x/net` and `golang.org/x/text` behind it.
  The library's defaults were checked and overridden: its transport skips TLS verification and reads
  the environment's proxy settings, and both are wrong for a secret's destination.
- **A trust anchor exists on the machine.** `<home>/proxy/ca.pem` signs certificates for every
  workspace; anyone who can read it can impersonate any host to every sandbox. `0600` and a refusal
  on a wider mode are the guard.
- **One listener per workspace**, and therefore one port per workspace in pasta's argv. A workspace
  that is running with a network off has neither.
- **A pinned client is excluded from a secret's host**, by design, and gets a handshake failure there
  rather than a substitution. That is the same trade the microVM engine made.
