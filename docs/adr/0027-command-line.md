# ADR 0027 — One command per question, and flags stay with the command that answers it

Status: accepted, built
Date: 2026-10-26

## The problem

The command line grew one flag at a time, and each one was reasonable when it was added. The
result was not: `acornfall` with no arguments started a server, `start` was not a word you could
type, and the server carried fourteen flags of which half belonged to a developer, the updater,
or a test. `--home` and `--data-dir` were the same folder under two names, with two environment
variables behind them; `doctor` declared both, so the last one typed won, silently. `--network`
and `--private` asked one question as two, with a combination that meant "off but also allowed".
`--allow-env` defaulted to true and could only be turned off by writing `=false`, beside a
`--no-env` that already said it. `workspace` took every flag on every verb, so `remove --image`
parsed and did nothing.

None of that is a missing feature. It is a surface nobody chose, and the cost is paid by every
person who reads `-h` looking for the two flags they need.

## The decision

**A command is a question a person asks, and it is named.** `acornfall` alone prints the list and
exits 2: nothing is a guess, and starting a server is too big a guess.

| command | the question |
|---|---|
| `start` | run the gateway |
| `setup` | can this machine run a workspace, and if not, what does it need |
| `doctor` | what can each sandbox engine do here (changes nothing) |
| `workspace` | which folders are workspaces, and what decides for them |
| `secret` | what values do my tools get, and where may they go |
| `provider` | which model providers does this install have keys for |
| `model` | which model runs when nobody chose |
| `update` | is there a new release, and install it |
| `version` | what is running |

**Flags belong to the verb that acts on them.** `workspace add` decides; `list` and `remove` take
a name and nothing else. Every verb answers `-h` from its own flag set, so the usage text cannot
drift from the flags — the hand-written lines had already forgotten two.

**A flag on the server is an install decision, not a per-boot one.** The engine is recorded on
the workspace at creation; the worker's sources and the last-resort model are environment
variables; the migrate scripts are baked into the binary. What is left on `start` is where it
listens (`--host`, `--port`), where it keeps things (`--data-dir`, `--workspaces-dir`,
`--sandbox-state`), and `--worker`.

**One folder has one name.** The install folder is `--data-dir`, `ACORNFALL_DATA_DIR`, and
`dataDir` in the code. `home` means the user's own home, which the sandbox mounts, and nothing
else.

**A secret's scope is a workspace's name, not its folder.** The registry is the one place that
decides where a workspace lives. `secret show` prints everything about an entry and has no way to
print its value, because a stored value has exactly one direction.

**Two prompts, and each says which way it defaults.** `setup` asks `apply these steps? [y/N]`
because it is about to change the machine; `update` asks `install vX? [Y/n]` because running it
was already the request. `--yes` answers both; `--check` refuses to apply.

**No token.** `--token`, `ACORNFALL_TOKEN`, and the middleware that read them are gone, and the
default listen address stays `0.0.0.0:7777`: open on purpose (ADR 0020), with authentication
named as future work rather than implied by a switch nobody set.

## What it costs

- **Scripts typed the old words.** `acornfall` alone, `--home`, `--token`, `--workspace`,
  `--addr`, `--network`/`--private`, `--sandbox-engine`, `--disable-vm-nesting` (install-wide),
  `--migrate-dir`, `--worker-dir`, `--worker-bin`, `secret rm`, and `secret --workspace <folder>`
  all changed or went away. The release notes name each replacement; this is a pre-1.0 break.
- **Two things moved to the environment**: `ACORNFALL_WORKER` (worker sources) and
  `ACORNFALL_SANDBOX_ENGINE` (a whole-install engine, for a test or a machine that must be told).
  `ACORNFALL_MODEL` stays as the last resort.
- **`--disable-vm-nesting` is per workspace now.** An install-wide refusal was a default for a
  decision each workspace makes, and on the pinned engine a refusal cannot be honored yet
  (ADR 0018).

## What it does not solve

- **`workspace` still edits the registry directly.** The interface creates workspaces inside one
  folder by name; pointing at a folder anywhere else is this command, and the gateway picks the
  change up on its next start.
- **`model list` lists the choice and the keys, not the catalog.** The models a provider offers
  come from the provider through the worker, which runs with the gateway; the interface shows
  them.
