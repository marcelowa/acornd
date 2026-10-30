# Features

What the platform does, stated as things that are observable from outside, and where each is
tested. It is the list to read before changing something: a feature here has a test that fails
when it stops working, and the test names are the map.

Each feature is covered at the lowest level that can decide it:

| Level | Command | What it can decide |
|---|---|---|
| Unit (Go) | `go test ./...` | Resolution, registries, lock order, arities. Milliseconds, no processes. |
| Unit (worker) | `bun test` (from `worker/`) | The overlay, guards, commands, skills, credentials, the worker protocol. Under a second. |
| Feature (end to end) | `go test ./internal/e2e/...` | A real gateway, a real worker, and a scripted provider over HTTP: everything a client can see. Resource tests run on the host engine; tests that need no engine run against the refusal path. |
| Sandbox engine boundary | `make test-sandbox` | The live driver, the worker SDK (exec streaming, kill, file operations), and the four end-to-end tests that exercise execution and the mounts. Needs a hypervisor (~35s). |
| Interface | `make ui-check` | The served interface, driven in a real browser — the suite starts a gateway for itself and stops it. The rail, the transcript, the splitter, per-pane controls, the theme, the phone drawer, and the secret field. `make ui` is the same suite against a gateway somebody already started. |

`make test` is the default gate: `-short -race`, the worker units, the Go units, and the
feature suite — resource tests on the host engine, tests that need no engine against the
refusal path — so the same command behaves the same on every machine, including CI
([ADR 0013](adr/0013-test-tiers.md)). It needs `bun` and `make deps`; without them the
end-to-end suite skips with a reason rather than failing. `make test-sandbox` is the hypervisor
tier — the microVM engine boundary — and fails rather than skips where there is no hypervisor.

## The scripted provider

Nothing here calls a model. `ACORNFALL_DEV_SCRIPT` points the worker at a JSON file of rules:
the first rule whose `match` is a substring of the last user message wins, its `tools` are
called, and its `reply` is the answer. That is what makes a feature driven by a tool call — a
guard blocking one, a subagent being spawned — testable offline and deterministically.

```
{"rules": [
  {"match": "delete the build", "tools": [{"name": "bash", "args": {"command": "rm -rf build"}}], "reply": "done"}
]}
```

`ACORNFALL_DEV_PROVIDER=1` is the older, hand-driven mode: one real `bash` call per turn and a
reply quoting the request. `make dev` uses it.

## Conversations

| # | Feature | Test |
|---|---|---|
| C1 | A fresh install has a root conversation to type into, and the state names it. | `TestAFreshInstallHasARootConversation` |
| C2 | Create, list, rename, hide, restore. | `TestConversationLifecycle` |
| C3 | A conversation is named after its first user message until it is named. | `TestConversationTitleComesFromTheFirstMessage` |
| C4 | Hiding is a tombstone: the transcript survives, and it can be restored. | `TestConversationLifecycle` |
| C5 | A fork branches *before* a user turn, keeps the history that led to it, and leaves the parent alone. | `TestForkBranchesBeforeATurn` |
| C6 | Conversation ids are the harness's own and are unique only within a workspace; a route always names the workspace. | `TestWorkspacesAreIsolatedAndStartOnDemand` |

## Turns and the transcript

