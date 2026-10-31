# ADR 0021 — A vault for tool secrets, global and per workspace

Status: accepted, built — microsandbox substitution, host environment delivery, and the conversation card; the real container driver is its own ticket
Date: 2026-10-25

## Context

Issue #95 is the brief, including its open questions: scope, carriers, what the agent is
told, at rest, and rotation. This ADR answers them. A password or token a command needs has
nowhere to live: a message puts it in the transcript, the model's context, and every fork; a
file in the workspace puts it where the agent and the tools can read it. Provider
credentials have a home (ADR 0003) and stay separate: they bind a model, not a command.

Two things changed the shape of the answer. The console now listens on every interface
without a token by default (ADR 0020), so the vault is reachable by whoever can reach the
console. And microsandbox will not be the only engine: the direction includes containers,
where the host-side substitution microsandbox offers does not exist. The vault must not be
built on one engine's secret surface.

## Measured: what the pinned engine gives (microsandbox 0.7.6)

The engine can keep a value on the host and put only a placeholder in the guest. Measured
on 2026-10-25:

- `msb create --secret ENV[:no-headers,query,body,passthrough=HOST]@HOST[,HOST...]` reads
  the value from the host environment at start and stores only a source reference. The
  inline `ENV=VALUE@HOST` form is refused: the value would be in the config at rest.
- The same entry in `sandbox.yaml`:

  ```yaml
  secrets:
    SPIKE_TOKEN:
      value: { $msb_env: SPIKE_TOKEN }
      allow: [api.example.com, "*.example.org"]
      substitution: { headers: true, query: false, body: false }
      require_tls_identity: true
  ```

- The guest sees `SPIKE_TOKEN=$MSB_SPIKE_TOKEN`, the default placeholder. Headers are
  substituted by default; query and body are opt-in. TLS identity is required and TLS
  interception is enabled automatically for a non-empty secret set. The default violation
  action is `block-and-log`, configurable to `block` or `block-and-terminate`.
- `msb inspect --format json` shows
  `source: {kind: env, var: SPIKE_TOKEN}`, `value: ""`, the placeholder, and the allowed
  hosts. The value is nowhere in the engine's store on disk.
- `msb modify --secret NAME@HOST` rotates live (the plan says `live`), with the value in
  the modify process's environment.
- Built and measured end to end: a live workspace sends the placeholder in an
  `Authorization` header to `httpbin.org` and receives the real value; the guest never held
  it. The test is `TestSecretsReachTheSandboxWithoutTheirValues`.

The limit is as important as the capability: this engine substitutes only into HTTPS
requests, because it intercepts TLS on port 443. A plain HTTP request is not seen at all,
and a client the proxy cannot see — `psql`, `git` over SSH, a tool that insists on reading a
file — cannot use a secret here. There is no raw-env mode to fall back to.

## Decision

**One vault, two scopes, workspace wins.** A secret is `global` (available to every
workspace) or belongs to one workspace. A workspace's effective set is every global secret
plus its own, and when both define a name the workspace's entry is the one delivered. Names
are unique within a scope, not across scopes: two workspaces may both define `DB_PASSWORD`
with different values, and a workspace `GITHUB_TOKEN` replaces the global one for that
workspace. The name is the environment variable the guest sees, so it is
`[A-Z][A-Z0-9_]*` and a small reserved list (`PATH`, `HOME`, `SHELL`, `TERM`, `USER`,
`HOSTNAME`, `PWD`, `MSB_*`, `ACORND_*`) is refused.

**Stored beside the provider credentials.** `<home>/secrets.json`, mode 0600, a JSON
object with a version and an array of entries: name, description, scope, workspace path for
a workspace secret, value, hosts, substitution locations, `allowEnv`, `rotatedAt`. Atomic
write; a lock file serializes read-modify-write. The file is never mounted into a sandbox
(`<home>` already is not), and self-update backups drop it the way they drop
`credentials.json`, including from old backups. Encryption at rest is a follow-up, not a
rejection: the version lets a key source wrap the values later without changing the API or
the interface. Until then, 0600 is the bar credentials already meet.

**The value is write-only.** No API response, log line, error, state file, or transcript
contains it. The interface shows the name, description, scope, hosts, substitution,
`allowEnv`, and when it was rotated. The value is entered once and never returned.

**The guest holds a placeholder, and the prompt says how to send it.** On a substitute engine
the environment variable is the literal `$MSB_<NAME>`; a command sends that value, for
example in an `Authorization` header. `${NAME}` and `{{NAME}}` are not placeholders and pass
through unchanged, and substitution is HTTPS only: the engine intercepts TLS on 443, so a
plain HTTP request is never substituted. The prompt and the list tool name the placeholder
and the HTTPS limit, because a success that never reaches a command is the worst failure
mode this feature has.

