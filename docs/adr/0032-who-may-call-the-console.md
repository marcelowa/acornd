# ADR 0032 — Who may call the console

Status: accepted (2026-10-30), built
Date: 2026-10-30

## Context

[ADR 0020](0020-open-by-default.md) removed the token because the console was one owner's tool
on a network they chose, and it said so plainly: *"the honest state of authentication here is
'absent' until it is designed."* `icp.md` says the same thing from the other end — no teammate or
tenant exists, so no persona for one is in the panel. That was true. It stops being true the
moment a family member or a friend opens the console from their own machine.

Three facts in the code make the design narrower than it first looks:

- **There is no caller anywhere.** `httpapi` finds a workspace and hands the request to a
  `Control`, which is the owner. Nothing in the tree asks who is asking: not a route, not a
  handler, not the worker protocol. ADR 0001 already assigns this to control — *"it owns
  identity, resources, credentials, and policy"* — and it was never built.
- **The workspace is already the isolation unit.** One workspace is one worker, one SQLite, one
  sandbox, one secrets scope ([ADR 0011](0011-workspaces.md)). Conversation ids are composite
  (`general/1276`) because two storages both mint `1`. Privacy between two people needs no new
  storage concept; it needs two workspaces.
- **The port is reachable from inside a guest.** ADR 0012, under known limits: *"with egress on,
  the gateway's own API is reachable from inside… which listens on every interface and needs no
  token by default."* ADR 0023 recorded the same route for bwrap until it was closed in the
  argv. Any design where "the request came from this machine" means "the owner" therefore makes
  **every agent in every workspace the owner** — the agent has `bash`, so it can `curl` the API
  and add a provider key or read a transcript.

The reachability of the console from outside the machine is
[ADR 0033](0033-remote-access.md)'s problem. This ADR is only: who is calling, and what may they
touch.

## Decision

### 1. A door is a proof, never an address

Two proofs are accepted, and nothing else:

| door | proof | who |
|---|---|---|
| local | the token file, `<data-dir>/token`, mode 0600 | the owner, from the CLI |
| remote | a signed identity assertion, verified | a person |

`<data-dir>/token` is minted at first boot, printed once, reprintable with `acornfall token`, and
rotatable. ADR 0014 already names that file, keeps it out of every sandbox mount, and pins the
exclusion with a test — so a guest can reach the port and still not hold the proof. Loopback is
**not** a door: a request from `127.0.0.1` with no token is refused, exactly like any other.

A request with no proof is `401` and one sentence. This is what closes ADR 0012's known limit: the
limit existed because the API assumed every caller was the owner.

The door also checks `Origin` and `Host` on state-changing methods. The console sets no cookie of
ours, so `SameSite` protects nothing — the door's own cookie authenticates a cross-site form POST
perfectly well, which is why this is not optional once the console is reachable from a browser.

### 2. The identity seam, one interface, two implementations today

```go
// identity.Email answers who is calling, or an error that says why it could not tell.
type Identity interface {
	Email(r *http.Request) (string, error)
}
```

| implementation | how it decides | configured by |
|---|---|---|
| `token` | constant-time compare against `<data-dir>/token` | on, always |
| `access` | verifies Cloudflare Access's signed assertion | `ACORNFALL_IDENTITY=access` |

The assertion is a JWT. It is verified — signature against the publisher's keys, `aud`, `iss`,
`exp` and `iat` with 60 seconds of leeway, and `email_verified: true` — and the plaintext
`X-Goog-Authenticated-User-Email`-style header is **never** read, because anyone who reaches the
port can write it. Keys come from `ACORNFALL_IDENTITY_KEYS`, are cached, and are refetched on an
unknown `kid`; a fetch failure falls back to the cache and refuses if there is none.

`iap` is the same code with a different issuer and audience, and is **not built** until someone
wants it. Two implementations exist today (`token`, `access`), which is what makes the interface
real rather than speculative; a shared conformance suite runs against both
(`identity/conformance_test.go`), so a third is a table entry rather than a rewrite.

### 3. People, and a door that admits only the ones the owner named

`<data-dir>/people.json` — the console's screen says **People**, and `users` is a word this
codebase does not otherwise use. One record per identity:

```json
{ "email": "ana@example.com", "name": "Ana", "role": "person",
  "state": "invited", "models": ["anthropic/claude-sonnet-4"], "can_create": false,
  "created": "2026-10-30", "last_seen": "2026-10-30" }
```

| state | how it is reached | what it can do |
|---|---|---|
| `invited` | the owner adds an email in People | nothing until the first sign-in |
| `active` | the first sign-in after `invited` | their personal workspace, and rooms they are in |
| `revoked` | the owner | nothing; every request refuses from the next one |