| # | Feature | Test |
|---|---|---|
| T1 | A prompt produces a transcript: the message, the prompt that was sent, the answer, and the spend. | `TestPromptRoundTrip` |
| T2 | Tool calls run for real, and both the call and its output are recorded. | `TestAToolCallRunsAndIsRecorded` |
| T3 | Steering joins a running turn instead of starting a second one. | `TestSteeringJoinsTheRunningTurn` |
| T4 | Abort stops the run, and the conversation is usable afterwards — including when the command left a process in another session holding its output: the supervisor reaps and reports the exit, and the worker stops waiting after a grace rather than hanging the turn ([ADR 0029](adr/0029-a-run-ends-when-its-command-does.md)). | `TestAbortStopsTheRun`, `TestAKilledRunEndsEvenWhenAGrandchildHoldsThePipes`, `worker/test/bwrap.test.ts` |
| T5 | A conversation can choose its own model and thinking level. | `TestThinkingIsClampedButTheRequestIsRemembered` |
| T6 | A level the model cannot express is clamped, and what was asked for is kept and reported. | `TestThinkingIsClampedButTheRequestIsRemembered` + `gateway/internal/control/thinking_test.go` |
| T7 | Spend is reported per conversation. | `TestUsageIsReportedForAWorkspace` |
| T8 | A model with no credentials, or a definition that does not exist, is refused where it was chosen. | `TestUnusableChoicesAreRefused` |
| T9 | A manual compaction is accepted; on a short talk the transcript stands, and the summary mark lands only when there was something worth summarizing. | `TestCompactKeepsAShortTranscript` |
| T10 | The compact dialog states the scope before anything folds: turn and entry counts for the talk in view. | `TestCompactPreviewCountsTheTalk` |
| T11 | `/compact` and `/clear` are always on offer, need no file, and run actions instead of turns; the invocation is recorded as a user message after the act, so a reset cannot bury it. A file of the same name shadows its builtin. | `TestSlashCompactIsAccepted`, `TestSlashClearResetsTheContext` + `worker/test/commands.test.ts` |
| T12 | A builtin shows busy while it runs and answers when done: the gateway stamps the act like a prompt, a real compaction lands its summary card, and a short talk gets a `Nothing to compact` note that never joins the context. | `TestSlashCompactIsAccepted` (no-op note) |
| T13 | A real fold reports itself: a note with folded entries and turns, plus what the last reply read in provider tokens, with a percent of the window when the model is known. | `TestSlashCompactReportsTheFold` + `worker/test/commands.test.ts` |
| T14 | After an apply or rollback restarts the server, the client reloads for the new build once the stream names it. The update button disables with an `Updating…` label while the apply call is in flight, so a second tap has nothing to press. | `ui/test/update.test.ts` + `Settings/SettingsScreen/UpdatesApplying` |
| T15 | Files and images ride with a message: uploads stage under the workspace's hidden `.attachments` folder, the model gets image parts, inline text, or a sandbox path, and the transcript names every file. 10 MB per file, 20 files and 100 MB per message; the upload endpoint allows 12 MB while JSON stays at 1 MB. | `TestAttachmentsRideAlong`, `TestAttachmentsRefuseOversize` + `worker/test/attachments.test.ts` + `ui/test/attachments.test.ts` + `Conversation/Composer/WithAttachments` |
| T16 | Amber brand system: bronze primary, blue secondary, ember warning on warm bark neutrals, both themes. Dark-mode brand mark, theme-aware favicon. Stop is destructive, Steer primary, Queue secondary; dark text on fills in light, white in dark. | `bun run check:system` + `Foundations/Palette`, `Foundations/Colour`, `Screens/Workspace/Running` |
| T17 | A reply can wear a form before it explains itself: `> [!WARNING]` draws a tinted callout, `- [x]` a checklist with its count, and a marker with words on its line stays a quote. Both go through the same sanitiser as prose — the box is a span, never a control. | `ui/test/render-markdown.test.ts` + `Conversation/Markdown/CalloutsAndChecklists` + `make ui` |
| T18 | A reply can draw a plan: a `steps` fence renders a dot per step — done, doing (ringed), blocked, todo — beside the model's own words, and a line that names no state keeps its whole line. | `ui/test/render-markdown.test.ts` + `Conversation/Markdown/Steps` + `make ui` |
| T19 | A reply can draw numbers: a `chart` fence renders a bar per line, scaled to the longest, with each value beside its bar. A fence that does not read whole — a line that is not `name: number`, a count of more than 24 bars — stays a code block rather than dropping a line. | `ui/test/markdown-blocks.test.ts`, `ui/test/chart-source.test.ts` + `Conversation/Chart` + `make ui` |
| T20 | A reply can show a grid, its facts and its files: a `matrix` fence tints a cell per state (and refuses a grid that does not line up), a `stats` fence draws one tile per `name: value`, and a `tree` fence writes each shared directory once with its children under it. | `ui/test/render-markdown.test.ts` + `Conversation/Markdown/WithMatrixAndTree` + `make ui` |
| T21 | A reply can draw a shape: a `spark` fence turns three to sixty numbers into a line, with the first and the last value printed at its ends. A fence that is not all numbers stays a code block. | `ui/test/markdown-blocks.test.ts`, `ui/test/spark-source.test.ts` + `Conversation/Spark` + `make ui` |
| T22 | A half-typed message waits in the room it was written in: the workspace holds it, so a switch, a reload, and the other machine show the same words, and a message that was sent clears it everywhere. Staged files ride along; a file still uploading does not. | `TestADraftWaitsInTheRoomItWasWrittenIn`, `gateway/internal/control/draft_test.go` + `ui/test/draft.test.ts` + `make ui` |

## Defaults

The chain is **conversation → its definition → this workspace → the install → last resort**.

| # | Feature | Test |
|---|---|---|
| D1 | A workspace's defaults apply to conversations that never chose, and the state says which layer each came from. | `TestWorkspaceDefaultsApplyAndSayWhereTheyCameFrom` |
| D2 | An install default applies to every workspace, without a restart; a workspace's own default wins. | `TestInstallDefaultsApplyToEveryWorkspace` |
| D3 | An install default is one file, `~/.acornfall/settings.json`, and clearing it leaves no file. | `TestInstallDefaultsApplyToEveryWorkspace` |
| D4 | With nothing set anywhere, a preferred model runs rather than whichever is listed first, reported as the last resort. | `TestTheLastResortRunsWhenNothingIsSet` + `TestLastResortPrefersAnExplicitModel` |
| D5 | A layer that cannot be used — a model no longer offered, a definition that is gone — falls through and is *not* rewritten. | `TestDefaultsFallThroughLayers` |
| D6 | A resolved default is never persisted as this workspace's own choice. | `TestResolvedDefaultsAreNotPersistedAsThisWorkspace` |
| D7 | A workspace that has never saved settings still follows the install defaults. | `TestInstallDefaultsApplyToEveryWorkspace` |

## Workspaces

