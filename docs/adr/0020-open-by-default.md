# ADR 0020 — The console listens on every interface and needs no token by default

Status: accepted, built
Date: 2026-10-25

## Context

The gateway bound `127.0.0.1:7777` and generated a token for any other bind. Reaching the
console from a phone or another desktop took two steps that had nothing to do with the work:
change the address, then copy a secret out of the startup log or `<data-dir>/token` into the
browser. A service manager had to keep that file, and a first open that lost it meant reading
the log again. `make dev` already binds the tailnet with no token, so the product already
treats a trusted network as the boundary.

The console is a single owner's tool for their own machine, and the sandbox already contains
what the agent runs. The token guarded a door on a network the owner chose. The cost was on
every first open.

## Decision

The default listen address is `0.0.0.0:7777`. No token is generated, stored, or required.
`--insecure` is removed, because it was the old way to ask for what is now the default.

The token itself is gone, not merely switched off: `--token`, `ACORNFALL_TOKEN`, and the
middleware behind them were removed rather than left as an option nobody has to take. A
guard that exists is a guard someone believes in, and the honest state of authentication here
is "absent" until it is designed. Security for an exposed port is future work on purpose, not
an oversight of this decision.

## Consequences

- Anyone who can reach port 7777 gets the console, its workspaces, and the provider key it
  spends. The port belongs on a network the owner trusts, behind a firewall, or behind a
  token. This is written in the README, not left to be discovered.
- An install that passed `--token` is no longer guarded after the upgrade, and the flag no
  longer exists to object. The release notes say so.
- `--addr 127.0.0.1:7777` is the one flag for the old exposure; an SSH tunnel still works.
- The sandbox boundary is unchanged. A guest can reach the gateway's API, as it could
  before, and the ADR 0012 limit now says the API is open by default.

## Alternatives

**Keep generating a token and print it.** Rejected: every first open depends on a file or a
log line, and the owner asked for none.

**Token only on non-loopback binds, no generation.** Rejected: the default address would
still be loopback, so another device still needs a flag; and a token only on a LAN bind is a
rule nobody can state in one sentence.

**An auth mode flag (`--auth none|token`).** Rejected: two flags for one answer. An empty
`--token` already means no token, and a set one means a guard.