**Nobody the owner has not named reaches the process at all.** There is no waiting room and no
`pending`: the door's allowlist is the same list, pushed to it whenever a person changes
([ADR 0033](0033-remote-access.md)), so a stranger's request is refused before a handler runs and
before anything is written. The console owns the list; the door enforces it. Two lists would be two places to forget a revocation, so there
is one, and a person's state is the only input to it.

The reconciliation runs the other way too: an email the door still admits but `people.json` no
longer names is refused with a sentence saying so, and the next reconcile removes it from the door.
That is the one state a drift between the two can produce, and it fails closed.

**Exactly one owner**, and it is claimed from the CLI: `acornfall people claim you@example.com`.
The token already proves ownership, so there is no first-come race and no self-promotion through
the API. An install with no `people.json` behaves as it does today — one owner, no people — except
for the one rename in decision 9.

When a person first becomes `active`, their personal workspace is created then, with the install's
default model if it is in their allowlist and the first allowed model if it is not. Nobody gets a
workspace they never showed up for.

**There is no session table and no cookie of ours.** Every request re-asserts identity, so a
`revoked` record takes effect on the next request with no expiry window, and the door's own
session lifetime is the only thing left to expire. That is why `revoked` is instant and why there
is no password, no reset flow, and no email sent by acornfall: the door proves the mailbox, and
the invitation is a message the owner sends.

### 4. A workspace has members, and the owner is not one

`Workspace` gains `Members []string`, a list of emails. A workspace-scoped request — anything
under `/api/workspaces/{name}/…` — reaches the workspace only when the caller is a member. The
routes that are not workspace-scoped (`/api/workspaces` itself, providers, update, install
defaults) are the owner's, and none of them carries a transcript.

A workspace with more than one member is what the console calls a **room**. The word is a screen's
word for a shared workspace, not a concept: nothing in the storage, the protocol, or the registry
knows it.

The install owner is deliberately **not** a member of anyone else's workspace. Owning the install
means lifecycle, credentials and policy; it does not mean reading a conversation. Every refusal is
`404`, not `403`: a person must not be able to enumerate the workspaces they cannot see.

Two consequences, both stated rather than hidden:

- **The owner can still reach the disk.** Their SQLite is a file on the machine, and `bash` or
  `ssh` opens it. The claim here is "the product offers no path", which is a weaker and more
  honest claim than confidentiality — the same register ADR 0011 uses about confinement.
- **Removing a workspace stays a CLI command.** `acornfall workspace remove` already exists and
  never deletes the folder. The interface does not get a button for another person's workspace,
  for the reason ADR 0011 gives about naming a path: the destructive power belongs where it is
  visible.

Migration: `general` becomes the owner's personal workspace at boot rather than at claim — the
local door already names the owner (decision 1) — and decision 9 says what that rename has to
move. An install that never adds a person has no members and no changes.

### 5. Two roles

| | owner | person |
|---|---|---|
| their own conversations, drafts, attachments | yes | yes |
| a shared workspace they are a member of | if also a member | yes |
| create a workspace | yes | only if `can_create` is set on their record |
| remove, re-image, re-engineer a workspace | yes | no |
| `host` engine | yes | **never** |
| provider keys and provider login | yes | no |
| global-scope secrets | yes | no |
| workspace-scope secrets in their own workspace | yes | yes |
| model choice | any | within their allowlist |
| install defaults, `update`, `setup`, `doctor` | yes | no |
| usage numbers for the install | yes | their own only |

`viewer` and `operator` are rejected until someone asks for them. A read-only guest is a real
feature; it is not a role, because it changes what a conversation *is* — someone who can read a
room and never speak is a different sharing model, not a permission.

**`host` is refused for a person, at the API and in the create form.** A person's workspace always
runs in a sandbox; `host` is the owner's own shell, and handing it to a friend hands them the
machine.

### 6. The model allowlist, enforced where it is assigned

`models` on a person restricts what they may run. It is enforced on the assignment path —
`POST /agent`, `POST /default-model`, and a workspace default set on their behalf — with a refusal
naming the allowed set. Filtering the catalog the interface offers is a convenience; the refusal
is the control.

**A shared workspace's model belongs to the room, not to the person.** The member who creates it
chooses the model from their own allowlist, and everyone in the room uses it. Intersections were
considered and rejected: a rule that says "the cheapest of the two allowlists, unless the room was
created before Tuesday" is a rule nobody will remember.