| # | Feature | Test |
|---|---|---|
| W1 | A workspace is a folder with its own harness, storage, and history; a second one is created by name and starts on demand. | `TestWorkspacesAreIsolatedAndStartOnDemand` |
| W2 | The interface creates workspaces inside one folder: a path is refused, not ignored. An existing folder of that name is adopted. | `TestWorkspaceCreationTakesANameAndRefusesAPath` |
| W3 | A workspace's own rules, guards, agents, and commands are read. The `trusted` flag that used to gate this is gone, and the API refuses it rather than ignoring it. | `TestAWorkspacesOwnResourcesAreRead`, `TestTheTrustFlagIsGone` |
| W4 | Removing forgets a workspace and stops its worker; it never deletes the folder or the history, and general cannot be removed. | `TestRemovingAWorkspaceKeepsItsFolderAndHistory` |
| W5 | A broken resource file is reported by that workspace, not fatal to the gateway, and fixing the file is enough. | `TestABrokenWorkspaceFileIsReportedAndHeals` |
| W6 | Idle workers are evicted; a watched or busy one is not. | `TestEvictionStopsOnlyIdleUnwatchedWorkspaces` |
| W7 | The tools can run on the host engine, chosen explicitly, and the state says it is not isolated. It is never selected by a missing hypervisor, and an unknown engine is refused with the list. | `TestWorkspaceRulesReachThePrompt`, `TestTheHostEngineNeedsNothing`, `TestIsolationSaysWhichEngineSeparatesFromTheHost`, `TestEngineRefusesANameItDoesNotImplement`, `worker/test/host.test.ts` |
| W8 | A tool call against a sandbox the engine stopped for being idle wakes it and runs, instead of failing or reporting files as missing. A bwrap sandbox is not stopped under a live worker at all: the worker says it is still there, and the sandbox's own timer only fires when the worker and the gateway are both gone ([ADR 0028](adr/0028-a-sandbox-stays-while-its-worker-does.md)). | `worker/test/microsandbox.test.ts` + `worker/test/sandbox.integration.test.ts` (real stop under a live client); `TestAKeepaliveKeepsTheSandboxAwake`, `TestAPingDoesNotKeepTheSandboxAwake`, `worker/test/bwrap.test.ts` |
| W9 | The worker refuses a loaded SDK that is not the pinned engine, and doctor reports a store the pinned engine cannot read — so no side migrates the shared catalog past the other. | `worker/test/microsandbox.test.ts` + `TestVerifyReadsTheStoreThroughThePinnedEngine` + house-rules pin check |
| W10 | Egress is on or off, and a workspace that may reach private networks has a grant: off means no network at all, on means public and DNS, and private only with the grant. The policy is written into the engine's own arguments on both engines — never inherited from an engine default. | `TestTheEgressPolicyIsWrittenForEveryCase`, `TestABwrapSandboxCannotReachPrivateNetworks`, `TestAMicroVMSandboxCannotReachPrivateNetworks` |
| W11 | A workspace with the grant reaches this machine's private networks, on bwrap and inside a microVM. | `TestABwrapAGrantReachesPrivateNetworks`, `TestAMicroVMSandboxWithTheGrantReachesPrivateNetworks` |
| W12 | Changing the grant rebuilds the sandbox rather than restarting it: the network is what the guest booted with, so stopping is not enough. | `TestAWorkspaceUpdateRestartsWhatDependsOnTheFolder` |
| W13 | A bwrap workspace receives the host's `/dev/net/tun` and `/dev/kvm`, and the capabilities nested pasta needs, by default; a workspace can withdraw them, and a missing device is skipped rather than failing the boot. | `TestBwrapBindsDevicesAndGrantsNestedEnginesTheCaps`, `TestBwrapClaimsNestingWhenKVMIsBound`, `TestDevicesAreOnByDefaultAndTheWorkspaceCanWithdrawThem`, `TestAWorkspaceUpdateRebuildsTheSandboxWhenDevicesChange` |
| W14 | A bwrap engine inside another bwrap workspace reuses pasta's user namespace for a network-on sandbox instead of creating a third one; the inner network is its own, and pasta's printed user-namespace failure is a refusal even when it exits 0. | `TestBwrapNestedEgressReusesPastaUserNamespace`, `TestBwrapConfigRecordsNestedFromTheParent`, `TestAPastaThatCannotMapItsUserNamespaceIsRefused` |
| W15 | Removing a sandbox stops it before deleting its files; a test that removes a started sandbox leaves no process. | `TestBwrapHasALifecycleLikeEveryOtherEngine` |
| W16 | A real microVM boots inside a bwrap workspace with the granted `/dev/kvm`, `/dev/net/tun`, and caps; the host's microsandbox engine runs a command in it. | `TestAMicroVMStartsInsideABwrapSandbox` |
| W17 | An existing sandbox is never recreated, but its config is refreshed before the running check: an upgrade that changes a baked setting is written for the next start, and a stopped sandbox starts from the new config. A change the running argv cannot absorb — the secrets proxy's forward, its trust mount, or a new placeholder name — stops and starts the sandbox instead. | `TestProduceRefreshesTheConfigOfAnExistingSandbox`, `TestBwrapPrepareWritesTheCurrentConfig`, `TestProduceRestartsARunningSandboxWhoseBakedConfigChanged` |

## Resources: rules, agents, skills

| # | Feature | Test |
|---|---|---|
| R1 | `AGENTS.md` reaches the prompt as its own section. | `TestWorkspaceRulesReachThePrompt` |
| R2 | Definitions overlay by layer: a workspace definition shadows a global one of the same name, and the explain view says which file won. | `TestAgentDefinitionsOverlayByLayer` |
| R3 | Instructions concatenate global-then-workspace, a definition's model and level beat both, denials union, and a denial beats an allow. | `TestConfigLayersFoldIntoOneAgent` + `worker/test/config.test.ts` |
| R4 | Unknown frontmatter keys are reported, never silently dropped. | `worker/test/config.test.ts` |
| R5 | Reload re-reads files without restarting anything. | `TestReloadPicksUpANewDefinition` |
| R6 | Skills are offered by name and description only; the body loads when the model asks, and an agent that names skills offers only those. | `TestSkillsAreOfferedAndFilteredByAgent` + `worker/test/skills.test.ts` |
| R7 | Slash commands come from two layers, the workspace shadowing the install, and the listing says which file each came from. | `TestCommandsComeFromBothLayersAndTheWorkspaceWins` |
| R8 | Expansion happens server-side, so the transcript records what the model saw; arguments are appended when there is no placeholder. | `TestCommandExpansionIsWhatTheModelSees`, `TestCommandArgumentsAreAppendedWhenThereIsNoPlaceholder` + `worker/test/commands.test.ts` |
| R9 | A message that merely starts with a slash is not rewritten. | `TestAnUnknownCommandIsSentAsWritten` |
| R10 | A resource bundle is read from `.agents/` — the ecosystem's name, where the Agent Skills convention points — as well as `.acornfall/`, at the user level (`~/.agents`) as well as in the workspace. Ours wins when a folder has both. Prompt templates are read from `commands/` or the ecosystem's `prompts/`. | `TestTheEcosystemAgentsFolderIsRead`, `worker/test/{config,commands,skills}.test.ts` |
| R11 | The global resource folders are mounted into every sandbox at their own paths, read-only, so a skill's instructions can name a script and have it run. The install folder itself — credentials, token, state — is never mounted. | `TestAGlobalSkillsScriptRunsInsideTheWorkspace`, `TestGlobalPathsAreTheResourceFoldersAndNeverTheInstall`, `TestRenderMountsTheGlobalsReadOnly` |