**A no-host substitute secret is refused at the card.** On microsandbox a secret with no
allowed host has nowhere to be substituted, so the card will not store one and the settings
page marks it required. The store itself stays engine-neutral: an environment engine does not
need a host, so the vault can still hold an entry for one.

**Delivery is an engine capability, not a vault property.** The driver reports one of:

| Delivery | Engine | Guest gets | Value reaches the guest |
|---|---|---|---|
| `substitute` | microsandbox 0.7.6 | `$MSB_<NAME>` placeholder | no; substituted for allowed hosts over HTTPS |
| `environment` | containers (future) | the real value in the environment | yes, by default; `allowEnv: false` keeps it out |

`allowEnv` defaults true: a container engine supports secrets without asking. On a
`substitute` engine it is irrelevant; on an `environment` engine, false keeps the value out
of the sandbox and the secret is simply absent there, which the state and the inspector say.
`hosts` are required for a `substitute` engine and ignored by an `environment` engine. The
wildcard form is a leading `*.`; `*` alone is refused.

On an `environment` engine the agent can read and print the value; the default accepts
that. The interface warns when a global secret will be readable in every container
workspace, and `allowEnv: false` is the opt-out. On a `substitute` engine a printed variable
is the placeholder.

**The vault is engine-neutral; only the driver knows the engine.** Control builds the
effective set and passes named grants to the sandbox policy. The microsandbox driver
renders `value: { $msb_env: ACORND_SECRET_<n> }` in `sandbox.yaml` and sets those
variables only in the `msb create` process environment. The worker never sees a value: it
attaches to the guest, where the env var holds the placeholder. A container driver will
set the variables in `docker run`/`podman run` and say `environment`; the vault, policy,
API, and interface do not change.

**Rotation is live where the engine can do it.** Updating the file, then:
microsandbox `msb modify --secret NAME@HOST` (or `--secret-rm`) with the value in the
modify process environment; containers recreate the sandbox, which the interface states.
A rotated value never touches a sandbox's files or its transcript.

**A change that needs a start is applied after the run, and the control starts the next
turn itself.** Adding a name or changing a host stores the next-start config; the card tool
tells the agent to stop, and when the run that asked settles the control restarts the sandbox
(substitute) or the worker (environment). Once no run is active, it starts a new turn with an
automatic continuation message naming the secret. The user does not resume. This is the
missing trigger the first beta exposed: "next start" with no way to ask for one left the
secret stored but never delivered.

The restart waits for every conversation in the workspace to be idle, and a prompt admitted
while it runs waits on the same lifecycle lock. The pinned engine has no atomic quiesce, so a
run started by a delegated agent inside the check window is a small residual race; the
workspace's own turns and tools are not interrupted. Background processes the agent left
running in the sandbox end at the restart; the files and installed packages persist. A
container driver has the same trade-off, which is why this is the default rather than a
mid-turn VM restart.

Per-use audit is not in v1. The vault records when a value was rotated, and the sandbox
state lists the names it holds. Which secret a request used is only visible where the
substitution happens, and that layer is the engine's, not a portable one.

**The prompt carries names, not values.** The workspace's system prompt lists `NAME`, its
description, and its allowed hosts (or "value in the sandbox" for an environment engine),
and says a placeholder is not the secret. On a substitute engine that is all the agent ever
has; on an environment engine the value is in the guest and the agent can read it, which
`allowEnv` accepted. The inspector has a Secrets card with the same list. The worker
protocol gains one optional `secrets` field; it carries names, descriptions, and hosts only.
The prompt is the starting snapshot; the `list` tool asks control for the current set, so a
secret added mid-conversation shows up without a restart.

**Management is part of the console.** Settings > Secrets manages global entries; a
workspace's settings manages its own and shows the global ones it inherits, marking the
ones its own value overrides. Add, rotate, delete, and a CLI (`acornd secret
add|rotate|rm`, value from stdin, never argv) for a headless install. The console is open by
default (ADR 0020): anyone who can reach it can add a secret or ask the agent to use a
granted one. They cannot read a value. On a container engine a global secret is readable
inside every workspace; the interface warns about that, and `allowEnv: false` opts out. The
value crosses the network when it is saved: WireGuard covers the tailnet, and an untrusted
LAN needs an SSH tunnel or an HTTPS proxy in front. A token authenticates the console; it
does not encrypt the value.

## In the conversation

Two surfaces, one rule: the value never enters a message, the composer, the model, or the
transcript.

