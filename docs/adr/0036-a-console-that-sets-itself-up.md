# ADR 0036 — The console sets itself up, and the run manages itself

Status: accepted (2026-11-01)
Date: 2026-11-01

## Context

[ADR 0033](0033-remote-access.md) built the door and everything behind it: the provisioner makes a
tunnel, an ingress, a `CNAME`, a one-time-PIN provider, an application, a group and a policy out of
one API token, and the group follows the people file. It works — the demo runs a whole install
through it. What it does not do is *start*: the pieces are joined by hand, and every join is a
terminal.

The manual journey today, on a clean machine:

```
curl … install.sh | sh            # binary, checksum, doctor — nothing runs, nothing starts
acornd setup                      # the machine can sandbox now
acornd start                      # a terminal that must stay open
  … providers and models in the console …
acornd people claim you@…         # the first admin, from the command line
acornd remote apply --hostname …  # Cloudflare, from the command line
  … export ACORND_IDENTITY=access + audience + issuer + keys, --host 127.0.0.1, restart …
  … only now does the browser through Cloudflare work …
```

Two of those lines are a deadlock rather than an inconvenience. `/api/remote/apply` refuses an
install with no book of people (`this install has no book of people yet`), and the book only opens
when the door is on, and the door only turns on from four environment variables, and two of those
values are created by the apply. The console can therefore not configure its own Cloudflare on a
fresh machine, which is the one thing a person who just installed it wants.

The env vars are the same problem seen from another side. A boot reads its identity from the
process environment, so a change made in the console cannot take effect without an edit to a
service file and a restart nobody is told about. A restart keeps its argv and not its environment,
which is why the update path — which re-execs — cannot change the door either.

And there is no answer to "who owns this install" before Cloudflare exists. ADR 0020 left the
console open; ADR 0032 put a door in front of it once there are people. Between the two lies the
window this repository has been living in: anything that can reach port 7777 can claim the install.

## Decision

### 1. One install command, and it ends with a running console

`scripts/install.sh` stops calling `doctor` and starts doing what the next three lines of the README
tell a person to do: it runs `acornd setup --yes` when the machine can be prepared without a
password, installs the binary, then `acornd start -d`, then prints the URL with its setup code. A
machine that cannot be prepared non-interactively gets the same plan printed as commands, exactly as
`setup` does today. The manual path stays documented and works; it is no longer the only one.

No unit, no cron, no linger. A person who wants the console after a reboot runs `acornd start` after
the reboot; that is a deliberate absence and §2 says why.

### 2. `start`, `stop`, `restart`, `status` — a run, not a service

| command | what it does |
|---|---|
| `acornd start` | runs in the foreground, as it does today; the log is the terminal |
| `acornd start -d` | detaches: `setsid`, stdout and stderr to `<data-dir>/acornd.log`, stdin from `/dev/null`; prints the URL and exits |
| `acornd stop` | `SIGTERM` to the recorded pid, waits for it to exit, removes the record |
| `acornd restart` | stops what is running and starts it again with the arguments it was started with; `-d` keeps it detached |
| `acornd status` | up or down, pid, since, address, health, and when it is down the last line the log got |

The record is `<data-dir>/run.json` (pid, argv, address, started), written before the listener opens
and removed when the process leaves it. `stop` and `status` verify the pid still belongs to acornd by reading
its command line, because a recycled pid is the one way this can kill a stranger. A record whose
process is gone is reported as such rather than treated as running.

Nothing restarts the gateway when it dies. A crash is visible in the log, in `status`, and as a
console that stops answering; a supervisor that silently resurrected it would hide exactly the
events this install is small enough to notice. Starting at boot is likewise out of scope: it needs
init, and init is the thing this decision is declining.

### 3. A setup code is what an owner needs before a door exists

Until an admin exists, `start` prints a URL carrying a code: `http://<host>:7777/setup?code=…`. The
code is generated on first boot, stored `<data-dir>/setup-code` at 0600, and printed on every start
until it is claimed.

- `/setup` asks for a name and an email, claims that person as the admin in the people file, and
  stops accepting the code.
- `acornd setup-code` prints it, `--rotate` replaces it, `--disable` removes it.
- The install token (`acornd token`) is accepted on the same login form, so a rotated or disabled
  code is never a lockout.
- Any other request from a caller with no session is a login page, not the console. This is what
  closes the open window: an install with an owner is closed from the first minute, not from the
  moment Cloudflare answers.