## Guards

| # | Feature | Test |
|---|---|---|
| G1 | A guard blocks a tool call and tells the model why; the transcript records the call and the refusal. | `TestTrustDecidesWhetherAWorkspaceIsRead`, `worker/test/hooks.test.ts` |
| G2 | Guards accumulate across layers: one added anywhere holds everywhere. | `worker/test/config.test.ts`, `TestTrustDecidesWhetherAWorkspaceIsRead` |
| G3 | A guard that cannot be understood is an error where it is written, not a rule that quietly does nothing. | `worker/test/config.test.ts`, `TestABrokenWorkspaceFileIsReportedAndHeals` |
| G4 | Path confinement survives `..`, sibling prefixes, and the workspace directory itself. | `worker/test/hooks.test.ts` |

## Delegation

| # | Feature | Test |
|---|---|---|
| H1 | `delegate` hands a task to a subagent and returns its answer as the result of the call. | `TestDelegateWaitsForTheChildAndReturnsItsAnswer` |
| H2 | A peer is a normal conversation: it is listed, records who created it, and is not owned. | `TestAPeerIsANormalConversation` |
| H3 | The overlay decides what a conversation may do, and the policy is reported so a refusal can be explained. | `TestDelegationPolicyCanForbidSpawning` |
| H4 | `child`, `peer`, `maxChildren`, `maxDepth`, `maxPeers`, `maxWakes`, `address`, `maxPeersTotal` parse and override the defaults. | `worker/test/config.test.ts` |

## Liveness and the stream

| # | Feature | Test |
|---|---|---|
| L1 | A dead harness is restarted on its own, and the conversation, its history, and its spend survive. | `TestTheHarnessRestartsAndKeepsItsHistory` |
| L2 | A run interrupted by a crash finishes afterwards. | `TestAnInterruptedRunFinishesAfterARestart` |
| L3 | The stream carries a full state on attach and whenever it moves, whole transcripts as view frames, and a keepalive that arrives while nothing is happening. | `TestTheStreamSendsStateViewsAndKeepalives`, `TestTheStreamAnnouncesStateChanges` |
| L4 | A failure says what the harness said, not only that it exited. | `workerpool.TestLastWordsPrefersTheLineThatExplains` |
| L5 | A death is announced once, and only for the worker that is running: a notification from one that was already replaced changes nothing and starts nothing. | `TestOnlyTheCurrentWorkersDeathCounts`, `TestTheHarnessRestartsAndKeepsItsHistory` | `TestABrokenWorkspaceFileIsReportedAndHeals` (a failed start restarts exactly once), `TestTheHarnessRestartsAndKeepsItsHistory` |

## Credentials and access

