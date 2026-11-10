# Acornd

Acornd is a personal assistant that runs on your own machine. You talk to a model in a web
console. The model can run commands, read and write files, and use skills. It does that inside a
sandbox, not directly on your machine.

One Go binary serves the console, starts a worker for each workspace, and keeps the workers running.
The worker is a small TypeScript process, run by bun. Each workspace has its own worker, its own
storage, and its own sandbox.

- **Local.** No account and no cloud service. Keys, history, and settings stay in `~/.acornd`.
- **Sandboxed.** Commands and file access run in a small virtual machine or a bubblewrap sandbox.
- **Many workspaces.** A workspace is a folder with its own agents, skills, commands, rules, and
  history.
- **People and rooms.** Invite people, give each a personal workspace, and share rooms with guests.
- **Remote access.** An optional Cloudflare tunnel with an email one-time PIN.
- **Linux and macOS**, on amd64 and arm64.

## Install from a release

Releases are one binary per platform, at
[github.com/marcelowa/acornd/releases](https://github.com/marcelowa/acornd/releases). You do not
need Go, bun, or build tools.

### One line

```bash
curl -fsSL https://raw.githubusercontent.com/marcelowa/acornd/main/scripts/install.sh | sh
```

The script:

- picks the binary for this machine: linux or darwin, amd64 or arm64
- checks its checksum
- puts it in `~/.acornd/bin` and on `PATH`
- runs `acornd setup --yes`, which does the steps this machine allows without a password
- starts the console in the background and prints the address to open

The address looks like `http://<host>:7777/setup?code=…`. Open it and enter a name and an email.
The code proves you started the machine. From then on the install is yours. Until somebody claims
it, the console refuses everything except the setup form. `acornd setup-code` prints the code
again until the install is claimed.

Flags: `--dir DIR`, `--version TAG`, `--no-start`, `--no-setup`.

The script adds a link in `/usr/local/bin` or `~/.local/bin` when one is writable, and writes a
marked block in your shell's rc file either way. A shell that was already open keeps its old
`PATH`, so open a new terminal.

### If the machine is remote

If the printed address does not open from your machine, forward the port over SSH and use the
local address:

```bash
ssh -L 7777:127.0.0.1:7777 user@machine
# then open http://localhost:7777/setup?code=…
```

The same tunnel is the way in when the console was started with `acornd start --host 127.0.0.1`.
Use it for the first claim. After that, **Settings > Remote** puts the console behind Cloudflare,
or keep using the tunnel.

### By hand

```bash
mkdir -p ~/.acornd/bin && cd ~/.acornd/bin
VERSION=v0.33.10          # the newest tag on the releases page
curl -LO https://github.com/marcelowa/acornd/releases/download/$VERSION/acornd-linux-amd64
curl -LO https://github.com/marcelowa/acornd/releases/download/$VERSION/sha256sums.txt
sha256sum -c sha256sums.txt --ignore-missing   # must say OK
mv acornd-linux-amd64 acornd && chmod +x acornd
./acornd setup
./acornd start
```

Swap `linux-amd64` for `linux-arm64`, `darwin-amd64`, or `darwin-arm64`.

The release also carries `worker.tar.gz`. You do not need it by hand: the gateway downloads it on
first start.

### What the machine needs

- Linux or macOS, on amd64 or arm64.
- A provider key, or the dev provider from a source tree.

`acornd setup` prepares the sandbox engine. On Linux it installs bubblewrap and pasta; on macOS it
checks the system hypervisor. It asks for your password only for the steps that need root. The
install script runs it for you.

`acornd doctor` reports what the engines can do here and changes nothing.

On Ubuntu 26.04 and later, the `passt` package ships an AppArmor profile that lets pasta start only a
program in `/usr/bin` or `/bin`. acornd's supervisor is in neither, so every sandbox with a network
refuses with `Failed to start command or shell: Permission denied`. `acornd setup` turns that profile
off the way Ubuntu's own `aa-disable` does — a symlink in `/etc/apparmor.d/disable/`, then
`apparmor_parser -R` — and says why when it does: it is a distribution security profile, and no
sandbox with a network can start while it is in force. Machines that ship no such profile, Ubuntu
24.04 and every other distribution, need no step.

### First run

The console shows a **Getting started** card until these steps are answered:

1. Claim the install with the code the start command printed.
2. **Settings > Providers**: paste a provider key, then tick the models this install may use.
3. **Settings > General > Defaults**: choose the model a new conversation starts with.
4. Optional: **Settings > Remote** to put the console behind Cloudflare.
5. Optional: **Settings > People** to invite others.

A new install allows no model until the admin ticks it. The list of allowed models binds
everyone, the admin included.

### Update

Use **Settings > Updates**, or:

```bash
acornd update           # check, show the notes, ask to install
acornd update --check   # report only
```

An update backs up the install first. The new binary stays only if `/healthz` passes for 30
seconds. On failure it restores the previous binary and names the cause on screen. With no gateway
running, it installs to disk and takes effect at the next start.

## What it can do

### Conversations

- **Streaming transcript.** Replies, tool calls, and tool output appear as they happen. Each tool
  card shows the command, its output, and how it ended.
- **Steering.** Type while a turn runs. The message queues visibly and joins the run after the
  current tool round. **Stop** ends the run.
- **Branches.** Fork a conversation before a user turn and edit the message that started the
  branch.
- **Hide and restore.** Delete hides a conversation; it does not remove it. The transcript stays and
  can come back from the rail.
- **Drafts.** A half-typed message stays in its conversation, across reloads, workspaces, and
  machines.
- **Attachments.** Send files and images with a message: 10 MB per file, 20 files and 100 MB per
  message.
- **Rich replies.** Markdown with code, tables, callouts, checklists, steps, bar charts,
  sparklines, matrices, trees, and Mermaid diagrams. A toggle shows the raw text.
- **A URL per conversation** (`/c/1276`), so tabs, reloads, and links work.
- **Usage.** Token and cost totals per provider, model, and tool, and spend per conversation and
  per workspace.
- **The inspector.** The prompt sections and tool definitions the last request sent, estimated
  tokens per section, and what changed since the previous request.

### Workspaces

- A workspace is a folder plus its own worker, storage, and history.
- It reads `AGENTS.md`, agents, skills, commands, and guards from `.agents/` or `.acornd/`. The
  second wins when both exist. `.agents/` is the ecosystem's name, so a bundle written for another
  agent tool works here.
- Create one by name inside `~/acornd/workspaces` from the console. Point at any folder with
  `acornd workspace add <name> <folder>`.
- Forget a workspace without deleting its folder or history. `acornd workspace purge` is the only
  thing that deletes.
- Defaults chain: the conversation, then its agent definition, then the workspace, then the
  install, then a last resort. The inspector says which layer won.
- An idle worker stops after 15 minutes. A busy or watched one keeps running.

### Sandboxes

A workspace runs its tools on one engine. The engine is chosen when the workspace is created and
kept with its record. A new workspace gets the first engine this machine can run: bwrap, then
microsandbox. youki is a third engine a workspace can ask for with `--sandbox youki`; it stays out
of the default order until its tier has passed on a machine without systemd (issue #47 R12). A
machine that can run neither has no default: the console still works, every
workspace refuses its tools, and `acornd doctor` says what each engine is missing. The unisolated
`host` engine is reached only by asking for it.

| Engine | What it is | Needs |
|---|---|---|
| `microsandbox` | One microVM (a small virtual machine) per workspace. The strongest boundary. | A hypervisor: `/dev/kvm` on Linux, the system hypervisor on macOS. It fetches a 72 MB engine on first use. |
| `bwrap` | The workspace's own image in one long-lived bubblewrap sandbox, over a shared image base. Beta. | Linux, a user namespace, and pasta for its network: `apt install bubblewrap passt`. No KVM. On Ubuntu 26.04 and later, pasta's AppArmor profile has to be off, which `acornd setup` does. |
| `youki` | The workspace's own image in an OCI container, over a shared image base, with a policy file, a cgroup budget and secrets through the gateway's proxy. Selectable per workspace; not a default yet. | Linux, a user namespace, a delegated cgroup v2 for its budget (on systemd the user manager's `app.slice` works; no root needed), and pasta for its network. It fetches a 7.8 MB runtime on first use, and the chooser offers it before that. |

- The workspace folder is mounted read-write at its own path. The global resource folders
  (`~/.agents`, the install's agents, skills, prompts, commands) are read-only at their own paths.
  Nothing else is mounted.
- The worker stays outside the sandbox, next to the provider key.
- A workspace that names an engine this machine cannot run refuses its tools. It does not run
  them unconfined.
- Network is on by default: a workspace reaches the public internet. This machine's own networks
  (LAN, link-local, tailnet) are refused unless the workspace has a grant: `--egress private`, or
  the dialog's **private networks** toggle.
- A bwrap workspace gets a root of its own over the shared image base, so `apt install` works
  inside it and the machine's own tools are never touched. The image is unpacked once per machine;
  a workspace pays only for what it changes. A machine that cannot overlay gets a copy instead.
  A command costs about 1.1 ms. The overlay works rootless when the kernel allows user namespaces.
- A youki workspace has a policy file: capabilities, devices, rlimits and read-only/masked paths
  are stated in its OCI config, `memory`/`cpus` are a cgroup budget, and a secret reaches an API
  through the proxy without the guest holding the value. Its root is an overlay over the shared
  image base too, mounted in a private namespace before the runtime creates its devices, and it
  works rootless when the kernel allows a user namespace.
- A bwrap workspace can run another acornd with its own sandbox and network. It needs `/dev/kvm`
  and `/dev/net/tun` from the machine, and a workspace can withdraw those with `--no-devices`.
- A workspace can run a hypervisor inside its sandbox when the machine allows it. `--no-vm-nesting`
  turns that off.
- `host` is not in the table because it is not a sandbox, and no install has it: it runs commands
  on the machine itself with the user's own reach, and only automated tests turn it on
  (`ACORND_TEST_HOST=1`). It is not a default, not in the chooser, and not a name `workspace add`
  accepts.
- `acornd doctor` says which engines work here, why one does not, and that `host` is off.

```bash
acornd workspace add demo ~/work/demo --sandbox bwrap
acornd workspace add daemons ~/work/daemons --keep-alive   # a workspace that runs things
acornd workspace list                                      # the engine is a column
```

### Agents, skills, commands, guards

- **Agent definitions** are Markdown files with front matter: name, description, model, thinking
  level, allowed and denied tools, and instructions.
- **They stack in layers.** A workspace definition shadows a global one of the same name. The
  inspector's explain view shows which file won.
- **Skills** are offered by name and description only. The model loads the body when it needs it.
  An agent that names skills gets only those.
- **Slash commands** come from `commands/` or `prompts/`, in the workspace and in the global
  folders. They expand on the server, so the transcript records what the model saw. `/compact` and
  `/clear` are built in.
- **Guards** block a tool call and tell the model why. A guard can match a command pattern or a
  path outside the workspace. Guards accumulate across layers.

A global definition lives in `~/.acornd/agents/<name>.md` or `~/.agents/agents/<name>.md`. A
workspace definition lives in `<workspace>/.acornd/agents/<name>.md` or
`<workspace>/.agents/agents/<name>.md`. Ours wins when both exist. Pick the agent in the top bar.
**Reload files** in the inspector re-reads them without a restart.

```markdown
---
name: researcher
description: Reads before it writes
thinking: medium
allow-tools: read, bash, skill
deny-tools: write, edit
---
You are a researcher. Read before you answer, and name the paths you relied on.
```

### Delegation

- `delegate` hands a task to a subagent and waits for the answer.
- `subagent` starts one that keeps working while you answer, and wakes the parent when it reports.
- `peer: true` makes a normal conversation instead. It appears in your rail, records who created
  it, and outlives its creator.
- Limits are set in the agent definition: children, depth, peers, wakes, reachability. The
  inspector's Delegation card shows the policy.

### Secrets

Tool secrets are values a command needs. They stay out of the transcript and out of the model.

- Global or per workspace. A workspace secret with the same name wins.
- The model sees names and descriptions. It asks for a value through a card, and the value goes to
  the gateway, never the model.
- In a sandbox, the variable holds a placeholder: `$MSB_<NAME>`. The engine swaps in the real value
  only for the secret's allowed hosts and only over HTTPS. A plain HTTP request is not intercepted.
- `${NAME}` and `{{NAME}}` are not placeholders. They pass through.
- Stored 0600 in `~/.acornd/secrets.json`.

```bash
acornd secret add GITHUB_TOKEN --global --description "release token" \
  --hosts api.github.com,github.com < token.txt
acornd secret list
acornd secret show GITHUB_TOKEN      # what it is for and where, never the value
```

A secret added during a run is stored first. The gateway restarts the sandbox when the turn
settles and continues the run itself, so you do not resend anything. Rotating a value is live.

The default image has no `curl`. Install one inside the sandbox, or use the project's own HTTPS
client.

### People and rooms

- The first person to claim the install is the admin.
- The admin invites people by address, sets the models each may use, and says who may lead a room.
- Each person gets a personal workspace named after their address.
- A room has one owner and its guests. The owner invites, removes, and forgets. A guest may leave.
- A guest can prompt, steer, stop, and compact. A guest cannot create, fork, hide, rename, reset,
  or change the model.
- The same list is in the terminal: `acornd people`.

### Remote access

**Settings > Remote** puts the console behind Cloudflare Access:

1. Paste a Cloudflare API token. The screen lists the zones the token can see.
2. Pick a zone and type a hostname. A label (`console`) is completed with the zone. A full name
   outside the zone is refused.
3. **Apply** creates what is missing: the tunnel, the DNS record (`CNAME`), the one-time PIN, the
   Access application, the group, and its policy. A second press creates nothing.
4. **Turn on** restarts the console. It listens on 127.0.0.1 only, and the tunnel carries the
   browser.

After that, people sign in with a code sent to their email. The LAN address stops answering.

Without a tunnel, listen on 127.0.0.1 only and use SSH:

```bash
acornd start --host 127.0.0.1
ssh -L 7777:127.0.0.1:7777 desktop
# then browse http://localhost:7777
```

A script can call the API with `Authorization: Bearer $(acornd token)`. A browser signs in with the
install token, or with the setup code on a fresh install, and gets a cookie that page scripts cannot
read, for a month. The token is a 0600 file in the install folder, read per request, so a rotation
takes effect at once.

### Models and providers

- Add a provider key in **Settings > Providers**, or from stdin:
  `echo "$OPENCODE_API_KEY" | acornd provider add opencode-go`.
- The list comes from the model library: over 40 providers, each with its own model list.
- A new install allows no model until the admin ticks it. The list of allowed models binds
  everyone, including the admin. A person's own list narrows it further.
- A conversation, its agent, its workspace, and the install can each set a default model and
  thinking level. The nearest wins. With nothing set, a preferred model runs, not the first one in
  the list. `acornd model set <provider>/<modelId>` chooses the install default.
- A thinking level the model cannot express is clamped. What was asked for is kept and reported.

### Recovery and updates

- If a worker dies, the gateway says why and restarts it after a wait that grows while it keeps
  failing. Every run resumes from its last commit, in about two seconds. The stream reconnects and
  catches up on what it missed.
- The console checks GitHub for a new release every hour, and on the Refresh button.
- An update backs up config, the state database, and the workspace list, and keeps the last three
  sets. Secrets and VM disks are not backed up.
- A running conversation blocks the update with a choice: wait for it to finish, stop the runs, or
  cancel.
- The release's migration script runs at boot. A non-zero exit stops the boot. `/healthz` must
  pass for 30 seconds, or the previous binary comes back with the cause on screen.

### Command line

Every command answers `-h`. `acornd` with no arguments lists them.

| Command | What it does |
|---|---|
| `start` | Run the gateway in the foreground. `-d` detaches, logs to `<data-dir>/acornd.log`, and returns. |
| `stop` | Stop the run recorded in this install folder. |
| `restart` | Start it again with the arguments it was started with. |
| `status` | Is it running, where, and since when. |
| `setup` | Check this machine, and make it able to run sandboxes. |
| `setup-code` | Print, rotate, or remove the code that claims a fresh install. |
| `doctor` | Report what the sandbox engines can do here. Changes nothing. |
| `workspace` | List, add, forget, and purge workspaces. |
| `secret` | List, add, show, rotate, and remove secrets. |
| `provider` | List, add, and remove model provider keys. |
| `model` | List or choose the default model. |
| `people` | Who may use this install, and what they may do. |
| `remote` | The tunnel a browser comes through, and who it admits. |
| `token` | Print the token that proves you own this install. |
| `update` | Check for a release, and install it. |
| `version` | Print the running version. |

### Not built yet

Memory, MCP, codemode, triggers, and versioned agent definitions. `docs/adr/` holds the designs
they will land against.

## How it works

```
browser ──HTTP+SSE──► gateway (Go) ──► control ──JSONL/stdio──► worker (bun)
                                                                  │
                                                                  ▼
                                                        sandbox (microVM or bwrap)
```

The gateway and control are packages in one Go binary. The worker is the harness: it owns one
workspace's storage and runs the agent loop.

| Layer | Does | Does not know |
|---|---|---|
| Browser console | Draws state and sends actions. | Workers, storage, providers. |
| Gateway | HTTP, server-sent events, sign-in, cookies, uploads, slow clients. | What an agent or a workspace is. |
| Control | Workspaces, models, keys, people, worker lifecycle, usage. | How the agent loop works. |
| Worker | The agent loop, tools, skills, the sandbox. | The console, the install database. |

Three rules decide the shape:

1. **One workspace = one worker = one storage.** All conversations in a workspace, including
   subagent conversations, live in one SQLite file. A worker crash affects that workspace, and
   every run resumes from its last commit.
2. **The console holds no transcript.** It draws what the worker committed. Nothing reaches the
   screen before it is stored.
3. **Stored agent configuration is names, never code.** An upgrade cannot rewrite history.

### Worker protocol

Control and worker talk JSONL over stdin and stdout, one object per line.

| Direction | Messages |
|---|---|
| control → worker | `describe`, `open`, `attach`, `detach`, `prompt`, `abort`, `usage`, `shutdown` |
| worker → control | `response`, `view`, `submission` |

A `view` is the whole committed state of one conversation, not a diff. `seq` is the resume cursor:
a client that reconnects asks for the newest view and is correct by construction. The protocol is
written once per language, in `worker/src/protocol.ts` and
`gateway/internal/protocol/protocol.go`, and documented in
[docs/adr/0002-layers-and-protocol.md](docs/adr/0002-layers-and-protocol.md).

A second harness (a different worker) only has to speak these commands. Control does not change.

### Where things live

The install folder is `~/.acornd` by default (`--data-dir`, `ACORND_DATA_DIR`). In this table,
0600 means only your user can read the file.

| Path | What |
|---|---|
| `bin/acornd` | The binary, when the install script put it there. |
| `credentials.json` | Provider keys. 0600. |
| `secrets.json` | Tool secrets. 0600. |
| `people.json` | Who may use the install, and what they may do. |
| `remote.json` | Tunnel and Access state, and the Cloudflare token. 0600. |
| `settings.json` | Install defaults and the allowed models. |
| `workspaces.json` | The workspace registry. |
| `token` | The install token. 0600. |
| `setup-code` | The claim code, until somebody claims the install. 0600. |
| `run.json` | The recorded run: pid, arguments, address, start time. |
| `acornd.log` | Output of a detached run. |
| `tools/bun-1.4.4/` | The pinned bun. |
| `worker/` | The worker sources for this release. |
| `state/<name>/` | One workspace's SQLite storage, settings, and drafts. |
| `sandboxes/` | Engine images, workspace disks, snapshots. |

Workspace folders are in `~/acornd/workspaces` by default (`--workspaces-dir`).

The gateway serves the console on every interface, port 7777, by default (`--host`, `--port`). Once
remote access is configured, it listens on 127.0.0.1 only. A flag wins over `ACORND_DATA_DIR` and
`ACORND_WORKER`.

## Development

### Requirements

- Go **1.27.3**. The patch matters: `gateway/go.mod` pins it, and CI reads the version from there.
- bun **1.4.4**. The runtime pins it in `gateway/internal/update/worker.go`.
- make and gcc.
- `unzip` for the bun installer, and `procps` for `pgrep`, which two end-to-end tests use.
- `git` with an `origin/main` ref. `make test` compares the coverage and mutation minimums
  against it, so a single-branch clone fails until `git fetch origin main`.
- A browser for `make ui` and `make ui-check`, at `CHROME_PATH` (default `/usr/bin/chromium`).
- About 6 GB of disk for `make tools`. On a machine with 2 GB of RAM, use
  `GOGC=20 GOMAXPROCS=1 make tools`, or the compiler is killed partway through gosec.
- A provider key, or the dev provider.

### Setup

```bash
# Go: https://go.dev/dl, at the patch gateway/go.mod pins
curl -fsSL https://bun.sh/install | bash   # bun, into ~/.bun/bin
sudo apt install make gcc                  # Debian/Ubuntu build tools

make deps     # bun install in worker/, ui/, tools/
make tools    # the six Go linters, compiled from source
make build    # -> ./bin/acornd
```

`make tools` installs staticcheck, golangci-lint, deadcode, gremlins, govulncheck, and gosec. If
you skip it, `make lint` prints `not installed (make tools)` and passes that part. A green gate
then proves less than it looks.

### Run

```bash
echo "$OPENCODE_API_KEY" | ./bin/acornd provider add opencode-go
ACORND_WORKER=$(pwd)/worker ./bin/acornd start   # http://<host>:7777
```

`ACORND_WORKER` is required in a source tree. Without it, the gateway looks for the worker a
release would have downloaded, and a dev build never fetches one.

`make dev` starts the same thing with a scripted provider. It answers every turn with one real
`bash` call and a fixed reply, so the whole path runs offline.

### Checks

| Target | What it does |
|---|---|
| `make check` | Fast: the bundle, the compiler, and the unit tests. No processes. |
| `make lint` | gofmt, vet, tsc, oxlint, oxfmt, the architecture test, three cross-builds, golangci-lint, deadcode. |
| `make test` | The gate: lint, the coverage and mutation minimums, the unit tests, then the feature suite. |
| `make test-sandbox` | The microVM boundary: live driver, SDK, and four end-to-end tests. Needs `/dev/kvm`. |
| `make test-bwrap` | The bubblewrap engine end to end. Fetches an image, so `make test` skips it. |
| `make test-youki` | The youki engine end to end. Fetches its runtime and an image; needs user namespaces and a delegated cgroup v2 (a systemd user's slice is delegated; run it in a `systemd-run --user --scope` or a service). |
| `make ui` | Drives the real console in headless Chromium. Needs a running gateway. |
| `make ui-check` | The browser suite. Builds, starts its own gateway, stops it after. |
| `make modernize` | The Go idiom analyzer. A daily workflow runs it; the gate does not. |
| `make mutations` | Mutation testing for one package: `PKG=./internal/control`. |

`make test` runs the end-to-end tests with the sandbox tier forced off, so the tools take the
refusal path. `make test-sandbox` runs them against real microVMs. The end-to-end tests need bun
and `make deps`. Without them they skip with a reason.

`docs/engineering/standards.md` is what the code is held to, rule by rule, with the check that
enforces each one. `docs/features.md` is the inventory: what the platform does, as things you can
observe, and the test that covers each.

### Layout

| Path | What |
|---|---|
| `docs/adr/` | Decisions. Start with `0001-concepts.md` and `0002-layers-and-protocol.md`. |
| `docs/features.md` | What the platform does, and the test for each. |
| `docs/engineering/standards.md` | The rules the code is held to. |
| `gateway/` | Go: HTTP server, control, worker supervision, the embedded console. |
| `gateway/web/` | The built console bundle the gateway serves. |
| `worker/` | TypeScript: the agent loop, tools, skills, credentials. |
| `ui/` | The console: React, theme, Storybook. Built by `make ui-build`. |
| `tools/` | Browser-driven checks. |
| `scripts/install.sh` | The release installer. |