The allowlist is a proxy for cost, not a ceiling. A cheap model in a runaway loop still spends,
and a runaway run is bounded by the run and turn limits instead. Per-person usage is shown as a
number — spend, not content — and a per-person cap is deliberately deferred, not forgotten.

### 7. A shared workspace names its speaker

In a workspace with more than one member, a user message is prefixed with the speaker's display
name before the prompt is sent — `[Ana] can you check the build?`. A personal workspace is
unchanged.

**Built at the edge, not in control.** The name lives in the book and the membership in the
workspace registry, and the edge is the one layer that can see both without control learning what a
book of people is (ADR 0002). The rule is three lines with a test apiece: named when the room has
more than one member and the speaker is not the owner, untouched when the message is a command —
`/compact` is expanded by its first character, and a prefix would turn it into a sentence about
compacting — and untouched when the message is files without words.

**No `author` field on entries.** Attribution here is for the model, and the model reads assembled
text; a durable schema change to carry a display name would be a harness change for a presentation
need.

### 8. When the console can be a lock, it is one

`start` refuses a non-loopback `--host` on an install that has people, and the refusal names both
ways out: the tunnel ([ADR 0033](0033-remote-access.md)) does the reachability, and `127.0.0.1` is
the only address it forwards to. A door that only matters on one address is not a door.

An install with no people is untouched: `0.0.0.0:7777`, no token, no assertion — ADR 0020 stands
for the single owner it was written about, and ADR 0012's known limit stays true for exactly that
install. Turning on people is the change that closes it.

### 9. Every person has a personal workspace, and none of them is called general

`general` was "the workspace" because there was one. Multi-user makes that name two things at once
— a constant meaning "the default" and a `General bool` on the record — and both go.

- **The default workspace is the caller's, not a constant.** `GeneralWorkspaceName` disappears.
  Where the code falls back to `general` today (`ensure`, routing, the API's unnamed workspace, the
  conversation list), it falls back to the calling person's personal workspace.
- **A personal workspace is derived, never flagged.** It is a workspace with exactly one member,
  or the install's own room. `General bool` becomes
  `Name == "personal" || len(Members) == 1`: a membership list is a fact, and a flag is a claim about
  the same fact that can disagree with it.

  **Why the name is in the rule.** The install's own room was created before there were people and
  has no member; every room made since has one. Loading an install fills the owner's room with their
  address once the owner is known, so the two shapes converge — but a single-owner install never
  learns who its owner is, and that room must still be un-forgettable. The name is not a second flag:
  it is the identity of one record, and membership answers for every other room.

  **Why not "at most one member".** Because a workspace somebody made and has not shared yet has no
  members either, and calling that somebody's own room would make it un-removable. The end-to-end
  suite caught exactly that when the rule was written that way; the test is
  `TestARoomWithNoMembersIsNotAnybodysOwnUnlessItIsTheInstalls`.

  **Built.** The field and its wire copy are gone; the readers are `Workspace.Personal()`, a sort
  that puts somebody's own room first, and the interface's rule for when *Forget* is offered.
  `exactlyOneGeneral`, which repaired a registry hand-edited into two, went with it: there is
  nothing left to disagree. A file written under the old rule still loads, and `general` keys in it
  are ignored — there is a test for that, because a migration nobody tested is a guess.
- **Its name belongs to its person, and `personal` is the install owner's.** The owner's is
  `personal`; everyone else's is their email's local part made unique when it is created (`ana`,
  then `ana-2`). The console labels all of them **Personal** and never shows the stored name. A
  workspace's name is immutable once created (ADR 0011), so a rename is a migration, not an edit.

Why not the obviously nicer thing — every personal workspace literally named `personal`, resolved
per caller? Because `personal/1276` would then be two different transcripts in one namespace, and a
composite id is the only thing keeping two storages apart. A reserved name that resolves to "mine"
is a second identity for one thing, which is what ADR 0001 spends a paragraph warning about.

**The rename, and what it has to move.** An existing install's `general` becomes the owner's
`personal`: the registry record; `state/general/` becoming `state/personal/` **with the whole
folder** — `session.sqlite`, its write-ahead log and shared memory, `settings.json`, `drafts.json`
— because ADR 0011 records what moving `session.sqlite` alone cost, and this is the same trap; and
every stored `workspace/id` key (bindings, the selected conversation, attachments) rewritten to
match. It runs once, at boot, in one transaction on the
registry; an install whose rename cannot complete keeps the old name and says which file it could
not move, rather than starting with two folders and one of them empty.

## What it costs

- **A token file exists again.** ADR 0020 removed the token because every first open depended on a
  file or a log line. The CLI reads this one automatically, so a first open on the machine is
  unchanged; only a script that talks to the API by hand needs to know it exists.