| # | Feature | Test |
|---|---|---|
| P1 | Credentials live in the install folder (0600) and never inside a workspace. | `worker/test/skills.test.ts`, `TestWorkspacesAreIsolatedAndStartOnDemand` |
| P2 | The provider list and each model's thinking levels come from the harness. | `worker/test/protocol.test.ts`, `TestAFreshInstallHasARootConversation` |
| P3 | An install with no `ACORNFALL_IDENTITY` is the single admin ADR 0020 describes: every request is answered and no credential exists. Naming a door changes that for the run that named it. | `httpapi.TestNoDoorMeansTheConsoleIsOpen`, `TestADoorIsOffUnlessThisRunNamesOne` |
| P4 | A request a configured door does not prove is refused before any handler runs, with the reason its door wrote. Loopback is not a proof: the token file is. | `httpapi.TestARequestNoDoorProvedIsRefused`, `TestAProvenRequestReachesItsHandler`, `identity.TestALoopbackRequestWithoutProofIsRefused` |
| P14 | The token is a 0600 file in the install folder, minted on first use, read per request so a rotation takes effect at once, and read only from the `Authorization` header — never a query string. | `identity.TestTheTokenFileIsNotReadableByAnyoneElse`, `TestARotatedTokenIsTheOnlyOneThatWorks`, `TestTheTokenIsNeverReadFromTheQueryString`, `TestEnsureTokenMintsOnceAndKeepsIt` |
| P15 | A signed assertion is verified before anything is read out of it: RS256 only, signature against the publisher's key, audience, issuer, lifetime with a minute of leeway, and a present-and-false `email_verified` refused. The plaintext identity header is not evidence. | `identity.TestAValidAssertionNamesItsEmail`, `TestTheRefusals`, `TestThePlaintextIdentityHeaderIsNotEnough` |
| P16 | Keys are cached for an hour, refetched when an assertion names one this process has not seen, and a publisher outage falls back to what was cached; with nothing cached the request is refused rather than admitted. | `identity.TestAnUnknownKeyIDRefetches`, `TestAKnownKeySurvivesAPublisherOutage`, `TestKeysThatCannotBeFetchedFailClosed` |
| P17 | A state-changing request whose `Origin` is another site is refused, a read is not, and health answers without a proof. A console with a door listens on loopback only. | `httpapi.TestACrossSitePostIsRefused`, `TestASameSitePostIsAllowed`, `TestACrossSiteReadIsAllowed`, `TestHealthAnswersWithoutAProof`, `TestAConsoledWithADoorListensOnLoopbackOnly` |
| P5 | Credential reads hand back copies, `modify` returning nothing changes nothing, and `delete` is what removes. | `worker/test/skills.test.ts` |
| P6 | Tool secrets live in `<home>/secrets.json` (0600), global or per workspace; a workspace name wins, and every API response is metadata only. | `vault.TestStoreUpsertListAndPermissions`, `TestSecretsAreStoredAndListedWithoutValues` |
| P7 | The CLI adds, lists, rotates, and removes a secret with the value on stdin, never argv. | `TestSecretCLIWritesTheVault` |
| P8 | A microsandbox workspace receives each secret as the literal `$MSB_<NAME>`; the value stays host-side and is substituted only for the allowed hosts over HTTPS. A card refuses a substitute secret with no allowed host. | `TestPolicyRenderKeepsSecretValuesOut`, `TestSecretsReachTheSandboxWithoutTheirValues`, `TestACardRefusesASecretWithoutHosts` |
| P9 | An environment engine puts the value in the sandbox; `allowEnv: false` withholds it and the state says why. bwrap is no longer one: its secrets are substituted, see P13. | `TestHostEngineDeliversSecretsIntoTheWorkerEnvironment` |
| P10 | The agent can list secrets and ask for a value through a card; the value goes to the gateway, never the transcript or the model. | `TestTheConversationAsksForASecretWithoutShowingItsValue`, `TestTheConversationListsSecretsWithoutValues` |
| P11 | Rotating an existing name is live where the engine can; adding or removing one is stored, applied after the active run settles, and the control starts the continuation itself. | `TestSecretsChangedRotatesLive`, `TestSecretsReachTheSandboxWithoutTheirValues`, `TestHostEngineDeliversSecretsIntoTheWorkerEnvironment` |
| P13 | A bwrap workspace holds `$MSB_<NAME>` and its commands reach a proxy the gateway runs: the value is substituted only for the secret's allowed hosts and only in the locations the secret enables, a request anywhere else is refused with a name and a host rather than a value, a host no secret names is tunnelled rather than intercepted, a granted name that answers with an address inside the machine is refused, the connection goes to the address that was resolved, and a rotation is a grant swap under the running sandbox. The first proxy, the last one removed, or a new placeholder name restarts the sandbox and worker because those are baked into what they started with. The sandbox's own processes are started with three variables and `--clearenv`, and a command inherits a shell's handful rather than the gateway's environment. | `TestTheSandboxProcessGetsItsOwnEnvironment`, `TestTheSandboxArgvClearsTheEnvironmentFirst`, `TestBwrapSubstitutesSecretsThroughItsProxy`, `TestABwrapWorkspaceWithASecretsProxyCarriesOnePortAndItsCA`, `TestASubstituteEngineGetsAProxyForItsSecrets`, `TestAFirstProxyAndANewNameNeedARestartButARotationDoesNot`, `TestBwrapRestartsOnlyWhenTheProxyForwardChanges`, `TestABwrapWorkspaceSubstitutesASecretAtItsProxy`, `secretsproxy.TestAPlaceholderBecomesTheValueForAnAllowedHost`, `TestASecretSentToAHostItDoesNotNameIsRefused`, `TestGrantsCanBeSwappedUnderARunningProxy`, `TestASecretIsNotSentToAnAddressInsideTheMachine`, `worker/test/bwrap.test.ts` |
| P18 | An install with no door answers every request; one that names a publisher requires a proof, and the admin is proved by the token file, not by the address the request came from. | `identity.TestTheTokenDoorAnswersTheAdmin`, `TestALoopbackRequestWithoutProofIsRefused`, `httpapi.TestARequestNoDoorProvedIsRefused` |
| P19 | A signed assertion is verified before a claim is read: RS256 only, signature, audience, issuer, lifetime with a minute of leeway, and the plaintext identity header is not evidence. Keys are cached, refetched on an unknown key id, and a publisher outage uses what was cached. | `identity.TestTheRefusals`, `TestThePlaintextIdentityHeaderIsNotEnough`, `TestAnUnknownKeyIDRefetches`, `TestAKnownKeySurvivesAPublisherOutage` |
| P20 | Nobody the admin has not named reaches the process: `people.json` holds invited, active and revoked records, exactly one admin claimed from the command line, and an unknown address is refused with 403 before a handler runs. | `people.TestTheAdminIsClaimedOnce`, `TestClaimingSomebodyAlreadyInTheBookPromotesThem`, `TestAnAddressWrittenTwiceAnswersAsItsAdmin`, `TestAnAddedPersonIsInvitedUntilTheySignIn`, `httpapi.TestAnEmailTheAdminNeverNamedIsRefused`, `TestARevokedPersonIsRefused`, `TestTheAdminsAddressMayDoTheInstallsBusiness` |
| P21 | A state-changing request from another origin is refused, a read is not, health answers without a proof, and a console with a door listens on loopback only. | `httpapi.TestACrossSitePostIsRefused`, `TestACrossSiteReadIsAllowed`, `TestHealthAnswersWithoutAProof`, `TestAConsoledWithADoorListensOnLoopbackOnly` |
| P22 | A workspace belongs to its members, by address: a member may use it, a person who is not one is refused, and the refusal is the same 404 as "not there" so a status cannot be used to enumerate. The admin is not a member of anybody else's workspace. | `control.TestAMemberIsTheWholeOfWhatAWorkspaceAllows`, `TestARouteRefusesWithoutSayingWhichRefusalItIs`, `TestAListShowsAPersonOnlyTheirOwn` |
| P23 | A workspace that predates people is the admin's, recorded at boot: without it the first door would hide every existing workspace from the person at the keyboard. A workspace that already names somebody is left alone. | `control.TestAMemberlessRoomIsTheAdmins`, `TestAnInstallWithNoAdminKeepsBothShapesOfTheOwnRoom` |
| P24 | The install's own routes are the admin's — its workspaces' lifecycle, its credentials, its global secrets, its updates, its provider keys — and a person asking for one is refused with 403. | `httpapi.TestAnInstallRouteIsTheAdminsOnly` |
| P25 | "The workspace" is the caller's own: a person asking for no workspace by name lands on their personal one, made on that first request, named after their address, one member, and found rather than made again. Two people with the same name get two workspaces. | `control.TestAPersonsOwnWorkspaceIsNamedAfterThem`, `TestAPersonsOwnWorkspaceIsFoundRatherThanMadeAgain`, `TestASecondAnaGetsANumber`, `TestTheAdminStillLandsOnTheGeneralWorkspace` |
| P26 | The install's own workspace is called `personal`, not `general`: an install from before the rename moves the record and the whole state folder with it, write-ahead log and drafts included, and refuses rather than merging when the new folder is already taken. | `control.TestTheGeneralWorkspaceBecomesPersonal`, `TestARenameIsRefusedWhenTheNewFolderIsTaken`, `TestBothNamesInTheRegistryIsLeftAlone` |
| P27 | What a person may run is written down in the book (`provider/modelId`), enforced where a model is assigned — a conversation's agent and a workspace's default — with a refusal that names the models they do have. An empty list is every model: the admin's, and a person's until it is narrowed. | `people.TestAModelsListIsWhatTheyMayRun`, `TestTheAdminNarrowsWhatSomebodyMayRun`, `httpapi.TestAModelOutsideTheAllowlistIsRefusedOnAssignment` |
| P28 | Remote access is declared and reconciled: one apply makes the tunnel, points it at this console, and makes its proxied record, the one-time PIN, the group the book fills, the Access application and its policy, in that order; a second apply creates nothing, a half-finished one resumes, and a tunnel or application somebody made by hand is adopted rather than duplicated. Ids and the tunnel's own token are kept 0600 in `remote.json`. | `remote.TestTheFirstApplyCreatesEverything`, `TestASecondApplyCreatesNothing`, `TestTheTunnelIsPointedAtThisConsole`, `TestARecordPointingAtAnotherTunnelIsRepointed`, `TestAnApplyResumesFromTheIdsItWrote`, `TestWhatAlreadyExistsIsAdopted`, `TestATunnelThatIsNotOursIsLeftAlone`, `TestAFailureKeepsWhatItHadAlreadyWritten`, `TestARefusedCallNamesThePermissionItNeeded` |
| P29 | The connector is the install's business: the pinned `cloudflared` is hashed before it is run, a payload that is not the pinned binary is refused, a platform with no pin is refused without a fetch, and a supervisor restarts it with a backoff that grows while it keeps dying and resets after a run that lasted. The token travels in the environment, never in argv. | `cloudflared.TestAPayloadThatIsNotThePinnedBinaryIsRefused`, `TestAPlatformWithNoPinIsRefusedWithoutFetching`, `TestAnInstalledBinaryIsVerifiedRatherThanRefetched`, `TestTheSupervisorRestartsWithBackoffAndStops`, `TestALongRunResetsTheBackoff`, `TestTheTokenIsNotInTheCommandLine` || P12 | The secret value field is masked by default, reveals with a keyboard-operable eye without changing the value, and keeps the caret and selection across the switch. It is **not a password field** — masking is the browser's glyph masking where it has one and a drawn mask where it does not — because managers classify by the type and no opt-out beats it. | `make ui-check`, `ui/test/secret-input.test.ts` |
| P30 | The stream stays open across a restart: a socket the browser gave up on is replaced with a backoff, a socket that stopped pinging is declared dead even while it is open, and a stream that comes back refetches the state it missed so a reconnected page is never stale. | `stream.TestTheStream`, `the stream > replaces a socket the browser has given up on`, `declares a silent socket dead`, `catches up on the state it missed` |
| P31 | Remote access is reconciled at every start, not only by the command: the same apply runs with a budget, before the connector dials out, and declines in one line — with no door, no declared state, or no API token in the environment — without ever failing a boot. | `TestReconcileDoesNothingWithoutADoor`, `TestReconcileSaysItCannotWithoutAnAPIToken`, `TestReconcileDoesNothingBeforeAnApply`, `TestReconcileRefusesAnInstallWithNobodyInIt`, `TestSummariseSaysWhatChanged` |
| P32 | The door can be made from the console: the admin types a hostname, the zone and the account's API token, and the same apply the command runs happens behind a button — the token used for that press and never written down, the steps reported in the command's own words, and a refusal that keeps what it made. | `TestRemoteStatusSaysNothingIsConfiguredYet`, `TestRemoteApplyMakesWhatTheFormAskedFor`, `TestRemoteApplyReportsAFailedApplyWithItsSteps`, `TestRemoteApplyWithoutATokenCreatesNothing`, `TestRemoteApplyKeepsNoToken` |
| P33 | A person is offered only the models they may run: the state's providers are narrowed to their list before they leave the edge, the admin keeps everything, an empty list keeps everything, and an unreadable book narrows nothing — the refusal at assignment is still the control. | `TestAPersonIsOfferedOnlyTheModelsTheyMayRun`, `TestTheAdminIsOfferedEverything`, `TestAPersonWithNoListIsOfferedEverything`, `TestAnUnreadableBookLeavesTheListAlone`, `TestAnInstallWithNoBookOffersEverything` |
| P34 | A room with more than one member names its speaker before the model reads the turn; a personal room says nothing, a command is left exactly as typed, and a message of files alone carries no prefix. | `TestAPersonalRoomHasNoSpeaker`, `TestASharedRoomNamesANonOwner`, `TestADisplayNameFallsBackToTheAddress`, `TestAPersonalRoomAssemblesNoPrefix` |
| P35 | A workspace is somebody's own room by its stored kind, never by its membership: the install's own room and each person's lead the rail, are never shared and never forgotten, an update cannot make one un-forgettable, and a registry written under the old flag or the old membership rule still loads. | `TestARoomWithNoMembersIsNotAnybodysOwnUnlessItIsTheInstalls`, `TestTheWorkspaceListPutsPersonalFirstThenNames`, `TestAnUpdateCannotChangeWhoseRoomIsWhose`, `TestTheAdminsOwnRoomIsGivenItsMember`, `TestAnInstallWithNoAdminKeepsBothShapesOfTheOwnRoom`, `TestAGeneralKeyInAnOldRegistryIsIgnored` |
| P36 | The door can be checked from outside: the public hostname is fetched the way a browser with nobody signed in would, a redirect to the login domain is a guarded door, a 200 or the console's own 401 is the finding that looks like success, and a door without reachable keys is not guarded. Needs no API token. | `TestAGuardedDoorIsGuarded`, `TestAConsoleAnsweringDirectlyIsWorseThanADoorThatDoesNotOpen`, `TestTheConsolesOwn401IsRefusedAsAGuardedDoor`, `TestARedirectToAnotherLoginDomainIsRefused`, `TestADoorWithoutReachableKeysIsNotAGuardedDoor`, `TestWithoutAnIssuerACloudflareLoginIsEnough`, `TestTheDoorCheckNeedsNoApiToken`, `TestTheDoorCheckRefusesAnInstallWithNoDoor` |
| P37 | A file lock waits on a budget and then says who is holding it: the vault's readers and writers hold shared or exclusive locks on a file beside the store, a holder that never lets go is a refusal in two seconds rather than a hang, and a folder that vanished is reported instead of recreated. | `TestAHeldLockIsRefusedWithinTheBudget`, `TestAReadHoldsASharedLock` |
| P38 | What a provider's key may be spent on is a property of the connection: the admin sets a model list per provider, it binds everybody including the admin, a person's own list narrows on top of it, the picker carries only what is allowed, assignment is refused with the models that are, and every turn is checked against the model the conversation is pinned to. | `TestTheAdminSetsWhatAProviderMaySpendOn`, `TestAPersonCannotReadTheAllowlist`, `TestTheAllowlistBindsTheAdmin`, `TestARefusalNamesWhatTheProviderAllows`, `TestATurnPinnedToARevokedModelIsRefused`, `TestAPersonIsOfferedOnlyTheModelsTheyMayRun` |
| P39 | A message carries who wrote it — the address and the display name the gateway proved — and the speaker the model is told is stored as a name, never as text: the prefix is composed when the transcript is assembled for a request, so a guest typing another person's prefix is not believed and no name is read twice; an abort lands as a note naming who stopped it; a personal room names nobody; and an entry written before this shows no author rather than a guess. | `TestAnEntryCarriesItsAuthor`, `TestThePrefixIsComposedNotStored`, `TestTheSpeakerIsTheAuthorNotTheText`, `TestAnAbortNamesWhoStoppedIt`, `TestAPersonalRoomAssemblesNoPrefix`, `TestAPersonalRoomHasNoSpeaker`, `TestASharedRoomNamesANonOwner`, `TestNobodyToNameIsNoAuthor`, `TestTheCommandCarriesTheAuthor` |
| P40 | A draft and a landing belong to one person: a draft is keyed by conversation and address, so sending clears only the sender's words and their own draft follows them to another machine; where a person lands is remembered per person beside the workspace's fallback for a browser that has never chosen; and selecting no longer detaches the worker, so moving a conversation does not pull another tab's live view out from under it. | `TestADraftBelongsToOnePerson`, `TestADraftOutlivesTheProcess`, `TestMovingAConversationDoesNotDetachAnothers`, `TestTheLandingIsEachPersons`, `TestALandingOutlivesTheProcess` |
| P41 | A room's record says who is in it and what they are: exactly one owner and its guests, and a kind that says whether the room is somebody's own. A room is born with its creator as its owner, so a room made from the console is one its maker can open; a personal room refuses a guest and refuses to be forgotten, and an update can rewrite neither the members nor the kind. | `TestARoomIsBornWithItsOwner`, `TestARoomHasExactlyOneOwner`, `TestAPersonalRoomRefusesAGuest`, `TestAPersonalRoomCannotBeForgotten`, `TestAnUpdateCannotChangeWhoseRoomIsWhose`, `TestARoomWithNoMembersIsNotAnybodysOwnUnlessItIsTheInstalls` |
| P42 | A registry written before rooms had roles is migrated at load, in one pass and one write: an address list becomes its owner and a kind, one address with no roles is somebody's own room, a hand-edited pair is read as owner-then-guest with one log line saying it guessed, a room nobody is named in is the admin's, and a personal room that names a guest is refused with the file named. | `TestAMembersListBecomesItsOwner`, `TestAOneMemberRoomBecomesPersonal`, `TestASecondAddressBecomesAGuestAndSaysSo`, `TestAMemberlessRoomIsTheAdmins`, `TestTheAdminsOwnRoomIsGivenItsMember`, `TestAnInstallWithNoAdminKeepsBothShapesOfTheOwnRoom`, `TestAPersonalRoomWithAGuestIsRefused`, `TestAGeneralKeyInAnOldRegistryIsIgnored` |
| P43 | Forgetting a room unregisters it — gone from every rail, refused for every caller, its folder and history left exactly where they were — and `acornfall workspace purge <name>` is the only thing that deletes: it prints both folders, needs `--yes`, deletes the workspace's folder and its state, refuses a name the registry does not hold, and refuses every personal room. A name whose state is still there cannot be made again until it is purged, and `acornfall doctor` names every folder under the workspaces folder with no record. | `TestForgetKeepsTheFolder`, `TestPurgeNeedsTheRecord`, `TestPurgeDeletesTheStateToo`, `TestANewRoomCannotInheritAForgottenOne`, `TestPurgeRefusesEveryPersonalRoom`, `TestDoctorNamesAFolderWithNoRecord` |
| P44 | A room's owner invites somebody the book already names, removes a guest, and a guest may leave; a room cannot name an address the door will refuse, and the install's admin is not a member of a room they were not invited to. Forgetting a room is its owner's on the console, and a personal room is never shared. | `TestOnlyTheRoomsOwnerInvites`, `TestAGuestLeavesAndTheOwnerRemoves`, `TestForgetIsTheRoomsOwnersAndPersonalIsNeverShared`, `TestOnlyTheOwnerInvitesAndRemoves`, `TestAPersonalRoomRefusesAGuestThroughTheDoor` |
| P45 | A guest prompts, steers, aborts and compacts, and cannot create, fork, hide, rename, reset or choose a model; the table is one method asked by the routes and by command expansion, so `/clear` cannot slip past the rule that lets a guest compact. A turn is checked against the install's provider list and not the caller's, so a guest speaks in a room whose model a member pinned, and the install still stops a revoked model. | `TestMayIsTheRoomsTable`, `TestAGuestMayCompactButMayNotReset`, `TestAGuestsListDoesNotBindARoomTheyWereInvitedTo` |
| P46 | The book has routes for the screen that manages it: the install's admin lists, invites, revokes, narrows models and sets `can_create`, and every mutation answers with the whole book so the screen never shows a state between a write and a read; a person asking for any of them is refused. A new member may make rooms until the admin says otherwise, and a member whose record allows it creates a room they own. | `TestTheAdminManagesTheBook`, `TestAPersonCannotReadTheBook`, `TestOnlyAMemberWhoseRecordAllowsItMayMakeARoom`, `TestANewMemberMayMakeRoomsUntilTheAdminSaysNo` |
| P47 | The state says what the caller may change — create, fork, hide, rename, model, administer — and what the install lets them do — be its admin, lead a room of their own — so the interface disables a control instead of inferring a role and deciding the same question again. The install's answers come from the book at the edge, which is the only layer that can read both it and the room. | `TestTheStateSaysWhatACallerMayChange`, `TestTheStateSaysWhoMayMakeRooms` |

