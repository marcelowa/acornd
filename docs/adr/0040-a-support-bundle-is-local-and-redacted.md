# ADR 0040 — A support bundle is local and redacted

Status: accepted, built
Date: 2026-11-11

## Context

A workspace can fail in a way its own tools cannot explain. A sandbox that will not start has
no shell and no files, so the agent inside it cannot read the gateway log, the youki record,
or the cgroup tree. The person debugging it is on the host, and asking them to run a list of
commands from memory is how a report loses the one fact that matters.

The data needed is sensitive. The records name paths and a secrets proxy URL; the logs can
contain a token; the vault holds the values themselves. A diagnostic that copies the install
is a data export, not a bug report.

## Decision

**A support bundle is a local, redacted file made by the person who owns the install.**

- `acornd support preview` lists every file and redaction without writing anything.
- `acornd support bundle` writes a `.tar.gz` under a path the person chose. Nothing is
  uploaded.
- The console's Sandbox dialog offers the same action. It is shown to the install admin and
  the room's owner; a guest cannot make one, because the bundle carries the machine's logs and
  process groups, not the room's content.

The bundle contains:

- acornd and machine facts: version, OS, kernel, systemd, `/proc/self/cgroup`, uid map, and
  the cgroup mount lines.
- The workspace record, without members or conversation data.
- Every sandbox record for the workspace, its small configuration files, and the last part of
  its log.
- The container, supervisor, and runtime PIDs; their real cgroup from `/proc/<pid>/cgroup`;
  and the cgroup files and ancestors around both the real and the recorded path.
- The gateway's lifecycle lines; worker lines, prompts, and conversation text are left out.
- A listing of the sandbox directory, including the folders the collector did not read
  (rootfs, upper, private home).

It does not contain secret values, credentials, provider keys, conversation text, workspace
files, or environment values.

Redaction is best effort and happens before anything is written: exact vault values, token
shaped settings, authorization headers, URL credentials, recognised token prefixes, email
addresses, and the home path are replaced. The manifest names what was replaced, by category
and count, never the values. A person is expected to read the preview and the manifest before
sharing the file.

## Alternatives rejected

- **Upload automatically to a collector.** A diagnostic that phones home is a data export
  nobody approved, and it fails exactly when the network is the problem.
- **Copy the workspace.** The files are usually the user's work, not the failure. A listing
  and the small runtime files answer the question without them.
- **Let the agent make the bundle.** The agent is inside the sandbox that is broken and has
  no host path. More importantly, a compromised agent must not be able to read the host's
  logs and cgroups through a support route.
- **Trust the record's cgroup path.** The outages that motivated this started with a record
  that named a cgroup the sandbox process was not in. The bundle includes both paths and the
  process's own answer.

## Consequences

- A bundle is one file a person can attach to a report, and it is safe to read before sharing.
- The collector is deliberately narrow. A question it cannot answer needs a new field in the
  collector, not a new copy of the install.
- Redaction can over-replace (a token-shaped string that is not a secret) or miss a secret the
  vault does not know. The preview and manifest are the guard, and the file is never sent
  automatically.
- The console route and the CLI share one collector, so the two cannot describe different
  facts.

## Where it is checked

- `gateway/internal/support/support_test.go` — the recorded and real cgroup both travel, a
  secret and a proxy token do not, and the tar contains the manifest and README.
- `gateway/internal/control/support_test.go` — the owner and admin may collect; a guest and a
  stranger may not.
- `ui/src/components/workspace/support-bundle-dialog.tsx` — the preview is shown before the
  download, and the button is only offered when the caller may administer.
