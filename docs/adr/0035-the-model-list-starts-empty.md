# ADR 0035 — The install's model list starts empty

Status: accepted (2026-10-31), built
Date: 2026-10-31

## Context

P38 made a provider's model list a property of the connection: one credentials file is spent by
everyone, so what the key may buy is the admin's decision. It defaulted the other way. A provider
with no entry allowed every model it had, and an empty list meant "everything". A new install could
run any model any connected provider offers before the admin had chosen anything, and the dev
provider `faux` was offered everywhere without being named — an exemption nobody had chosen and the
admin could not take away.

The list was a white list with a black-list default. The question "what may this install spend on"
was answered by absence, which is the one answer nobody typed.

## Decision

- **A model is allowed only when the install names it.** A provider with no entry allows none of its
  models; an empty list is none. The list is a white list in the literal sense.
- **A new install allows nothing.** The first model is chosen in **Settings → Providers → Models**,
  which reads the full catalog and the install's list, starts unticked, and offers **Select all** and
  **Clear**. Until a model is ticked the picker is empty and a turn is refused with the list — which
  is "none of them".
- **The provider allowlist binds everyone, including the admin.** A person's own list narrows on top
  of it; an empty person list means every model the install allows, which is where a person starts.
- **`faux` is a provider like any other.** The dev provider is selected in the same dialog as any
  other, and nothing grants it implicitly. The e2e harness grants it in its own data folder because
  every test in that suite speaks to it; that is a fixture, not a product exemption.
- **Every state frame carries the filtered list.** The first state, the stream, and the state a
  mutation answers with all ask the same question, so a phone cannot show the full catalog after the
  first frame.

## What it costs

- **A fresh install cannot run a turn** until the admin selects a model. That is the decision, and
  the Providers screen is the first stop.
- **An existing install that never wrote a list also starts with none.** The upgrade does not invent
  a list; the admin selects once, and the list is then a file the install keeps.
- **The dev provider is no longer ambient.** A demo, a test suite, or a fresh development install
  that wants it grants `faux/faux-1` explicitly.
- **The picker can be empty**, and an empty picker is correct rather than broken: the Providers
  screen is where it is filled.

## Alternatives

**Keep the default and make `faux` the only special case.** Rejected: an exemption is a model nobody
selected, and it leaves the question "what else is secretly allowed" open.

**Absent provider means all, empty list means none.** Rejected: two spellings of one question, and
the absent-provider case is still the one nobody typed.

**A global "allow every model" switch.** Rejected for now: the admin can Select all per provider, and
a second switch would be a second answer to the same question. It is a UI shortcut, not a policy.

## Not in this ADR

- **Per-person lists.** Their default is unchanged: empty means every model the install allows.
- **Spend caps.** Still deferred; the allowlist is a proxy, not a ceiling.
- **Provider credentials.** A provider with no key cannot run a model even if one is allowed; the
  list grants permission, not access.

## Verification

- The default: `TestAnInstallWithNoAllowlistOffersNothing`, `TestTheAdminIsOfferedWhatTheInstallAllows`,
  `TestAPersonWithNoListIsOfferedEverythingAllowed`, `TestAnInstallWithNoBookIsBoundedByTheAllowlist`.
- The binding: `TestTheAllowlistBindsTheAdmin`, `TestAModelOutsideTheAllowlistIsRefusedOnAssignment`,
  `TestATurnPinnedToARevokedModelIsRefused`, `TestAGuestsListDoesNotBindARoomTheyWereInvitedTo`.
- The state: `TestEveryStateFrameIsFiltered` reads the first frame off the stream and fails if the
  scripted provider is in it without being granted.
- The interface: `Settings/Provider models` `NothingYet`, `Narrowed`, and `AllAllowed`; the picker
  stories show an empty picker where the install allows nothing.