## The worker protocol

The seam a second harness implements (`docs/adr/0002-layers-and-protocol.md`), tested without a
gateway in the way: `worker/test/protocol.test.ts`.

| # | Feature |
|---|---|
| X1 | Commands are answered by id, and a failure is a response rather than the end of the stream. |
| X2 | `describe` reports the harness, its working directory, providers, and capabilities. |
| X3 | Conversations can be created, listed, opened, renamed, hidden, and forked. |
| X4 | A prompt streams transcripts as whole views and reports submissions. |
| X5 | `expand` rewrites a command into what the model will see. |
| X6 | `policy`, `usage`, `commands`, and `agents` answer with what the interface explains. |

## Fixed by writing these tests

Eleven things were broken, and the tests are what found them — every one while writing a test
for something else. Each has its own test now, and the fix is in the history rather than a
workaround:

1. A conversation could be bound to a model with no credentials, or to a definition that does
   not exist, and failed later as a provider error.
2. Closing a worker did not stop its supervisor, so a workspace whose record changed (trust,
   folder) came back with two harnesses over one storage and the settings that had just been
   replaced.
3. A typo in a workspace's config took the whole gateway down, and the reason was only in the
   log: the interface could not say what was wrong.
4. Recovery created a conversation without listing first, so it made a second one and landed on
   the copy; and it skipped the settings and last-resort model a first start performs, so a
   workspace whose first start failed answered nothing.
