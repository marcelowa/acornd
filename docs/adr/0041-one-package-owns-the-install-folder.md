# ADR 0041 — One package owns the install folder's files

Status: accepted, built
Date: 2026-11-23

## Context

The install folder holds the files every layer reads: `workspaces.json`, `settings.json`,
`secrets.json`, `people.json`, `remote.json`, `update.json`, `config.json`, and each workspace's
`state/<name>/` with its `session.sqlite`, `settings.json` and `drafts.json`.

Each package spelled the names it needed. `settings.json` was joined in seven places. Three
packages (the vault, the book of people, the registry) had their own copy of a lock with a
bounded wait and their own atomic write, and the copies had drifted. The install's
`settings.json` had three writers and no lock, so one writer could erase another's keys (#72).

## Decision

1. **`internal/layout` owns the folder's layout.** It answers every path, takes the lock
   (`Lock`, `LockShared`, a file beside the data file, refused after `LockBudget`), and writes a
   file atomically (`WriteFile`). It imports nothing from this module.
2. **No other package spells a file name or takes a flock.** Two tests in `internal/layout`
   check this over every non-test Go file. The exceptions are named in the tests, each with a
   reason: the OCI spec's `config.json`, help text, and the sandbox's image pull lock, which
   waits as long as a pull takes.
3. **What a file holds stays with its owner.** Control still owns the registry's and the
   settings' shape, the vault its secrets, people the book, remote its setup. Layout knows
   where a file is and how it is written, not what is in it.
4. **A file with more than one writer is changed under the lock.** The writer reads the file
   after it takes the lock, changes it, and writes before it lets go.

## Consequences

- The import boundary moved: `layout` is a new leaf, and `cmd/acornd`, `control`, `httpapi`,
  `people`, `remote`, `sandbox`, `support`, `update` and `vault` import it. Each row in
  `gateway/internal/arch/arch.go` says why.
- A new file in the install folder is a new function in `layout`, not a literal in its owner.
- `internal/update/migrate` stays where it is. It holds shell scripts, not Go, so it spells no
  name this rule covers.