- **Show what is available when asked.** A `secret` tool with `list` and `request`. `list`
  answers names, descriptions, scope, hosts, and how each reaches the sandbox. It never
  answers a value. The model calls it when the user asks what secrets exist; the preamble
  says the tool exists and what it does.
- **Ask the user to set one.** `request(name, description, hosts?)` holds the tool call
  while the conversation shows a secret card: the name, the description, a password field,
  and a scope choice — this workspace by default, global if the user wants it everywhere,
  with the warning that global reaches every workspace. The card posts straight to the
  gateway; control writes the vault and resolves the call with `set` or `cancelled`. An
  existing name offers to replace the value, which is a rotation. The transcript keeps the
  request and the outcome, never the value. The card says whether the running sandbox
  picked the secret up or needs a restart.
- **Set one without the agent.** The conversation menu has a Secrets entry with the same
  list and add/rotate through the same card.
- **A workspace value beats a global one.** The list and the card say when this workspace
  will use its own value instead of the global one with the same name.
- **Prompt the user, never the chat.** The preamble says: never ask for a secret as text;
  use the request tool. A value typed into the composer is a value in the transcript, and
  nothing can take it back.

The request is a gateway-owned pending call: the worker's tool blocks on it, the client
shows the card, and the gateway is the only process that writes the value. The worker never
receives it. The card posts to the same origin, and the value is not kept in the client
after the answer.

## Slices

1. Store, API, CLI, and scope resolution (global + workspace override), with descriptions,
   values write-only, and the file excluded from backups. No sandbox delivery yet, so
   nothing can use it.
2. Microsandbox delivery: policy rendering with source references, create/modify process
   environments, the settings list/add/rotate, and the preamble and inspector names. This
   is the first slice that works end to end.
3. The conversation: the `secret` tool (`list`, `request`), the card, the pending call, and
   live add/rotation. This is where the user's ask lands: ask, set, and list without a
   value ever entering the transcript.
4. The `environment` delivery contract (`allowEnv` default true) with a fake driver and the
   refusal path. The real container driver is its own ticket.

## Tests

- Store: 0600, atomic rewrite, name validation, a workspace value overrides a global one,
  names may repeat across scopes but not within one, missing descriptions refused, no
  response carries a value.
- Policy: `sandbox.yaml` contains the source reference and no value; the create process
  environment carries it; the fake driver sees `substitute` or `environment`.
- Conversation: `list` answers names and descriptions and no value; `request` writes the
  chosen scope and resolves with `set`; cancel leaves the vault untouched; the transcript
  and every SSE frame contain no value; a workspace override is the delivered value.
- End to end: `bash` prints `$NAME` and the transcript holds `$MSB_NAME`, not the value;
  the API's secrets response has no value; the system prompt lists the name and placeholder.
- The card path: a request answered during a run stores the value, the tool tells the agent
  to stop, the control restarts the sandbox after the turn settles, starts the continuation
  itself, and the next command sees the placeholder. An HTTPS request to an allowed host
  carries the real value; plain HTTP does not substitute. A substitute secret with no
  allowed host is refused at the card.
- Live sandbox tier: a request with the placeholder in an allowed host's header reaches
  the host with the real value; the same placeholder to a disallowed host is blocked.
- Rotation: a live microsandbox sees the new value without a recreate; a stopped one gets
  it at the next start; the old value is absent everywhere after.
- Container fake: the default delivers the value as an environment variable; `allowEnv:
  false` leaves it absent and says so.

## Alternatives

**A workspace `.env` file.** Rejected: it is in the transcript and the model's context the
first time the agent reads it, it is copied into every fork, and every tool can read it.

**Provider credentials as the vault.** Rejected: a provider credential binds a model and
has an OAuth/refresh lifecycle; a tool secret is named by a command and has allowed hosts.
ADR 0003's store stays where it is.

**Microsandbox's secret surface only.** Rejected by the container direction: the vault
would become an engine feature and have to be rebuilt when the engine changes.

**Global secrets opt-in per workspace.** Rejected: global means every workspace. A
workspace that needs a different value overrides it with a workspace secret; a per-workspace
refusal list can be added later without changing the store.

**Encryption at rest now.** Deferred, not rejected: no key source exists yet. The versioned
file and a 0600 default let a key source wrap the values later without a schema break.

## Follow-ups

- Encryption at rest: choose the key source (OS keyring, passphrase, or a file the owner
  supplies) and wrap the `value` fields. The API and interface do not change.
- The real container driver: its own ticket. This ADR fixes only its delivery contract
  (`environment`, `allowEnv` default true); the host engine already exercises that contract
  in `TestHostEngineDeliversSecretsIntoTheWorkerEnvironment`, including the
  card-without-a-restart path.
