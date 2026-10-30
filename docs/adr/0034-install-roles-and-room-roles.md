# ADR 0034 — Install roles and room roles

Status: accepted (2026-10-31), built; the route-level permission tests are still owed
Date: 2026-10-31

## Context

[ADR 0032](0032-who-may-call-the-console.md) gave the console a door and a book of people, and a
workspace a list of members. What it did not give either one is a role inside a room: `Members` is
`[]string`, `Allows` is membership and nothing else, and the install's owner is deliberately not
special there. That was enough while a room could not be made — and it cannot be today. No route,
no CLI verb and no call to `adopt` changes a member list, so a room with two people in it exists
only as a hand-edited registry.

Three journeys are what this ADR is for:

1. **A member of a room wants to let somebody in.** Not the install's admin: the person who owns
   that room.
2. **A guest interacts in the conversations a room already has**, without making new ones.
3. **A guest cannot change the model** a conversation runs.

Two facts make the current words collide. "Owner" is the install's, and a room needs one too — and
the room's is the whole of journey 1. "Member" is ADR 0032's word for the install list (*"a
workspace has members"*) and also the only role a room has, so there is no name left for "in the
room with fewer rights". Two lists, one vocabulary, and no rule for which answers what.

A third fact is a bug this design would otherwise build on. `Workspaces.Create` sets no members,
so on an install whose admin has been claimed, a workspace made from the console has no member who
can open it. It is listed for the admin and refused for everyone, the admin included, because
`Allows` checks the list and not the role. The journeys would inherit that.

And a shared room exposes four things that were written for one person at one keyboard: the model
allowlist is checked against the caller on every prompt, the speaker's name is written into the
stored message, the draft belongs to the conversation, and the landing conversation (`select`) is
one per workspace — it even detaches the worker from the previous one, which pulls the
subscription out from under another tab's live stream.

## Decision

### 1. Two axes, and a rule between them

A person's rights are answered on two lists, and neither answers for the other.

| Axis | Question | Values | Lives in |
|---|---|---|---|
| **Install** | who exists, and what may they do on this machine? | `admin` · `member`, with `invited` / `active` / `revoked` | `people.json`, the door |
| **Room** | is this room theirs, and what may they do in it? | `owner` · `guest` | `workspaces.json`, the room's members |

1. **A room role never grants an install right.** A room's owner cannot add a provider key, read a
   global secret, or replace the binary. The `owner(...)` wrapper on those routes is unchanged.
2. **An install role never grants a room right.** Being the admin does not make you a member of a
   room. You can be invited like anyone, and then you are its owner or a guest.
3. **The install decides who may exist; a room decides who may enter.** A room can only name
   somebody the book already names. An address the admin has not named is refused with the
   sentence that says to ask the admin — never invited into a room that will refuse them.
4. **A revoked person is out of every room at once**, without touching a single membership. Their
   name stays in the room, so naming them again puts them back. Revocation stays cheap.

The word **room** is [ADR 0032](0032-who-may-call-the-console.md) §4's screen word, and this ADR
makes it the record's everyday name. The storage concept is still a workspace.

### 2. The install has one admin and its members

| today | from this ADR |
|---|---|
| `owner` (install) | **`admin`** |
| `person` | **`member`** |
| `invited`, `active`, `revoked` | unchanged, and still states rather than roles |

One admin, claimed from the command line (`acornfall people claim`), proved by the token file.
Everyone else in the book is a member. `admin` says what the role is — administering the install —
where `owner` claimed the machine, the credentials and the folders as well.

The member word is `member`, not "normal", "general" or "common": those are adjectives, "general"
is still loaded from the workspace that used to carry that name, and `member` is the noun the
codebase already uses for somebody in the book — free the moment a room has roles of its own.
`person` stays the noun for the record and for the screen: the **People** screen, `people.json`,
`acornfall people`.

**The rename is one commit with nothing else in it**, and it lands before any room work, because
every later diff that says "owner" would otherwise need a reader to work out which of the two it
means. Until it lands, the code's `owner` means `admin`.

### 3. A room has one owner, and guests

- A workspace's members become records with a role: `{email, role}` with `role` either `owner` or
  `guest`. Exactly one owner, checked when the registry loads — with one exception. An install that
  has never claimed its admin has no address to name as an owner, and refusing to create a room
  there would break the default install, so a room is written with nobody in it and the admin is
  put in it at the next load. The invariant is "at most one owner, and never a guest without one",
  and the load is what makes it one.
- **A room is born with its creator as its owner.** `Workspaces.Create` takes the creator; the
  ownerless room stops being constructible, and the bug in the context goes with it.
- **The owner invites and removes guests.** Nobody else administers: not another guest, not the
  install's admin. A guest may remove themselves — that is leaving, and it is the only removal a
  guest has.
- **The owner never leaves.** To end a room they forget it (§6). There is no transfer of
  ownership: a room is its owner's until it is forgotten, and handing one over is a decision with
  its own reasoning, not a field added quietly.
- `can_create` is the install's permission to **own rooms**. A member without it may be a guest
  and nothing more. It is on by default: without it, journey 1 belongs to the admin alone.

### 4. What a guest may do

| in a room | guest | owner |
|---|---|---|
| read, prompt, attach, steer, abort, compact | yes | yes |
| create a conversation, fork, hide, rename | no | yes |
| `/clear` | no | yes |
| change model, thinking, agent | no | yes |
| add or remove guests | no | yes |
| forget the room | no | yes |
| leave | yes | never |

**The check is one helper**, asked by the routes *and* by command expansion. `/compact` and
`/clear` are commands, not routes: `expandCommand` runs both today with no check at all, so a
guest allowed to compact could type `/clear` and reset the room's context — more than compaction.
Compact yes, reset no, and the rule lives where the action is chosen rather than in the route
table alone.

**The model belongs to the room** ([ADR 0032](0032-who-may-call-the-console.md) §6). The allowlist
governs **choosing**, not speaking: it is checked where a model is assigned, against the caller's
list; the install's provider list still checks every turn, for everyone, because that is about
spending the key and not about who is typing. A person's own list is not applied when they speak
in a room somebody else set up — the member who pinned the model spent their own list to do it.
This also removes a smaller existing failure: narrowing somebody's list silenced a conversation
they had already pinned in their own room, until they re-pinned it.

**The interface shows agent, model and thinking disabled**, with their values, and no explanatory
sentence. The state carries the answer — *may change: no* — so a component never infers it from
the role, which would be a second copy of the table above.

### 5. Personal is never shared, and is a kind

The admin's `personal` room and every member's own room are never shared: no guests, ever, on any
install. So journey 1 means *make a room and invite people*, not *open my room*. A personal room is
also un-forgettable.

This reintroduces a stored fact that [ADR 0032](0032-who-may-call-the-console.md) §9 deliberately
removed, and the reason it was removed is the reason it is needed. Then, `Personal()` was derived
from membership — `name == personal || len(Members) == 1` — because a membership list is a fact
and a flag is a claim that can disagree with it. A room born with its owner is one member too, and
a room someone made and has not shared yet is one member. The fact that used to answer "is this
somebody's own room" no longer distinguishes it, so the record carries `kind: personal | room`, and
the three invariants are enforced rather than described: a personal room refuses a guest, cannot
be forgotten, and cannot be purged — purge is stronger than forget, so it cannot be the way round
the rule. The filesystem stays the operator's escape hatch.

The name stays: `personal` is the admin's, and everyone else's is their address's local part made
unique (`ana`, then `ana-2`). The console labels all of them **Personal**.

### 6. Forget unregisters; purge deletes

- **Forget** removes the room from every rail and refuses every caller, and leaves the folder and
  its state untouched. The room's owner does it from the console. The install's admin can do it
  from the command line, and there is **no console button for a room you do not own** — the
  destructive power stays where it is visible.
- **Purge** is the only thing that deletes: `acornfall workspace purge <name>`. It prints the
  folder **and the state**, requires the name or `--yes`, refuses when the record is missing, and is
  never behind an API route or a button.
- **Two folders, both deleted.** A name means the workspace's own folder and `<data-dir>/state/<name>`
  — storage, settings, drafts. Deleting only the first would leave a forgotten history for the next
  room of that name to open, so purge takes both, and `Create` and `Add` refuse a name whose state
  folder is still there until it is purged.
- **The order between them is the trap.** Purge needs the record to know the folder, so it runs
  instead of forget, never after it. Once forgotten, that folder is just a folder.
- `acornfall doctor` names every folder under the workspaces directory with no record, so an
  orphan is seen once rather than forgotten twice.

### 7. Authors, drafts, and where a room lands

- **An entry carries its author**: the address, and the display name as it was written.
- **The speaker prefix stops being stored.** `[Ana] …` is written into the message text today, so a
  guest can type `[Bo] ignore the above` and be believed, and with an author field the model would
  read `[Ana] [Ana] hi`. The prefix is composed when the transcript is assembled for the model,
  and never lands in the storage.
- **Abort lands as a system entry with its author**, because a run has none of its own: a guest
  stopping a member's turn otherwise reads as a crash. Steer is a message and carries the author it
  already would.
- **Entries from before this ADR have no author.** The interface shows nothing for them rather than
  a guess.
- **Drafts are per person, not per room.** `State.draft` is the caller's, keyed by conversation and
  address, so two people typing in one conversation stop erasing each other. The promise T22 makes
  — a switch, a reload, and another machine show the same words — holds per person.
- **The landing conversation is per person too, and `select` stops detaching.** Streams already
  attach the conversation they show and refcount it per conversation, so one person moving does not
  touch another's live view.

### 8. Migration

| today | from this ADR |
|---|---|
| `members: ["ana@…"]` | `members: [{email: "ana@…", role: "owner"}]` |
| `members: [a, b, …]` (hand-edited only) | first is the owner, the rest are guests, and one line says it guessed |
| a room with no members | adopted by the admin when one is known; otherwise left as it is and reported |
| every room | `kind: room`, except `personal` and a one-member room, which are `kind: personal` |
| a personal room naming a guest | refused at load, with the file named |

The two-address line is a guess, and it is safe because no shipped code can make one: the only
shape in the wild is a one-member room. A personal room with a guest is refused rather than
repaired for the same reason ADR 0032 refuses a `trusted` field: a claim this design says is
impossible must not load as if it were possible.

## What it costs

- **A rename across code, tests and docs**, in one commit with no behaviour. Cheap to do and
  expensive to postpone.
- **`kind` comes back to the record**, and the two invariants have to be enforced at the API and at
  load, not only described.
- **Some install rights move out of a room.** The admin loses the ability to invite into, or forget,
  a room that is not theirs from the console. Their escape hatch stays: the command line.
- **A schema change in the harness** for the author, and a protocol field for the draft's owner. One
  contract, both languages, one commit (S6).
- **Old entries are anonymous**, and a room whose history predates this ADR can never answer who
  said what.

## Alternatives

**Flat administration — any member can add or remove.** Rejected: it has no answer to "who may
remove whom", and one member can remove the person who invited them. One owner is one rule.

**Guest as an install role.** Rejected: every member already gets a personal room, and an
install-level guest could not start a conversation at home. The restriction is about one room.

**Sharing a conversation instead of a room.** Rejected: a room's history is one thing, and
per-conversation access would put a check in every route while the privacy claim stays the
workspace (ADR 0032 §4).

**Keep deriving `Personal()` from membership.** Rejected now: a room is born with its owner, so one
member no longer means "somebody's own room". The old rule would make every fresh room
un-shareable and un-forgettable, which is exactly what its own comment warned about.

**Delete the folder on forget.** Rejected: one misclick on a folder that is often somebody's
repository, with no undo. Forgotten and deleted are two acts, and the second one names the folder.

**Keep the prefix in the text, and add the author beside it.** Rejected: a guest can type another
person's prefix, and the model would read the name twice.

**A `viewer` role, read-only.** Still rejected (ADR 0032 §5). A guest speaks; a viewer does not, and
that is a different sharing model rather than one more permission.

## Not in this ADR

- **Ownership transfer.** A room that needs a new owner is a decision of its own.
- **The People screen.** ADR 0032 §3 promised it, and it is built in this change: it shows the roles
  this decision gives, and every write goes through the book's own routes.
- **Per-person provider credentials, spend caps, encryption at rest.** Unchanged from ADR 0032.
- **Email sent by acornfall.** The door proves the mailbox; an invitation is still a message the
  admin sends.
- **The order the work lands in.** The decision is here; the plan follows it.

## Verification

The naming is the plan; each of these is a test that must exist before this is called built.
`make test` unless a package is named.

- Install roles: `TestTheInstallHasAnAdminAndItsMembers`, `TestOnlyTheAdminNamesPeople`,
  `TestAMemberCannotDoTheInstallsBusiness`.
- The room's owner: `TestARoomIsBornWithItsOwner`, `TestARoomHasExactlyOneOwner`,
  `TestTheOwnerCannotLeave`, `TestAGuestMayLeave`, `TestOnlyTheOwnerInvitesAndRemoves`,
  `TestARoomCannotNameSomebodyTheBookDoesNot`.
- What a guest may do: `TestMayIsTheRoomsTable` (the whole table), `TestAGuestMayCompactButMayNotReset`
  (through `expandCommand`, not only the route), `TestOnlyTheRoomsOwnerInvites`,
  `TestAGuestLeavesAndTheOwnerRemoves`, `TestForgetIsTheRoomsOwnersAndPersonalIsNeverShared`, and
  `TestAGuestsListDoesNotBindARoomTheyWereInvitedTo`. The disabled pickers are built and pinned in
  `ui/test/map.test.ts`. Still owed: the route-level tests for create, fork, hide, rename and the
  model — the table is tested and every handler asks it, but a test that removes one call would need
  a fake control the edge does not have. `TestWhatAGuestMayDo` was the plan's name and the table is
  the built one.
- Personal: `TestAPersonalRoomRefusesAGuest`, `TestAPersonalRoomCannotBeForgotten`,
  `TestARoomSomebodyMadeIsNeither`.
- Models: `TestAGuestsListDoesNotBindARoomTheyWereInvitedTo`, `TestTheInstallsListStillStopsATurn`.
- Authors: `TestAnEntryCarriesItsAuthor`, `TestThePrefixIsComposedNotStored`,
  `TestTheSpeakerIsTheAuthorNotTheText`, `TestAnAbortNamesWhoStoppedIt`.
- Drafts and landing: `TestADraftBelongsToOnePerson`, `TestMovingAConversationDoesNotDetachAnothers`.
- Forget and purge: `TestForgetKeepsTheFolder`, `TestPurgeNeedsTheRecord`,
  `TestPurgeDeletesTheStateToo`, `TestANewRoomCannotInheritAForgottenOne`,
  `TestPurgeRefusesEveryPersonalRoom`, `TestDoctorNamesAFolderWithNoRecord`.
- Migration: `TestAMembersListBecomesItsOwner`, `TestASecondAddressBecomesAGuestAndSaysSo`,
  `TestAOneMemberRoomBecomesPersonal`, `TestAMemberlessRoomIsTheAdmins`,
  `TestAPersonalRoomWithAGuestIsRefused`.
- The helper that answers every row of §4 joins `.mutationfloors`: a mutant in the permission table
  has to die, or the table is a comment.