5. A workspace that had never saved settings never read the install defaults.
6. Repairing a registry hand-edited into two general workspaces kept "the first" — but the
   registry is a map, so which one survived was whatever the iteration produced, and the same
   file could open differently on two runs.
7. `Close` stopped a workspace's worker and left `workerUp` true, so a list built after a
   shutdown said every workspace was still running.
8. A message that was **only files** reached the worker without `text` (the field is `omitempty`
   on the wire) and failed the whole send on `text.trim()`.
9. The draft endpoint answered with the draft **and then the state**: two JSON documents on one
   response, which no client can parse.
10. A microVM workspace's private-networks **grant** was a no-op the engine ignored, while the
    dialog said it was allowed — and the refusal that did work came from an engine default
    rather than from us.
11. The secret field was a password field, so Bitwarden kept offering to save a tool secret as a
    login, and no opt-out attribute beats the type.
12. `Bwrap.Remove` deleted a running sandbox's files and left its supervisor, bubblewrap, and
    commands behind; the lifecycle test passed because `List` only reads configs. Two test runs
    were still alive on a real machine when the leak was found.
13. Nested pasta printed `Couldn't configure user mappings` and exited 0, so the probe called a
    network that never existed healthy — and the inner acornfall would have chosen bwrap and lost
    its egress.