- **`general` is gone.** Every install renames it to `personal` once, moving the state folder
  whole and rewriting the stored `workspace/id` keys. That is a migration with a transaction
  around it, not a string change, and it is where this ADR could lose a transcript if it is done
  the way ADR 0011's first attempt did it.
- **Some routes stop being universal.** Every handler that does not name a workspace now depends on
  who is calling, and the ones that do have one more check in front of them.
- **A person cannot be given the machine.** Anyone who wanted a friend to have `host` for one
  afternoon gets a refusal instead. That is the point.

## Alternatives

**Keep IAP as the identity provider.** Rejected for the same seam and different plumbing: it needs
a load balancer, a certificate and a connector to reach a machine at home, and the assertion it
hands over is the JWT this ADR already verifies. It is the second implementation, not the first.

**Tailscale identity (Serve + whois).** Rejected on the friction, not the security: it is the
cheapest of all of them technically, and it asks every family member to install an app. Recorded
here because it becomes the right answer the day the people involved already have it.

**Invite links as the only proof.** Rejected: the link is then the credential, a forwarded message
is a full account, and there is nothing to verify. It survives as the fallback in
[ADR 0033](0033-remote-access.md) for a person who cannot reach the door at all.

**A session table in control.** Rejected: a second answer to "who am I", a revocation lag, and
storage for something the door re-asserts on every request anyway.

**An `author` field on transcript entries.** Rejected under decision 7.

**Address-based trust (`--host 0.0.0.0` plus a middleware).** Rejected because of the third fact in
the context: the guest reaches the port, so an address is a claim the thing we are containing can
make.

## Verification

The naming is the plan; each of these is a test that must exist before this is called built.

- Identity: `TestAValidAssertionNamesItsEmail`, `TestTheWrongAudienceIsRefused`,
  `TestThePlaintextIdentityHeaderIsNotEnough`, `TestAnExpiredAssertionIsRefused`,
  `TestAnUnverifiedEmailIsRefused`, `TestKeysThatCannotBeFetchedFailClosed`, and the shared
  conformance table that `token` and `access` both run.
- Doors: `TestALoopbackRequestWithoutProofIsRefused`, `TestARotatedTokenIsTheOnlyOneThatWorks`.
- People: `TestRevocationTakesEffectOnTheNextRequest`, `TestOneOwnerAndTheCliClaimsIt`,
  `TestAPersonsWorkspaceIsCreatedOnFirstSignIn`,
  `TestAnEmailTheOwnerNeverNamedNeverReachesTheProcess`,
  `TestAnEmailTheDoorStillAdmitsButPeopleDoesNotIsRefused`.
- Personal workspaces: `TestEveryPersonStartsWithOneAndItHasOneMember`,
  `TestTheDefaultWorkspaceIsTheCallersOwn`, `TestAPersonsWorkspaceNameIsUniqueAtCreation`.
- The rename: `TestGeneralBecomesPersonal`, `TestTheRenameMovesTheWriteAheadLogToo`,
  `TestTheRenameRewritesStoredWorkspaceIds`, `TestAFailedRenameKeepsTheOldName`.
- Authorization: `TestAPersonCannotReadAnothersWorkspace` (404), `TestTheOwnerIsNotAMember`,
  `TestAModelOutsideTheAllowlistIsRefusedOnAssignment`, `TestAPersonCannotChooseTheHostEngine`,
  `TestGlobalSecretsDoNotReachAPersonsWorkspace`.
- Rooms: `TestASharedWorkspaceNamesItsSpeaker`, `TestAPersonalWorkspaceDoesNot`.
- The invariant: `TestANonLoopbackBindIsRefusedWhenPeopleExist`, plus the existing
  `TestABwrapSandboxCannotReachTheMachinesLoopback`, which is the other half of the same claim.
- `internal/identity` joins `.mutationfloors`: a mutated audience check has to die.

## Not in this ADR

- **How a browser reaches the machine.** The tunnel, its provisioning, and what the owner pastes
  into the console: [ADR 0033](0033-remote-access.md).
- **Per-person provider credentials.** One `credentials.json` is spent by everyone, and this ADR
  accepts that: a person is trusted to spend the install's key, within an allowlist of models. A
  per-person key changes `credentials.json`, the vault's scope, and usage accounting, and it is its
  own decision.
- **Spend caps.** Deferred with the reason in decision 6; the number per person is the mirror that
  comes first.
- **Encryption at rest**, and therefore any claim about a person's history against the owner with
  the disk. Stated in decision 4 as a limit rather than implied.