### 4. The owner session is a cookie, signed with the install token

Claiming mints a signed cookie (30 days, `HttpOnly`, `SameSite=Lax`, `Secure` when the request
arrived over TLS): the payload is the email and an expiry, the key is derived from the install
token, and every request checks it before anything else. One cookie, no session table, no new
credential to lose — the install token is already the credential that proves ownership, and this is
the browser's copy of it with a name attached.

The cookie is host-bound, which is the property that makes the break-glass path work: over an SSH
forward to `127.0.0.1:7777` the owner logs in once with the install token and is in, whatever
Cloudflare and the tunnel are doing.

The door therefore has three verifiers and one order: owner cookie, install token, Cloudflare
assertion. That is `identity.Any`, which already exists ([ADR 0032](0032-who-may-call-the-console.md) §3).

### 5. The door is a file, not an environment

`remote.json` grows `team_domain` and `session_duration`, and boot reads them:

| value | from where |
|---|---|
| hostname, audience, team domain | `remote.json`, written by the apply |
| issuer and keys | the team domain: `<domain>` and `<domain>/cdn-cgi/access/certs` |
| whether the door is on | the file exists and is configured; `remote.json` is the switch |

`ACORND_IDENTITY`, `ACORND_IDENTITY_AUDIENCE`, `_ISSUER` and `_KEYS` stay as an override for tests
and for a machine behind somebody else's proxy. A normal install never sets them, and the demo
drops all four.

The listen address follows the same rule: the default stays `0.0.0.0` for an install with no door,
because a headless machine has to be reachable on the LAN to be configured at all, and becomes
`127.0.0.1` the moment `remote.json` is configured. An explicit non-loopback `--host` on a door
install is refused in the words of [ADR 0032](0032-who-may-call-the-console.md) §8, which this
amends in one respect only: the *default* now follows the door instead of being a trap.

### 6. The Cloudflare screen asks as little as it can, and turns the door on itself

The screen asks for three things, and only the first is not a choice:

| asked | why not derived |
|---|---|
| the API token | the one credential only the owner can mint; the screen shows the five scopes and a link |
| the zone | a choice, from the zones the token can see |
| the hostname | a choice, typed as a label (`console`) or a full name (`console.acornd.app`); a label is completed with the zone, a name in another zone is refused, and the `CNAME` is created when absent |

Everything else is fetched and stored: the account id from the zone, the team domain from
`GET /accounts/{id}/access/organizations` (today it is asked for and never written down), the
tunnel, ingress, application, group, policy and provider ids from the apply, and the audience from
the application.

Apply ends with one button that saves the identity values to `remote.json` and restarts the process
— the update path already re-execs, so this is the same mechanic with a different reason. The
session duration is set to one month, which is Access's maximum: Cloudflare caps both application
and global sessions there, so a year is not a value the platform accepts and the family re-enters an
emailed code monthly.

### 7. An invitation is an entry in the people file

Adding somebody is the invitation: the write re-reconciles the Access group, so their address opens
the door the moment they try. Nothing sends mail — Cloudflare sends the code when they present the
address, and the owner sends the URL. The screen copies the link; there is no "send invite" that
could fail quietly.

### 8. What this decision does not do

- **No start at boot.** init does that, and this install declines init.
- **No restart on crash.** §2.
- **No mail of our own.** Sending the code would mean SMTP, and then an identity provider to keep
  correct; ADR 0033 §1 chose against operating one.
- **No long sessions.** The one-month ceiling is Cloudflare's; §6.

## What it costs

- **A hidden credential more.** The setup code and the owner cookie join the install token in the
  folder. The code is printed by a process whose stdout is the terminal, and the cookie is signed by
  a key derived from a token that is already 0600.
- **An install that is closed before it has a door.** A person who needs the console on a LAN and
  lost the code uses the install token; a person with neither edits the folder, which they already
  could.
- **The command list grows by four.** `start -d`, `stop`, `restart`, `status`, `setup-code`.
  [ADR 0027](0027-command-line.md)'s rule holds: each is a question, and none carries another's
  flags.
- **`ACORND_IDENTITY*` demoted.** Scripts that set it still work — the environment wins — but the
  documented path is now the file, and the demo stops using it.
- **A month of session, not a year.** The alternative is our own login and our own mail, which is a
  different product decision; this ADR does not take it.