14. A binary upgrade adopted a running bwrap sandbox and never wrote the new config: the only
    way to get the device grant was to remove the sandbox by hand. `Produce` now writes the
    config before the running check.

## Not covered, and why

- **Real providers.** Every test uses the scripted one. A provider is a network and someone
  else's availability; what matters on our side is that the binding, the clamping, and the
  refusal are right, and those are covered.
- **The model's judgement.** Nothing here asserts that an agent does something sensible. The
  platform's job is to make the call and record it.
- **The sandbox needs a hypervisor.** One microVM per workspace runs commands and file
  operations, with the workspace mounted at its own path and the global resource folders
  read-only at theirs ([ADR 0012](adr/0012-sandbox.md),
  [ADR 0014](adr/0014-global-resources-in-the-sandbox.md)).
  Everything that can be tested without one is: the environment, the path routing, and the
  refusal run against a fake sandbox, and the policy and the lifecycle against a fake engine.
  The end-to-end tests that use a real workspace — its `AGENTS.md`, `.agents/`, skills,
  commands, guards — run in `make test`: the resource features on the host engine, which needs
  no hypervisor, and the four that exercise the microVM boundary on `make test-sandbox`, which
  fails rather than skips where there is no hypervisor. The worker's file operations and
  streaming execution are exercised against the real SDK there too
  ([ADR 0013](adr/0013-test-tiers.md), [ADR 0015](adr/0015-host-engine.md)).
- **Memory and self-improvement.** Designed, not built (see `docs/adr/`).
- **The interface.** `make ui-check` drives the interface the gateway serves: the rail and the
  transcript, the resizable splitter that remembers its width, per-pane format toggles and
  copy, the theme toggle, and the phone drawer. The inspector and the settings sheet are
  still being rebuilt (#13), which is why their assertions are not in the suite yet.
- **The microVM inside a bwrap workspace** is covered by the sandbox tier: the host's
  microsandbox engine boots a real microVM inside the bwrap, and the test runs in
  `make test-sandbox`, not in the fast suite.
