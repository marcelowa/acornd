import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
	abortConversation,
	addMember,
	answerSecretRequest,
	cancelSecretRequest,
	compactConversation,
	createConversation as createConversationRequest,
	fetchCompactPreview,
	createWorkspace as createWorkspaceRequest,
	fetchCommands,
	fetchState,
	fetchWorkspaceStatus,
	forkConversation,
	hideConversation,
	openStream,
	removeMember,
	removeWorkspace as removeWorkspaceRequest,
	renameConversation,
	sendMessage,
	uploadAttachment,
	setAgent,
	setDefaultAgent,
	setDefaultModel,
	setDefaultThinking,
	type UploadedAttachment,
	updateWorkspace,
	type AgentChange,
	type CompactPreview,
	type DefaultScope,
	type SecretAnswerInput,
} from "./client";
import { foldContext, lastUsage, type ContextRequest } from "./context";
import {
	contextWindowFor,
	stalePins,
	textOf,
	toAgentGroups,
	toConversation,
	toEngines,
	toMembers,
	toModelChoices,
	toModelGroups,
	toSandbox,
	toScopeDefaults,
	toThinkingGroups,
	toTranscript,
	toWorkspaces,
} from "./map";
import type {
	BusyAction,
	ChoicePicker,
	ConversationSummary,
	EngineSummary,
	PickerGroup,
	SandboxSummary,
	ShareView,
	SlashCommand,
	TranscriptEntry,
	WorkspaceSummary,
} from "./view-models";
import { useNavigation, useScreens, type NavigationSlice } from "./use-navigation";
import { usePeople, type PeopleSlice } from "./use-people";
import { useDraft, type DraftSlice } from "./use-draft";
import { useProviders, type ProvidersSlice } from "./use-providers";
import { useRemote, type RemoteSlice } from "./use-remote";
import { useSession, type SessionSlice } from "./use-session";
import { useUpdate, type UpdateSlice } from "./use-update";
import { read, usePreferences, type PreferencesSlice } from "./use-preferences";
import type { WireResolvedAgent, WireSecretEvent, WireSender, WireUsage, WireView } from "./wire";

const SEEN_KEY = "acornd.seen";

/** How often a workspace that is opening is asked which step it is on. Fast enough that the line
 * changes when the work does, slow enough that it is one request a second. */
const openingPollMs = 700;

/** Sort key for the rail: newest first, undated last. */
function timeOf(updatedAt: Date | undefined): number {
	return updatedAt instanceof Date ? updatedAt.getTime() : Number.NEGATIVE_INFINITY;
}

/** The route's names, which components and the setup checklist read from here. */
export {
	isSettingsTab,
	routeFromUrl,
	SETTINGS_TABS,
	type Screen,
	type SettingsTab,
} from "./use-navigation";

/** A login in flight is the dialog's whole state, folded in `auth-flow.ts`. */
export type { AuthFlow } from "./auth-flow";

/** The book of people, as the People page reads it. */
export type { PeopleBook } from "./use-people";

export interface Platform
	extends
		PreferencesSlice,
		NavigationSlice,
		RemoteSlice,
		PeopleSlice,
		UpdateSlice,
		SessionSlice,
		ProvidersSlice,
		DraftSlice {
	conversations: ConversationSummary[];
	deleted: ConversationSummary[];
	/** The workspaces, with the one in view marked. */
	workspaces: WorkspaceSummary[];
	/** The room in view, when it can be shared: who is in it, and whether this caller runs it. */
	share?: ShareView | undefined;
	/** Put somebody the book names into a room, and take a guest out. Both are the room's owner's. */
	addGuest: (workspace: string, email: string) => void;
	removeGuest: (workspace: string, email: string) => void;
	/** A conversation belongs to a workspace for life, so switching means starting over: the
	 * transcript is cleared and the next one arrives with the workspace. */
	selectWorkspace: (name: string) => void;
	/** A new conversation in the workspace in view, created and opened. */
	newConversation: () => void;
	/** Each new conversation bumps it, so the composer can claim focus even where an
	 * arrival alone must not pop the keyboard. */
	composerFocusToken: number;
	/** Where the active workspace's tools actually run. */
	sandbox?: SandboxSummary | undefined;
	/** What each scope says, before resolution: one tab per scope in the settings screen. */
	defaults: {
		workspace: { model: string; thinking: string; agent: string };
		global: { model: string; thinking: string; agent: string };
	};
	/** Set a default, in this workspace or everywhere. */
	setDefault: (kind: "model" | "thinking" | "agent", scope: DefaultScope, value: string) => void;
	/** The name a `provider/modelId` is shown under, when the providers know it. */
	modelLabel: (value: string) => string;
	/** The levels a model offers, for the thinking default that follows it. */
	thinkingFor: (model: string) => PickerGroup[];
	/** Any workspace's image and network, by name: the workspaces screen's rows are not the one
	 * in view, and its menu is where a sandbox is changed. */
	setWorkspaceSandbox: (
		name: string,
		change: {
			image: string;
			network: string;
			keepAlive?: boolean;
			private?: boolean;
			disableDevices?: boolean;
		},
	) => void;
	/** A new workspace, on the engine chosen for it. */
	createWorkspace: (name: string, engine?: string) => void;
	/** The engines the create form offers, with this machine's answer for each. */
	engines: EngineSummary[];
	removeWorkspace: (name: string) => void;
	renameWorkspace: (name: string, title: string) => void;
	/** A tool's open secret request, shown as a card in the conversation. */
	secretRequest?: WireSecretEvent | undefined;
	/** Send the card's value to the gateway; the tool waiting on it gets only the outcome. */
	answerSecret: (requestId: string, input: SecretAnswerInput) => Promise<void>;
	/** Close the card without setting anything. */
	cancelSecret: (requestId: string) => Promise<void>;
	entries: TranscriptEntry[];
	active?: ConversationSummary | undefined;
	/** The conversation in view was created by another one, so its choices are not its own. */
	subagent: boolean;
	/** The worker can take a steering input mid-turn; without it the choice is stop or queue. */
	steerable: boolean;
	/** Model, thinking, and agent as the conversation resolves them, with their layer. */
	model: ChoicePicker;
	thinking: ChoicePicker;
	agent: ChoicePicker;
	/** The commands the server would expand, for the slash menu. */
	commands: SlashCommand[];
	/** What the model saw, request by request — the inspector's fold. */
	context: ContextRequest[];
	usage?: WireUsage | undefined;
	/** The running model's window, when the providers name it. */
	contextWindow?: number | undefined;
	resolved?: WireResolvedAgent | undefined;
	harness?: string | undefined;
	/** The level that was asked for when the model could not express it. */
	thinkingRequested?: string | undefined;
	select: (id: string) => void;
	rename: (id: string, title: string) => void;
	hide: (id: string, hidden: boolean) => void;
	fork: (at: string) => void;
	/** Start a manual compaction of the conversation in view. */
	compact: () => void;
	/** The compact dialog: open flag plus the counted scope. */
	compactOpen: boolean;
	compactPreview?: CompactPreview | undefined;
	openCompactDialog: () => void;
	closeCompactDialog: () => void;
	send: (text: string, whenBusy: BusyAction, attachments?: string[] | undefined) => Promise<void>;
	/** Stage files picked in the composer; ids ride with the next send. */
	upload: (file: File) => Promise<UploadedAttachment>;
	abort: () => void;
}

/**
 * The app layer: it owns the platform conversation — state, the stream, the actions, and the
 * two preferences that survive a reload — and hands the screens view models plus callbacks.
 * Nothing below this hook knows a URL, and nothing above it knows a wire type.
 */
export function usePlatform(): Platform {
	const preferences = usePreferences();
	const nav = useNavigation();
	const { route, setRoute, setRailOpen, setWorkspacesOpen } = nav;
	const remote = useRemote(route);
	const session = useSession(route);
	const {
		state,
		workspace,
		activeId,
		activeIdRef,
		reportError,
		clearFailure,
		act,
		reload,
		setState,
		setReady,
		setOpening,
		setConnection,
	} = session;
	const providers = useProviders(session);
	const { receiveAuth } = providers;
	const [secretRequest, setSecretRequest] = useState<WireSecretEvent | undefined>(undefined);
	const [view, setView] = useState<WireView | undefined>(undefined);
	const [senders, setSenders] = useState<Record<string, WireSender>>({});
	/**
	 * The steer-or-queued mark a message was sent with, per conversation and entry id. The wire
	 * keeps no link between an inbox item and the entry it becomes, so this is the interface's own
	 * memory: `onView` claims it as the message lands. See `docs/ui-workflow/refactor-ux/09-build.md`.
	 */
	const [marks, setMarks] = useState<Record<string, Record<string, BusyAction>>>({});
	const [seen, setSeen] = useState<Record<string, string>>(() => read(SEEN_KEY, {}));
	const [composerFocusToken, setComposerFocusToken] = useState(0);
	const [commands, setCommands] = useState<SlashCommand[]>([]);
	const drafts = useDraft(session);
	const { flushDrafts } = drafts;

	/** What is in the inbox and not committed yet, by its own words: how it was sent. */
	const sentMarks = useRef(new Map<string, BusyAction>());

	useEffect(() => {
		let cancelled = false;
		// A stale transcript from the previous conversation is worse than an empty one.
		setView(undefined);
		setSenders({});

		// A workspace that is not up yet is a state, not a wait: the status is cheap and says
		// which step it is on, so the pane can say "copying the image" instead of nothing while
		// the state request — which starts the workspace — would be blocking.
		const load = async (): Promise<void> => {
			try {
				const progress = await fetchWorkspaceStatus(workspace);
				if (cancelled) return;
				if (progress.status === "opening" || progress.status === "stopped") {
					setOpening({ stage: progress.stage ?? "" });
					if (progress.status === "stopped") {
						// A stopped workspace is one nobody has asked for yet, and the state request
						// is what starts it. It is sent now and its answer dropped: it only returns
						// when the workspace is up, and the poll above is what reports the steps
						// meanwhile.
						void fetchState(workspace, route.conversation).catch(() => undefined);
					}
					window.setTimeout(() => {
						if (!cancelled) void load();
					}, openingPollMs);
					return;
				}
				setOpening(undefined);
				if (progress.status === "failed") {
					reportError(progress.error ?? "the harness for this workspace did not start");
					return;
				}
				const next = await fetchState(workspace, route.conversation);
				if (cancelled) return;
				setState(next);
				setReady(true);
				clearFailure();
			} catch (cause) {
				if (!cancelled) reportError(cause);
			}
		};
		void load();

		const source = openStream(workspace, route.conversation, {
			onOpen: () => {
				if (!cancelled) setConnection("live");
				// The stream coming back is the moment a draft saved while it was away can land,
				// and the moment this client hears what moved on another machine.
				flushDrafts();
			},
			onState: (next) => {
				if (cancelled) return;
				// A state event can be a refusal with no workspace behind it: subscribing
				// to a conversation the worker no longer knows, for instance. That is a
				// failure to report, not a state to render — setting it would wipe the
				// transcript for an explanation nobody wrote.
				if (next.workspace === undefined) {
					reportError(
						(next as unknown as { error?: unknown }).error ??
							"the stream refused the conversation",
					);
					return;
				}
				setState(next);
			},
			onSecret: (event) => {
				if (cancelled) return;
				if (event.kind === "request") {
					setSecretRequest(event);
					return;
				}
				setSecretRequest((current) =>
					current?.requestId === event.requestId ? undefined : current,
				);
			},
			onView: (nextView, nextSenders) => {
				if (cancelled) return;
				// A message's steer-or-queued mark cannot come from the wire: an inbox item and the
				// entry it becomes are different records. So what was sent is remembered, and the
				// message that lands with the same words gets the mark — once, by entry id, so it
				// survives every later frame.
				const pending = sentMarks.current;
				for (const item of nextView.docs?.["pi.inbox"]?.items ?? []) {
					if (item.mode !== "steer" && item.mode !== "followUp") continue;
					const text = textOf(item.content).trim();
					if (text !== "") pending.set(text, item.mode);
				}
				const conversation = activeIdRef.current;
				const claims: Record<string, BusyAction> = {};
				for (const entry of nextView.entries ?? []) {
					// A person's message is placed as `acornd.message` (ADR 0034 §7); `pi.user` is an
					// agent's message to another conversation.
					if (entry.kind !== "pi.user" && entry.kind !== "acornd.message") continue;
					const text = (entry.model ?? [])
						.map((message) => textOf(message.content))
						.join("\n")
						.trim();
					const mode = pending.get(text);
					if (mode === undefined) continue;
					pending.delete(text);
					claims[String(entry.id)] = mode;
				}
				if (conversation !== undefined && Object.keys(claims).length > 0) {
					setMarks((current) => ({
						...current,
						[conversation]: { ...current[conversation], ...claims },
					}));
				}
				setView(nextView);
				setSenders(nextSenders);
			},
			onAuth: (id, event) => {
				if (cancelled) return;
				receiveAuth(id, event);
			},
			onReconnecting: () => {
				if (!cancelled) setConnection("reconnecting");
			},
		});

		return () => {
			cancelled = true;
			source.close();
		};
	}, [
		workspace,
		route.conversation,
		reportError,
		clearFailure,
		flushDrafts,
		setState,
		setReady,
		setOpening,
		setConnection,
		activeIdRef,
		receiveAuth,
	]);

	// Reading a conversation marks it read, which is what clears the dot.
	useEffect(() => {
		const current = state?.conversations.find((conversation) => conversation.id === activeId);
		if (current === undefined || seen[current.id] === current.lastEntryId) return;
		const next = { ...seen, [current.id]: current.lastEntryId };
		setSeen(next);
		window.localStorage.setItem(SEEN_KEY, JSON.stringify(next));
	}, [state, activeId, seen]);

	// The commands the server would expand. They are fetched per workspace, once: the list
	// changes when the workspace's resources change, not when a conversation does.
	useEffect(() => {
		let cancelled = false;
		const load = async (): Promise<void> => {
			try {
				const list = await fetchCommands(workspace);
				if (!cancelled) setCommands(list);
			} catch (cause) {
				if (!cancelled) reportError(cause);
			}
		};
		void load();
		return () => {
			cancelled = true;
		};
	}, [workspace, reportError]);

	const conversations = useMemo(
		() =>
			(state?.conversations ?? [])
				.filter((conversation) => !conversation.hidden)
				.map((conversation) =>
					toConversation(conversation, activeId, seen[conversation.id]),
				)
				.sort((a, b) => timeOf(b.updatedAt) - timeOf(a.updatedAt)),
		[state, activeId, seen],
	);

	const contextFold = useMemo(() => foldContext(view?.entries ?? []), [view]);

	const workspaces = useMemo(
		() =>
			toWorkspaces(
				state?.workspaces ?? [],
				state?.workspace?.name ?? "",
				// The install's default, so a workspace that chose nothing still says what it runs on.
				state?.engines?.find((engine) => engine.default === true)?.name ?? "",
			),
		[state],
	);
	const sandbox = useMemo(
		() => (state === undefined ? undefined : toSandbox(state.sandbox)),
		[state],
	);
	const engines = useMemo(() => toEngines(state?.engines), [state]);
	const deleted = useMemo(
		() =>
			(state?.conversations ?? [])
				.filter((conversation) => conversation.hidden)
				.map((conversation) =>
					toConversation(conversation, activeId, seen[conversation.id]),
				)
				.sort((a, b) => timeOf(b.updatedAt) - timeOf(a.updatedAt)),
		[state, activeId, seen],
	);
	const runningNow =
		(state?.conversations ?? []).find((conversation) => conversation.id === activeId)
			?.running === true;
	const entries = useMemo(
		() =>
			toTranscript(
				view,
				senders,
				runningNow,
				activeId === undefined ? undefined : marks[activeId],
			),
		[view, senders, runningNow, activeId, marks],
	);
	const active = useMemo(
		() => (state?.conversations ?? []).find((conversation) => conversation.id === activeId),
		[state, activeId],
	);

	// How a turn lands while one is running: the worker has to offer steering at all, and then
	// the choice is the person's, not a heuristic.
	const choose = useCallback(
		(change: AgentChange) => {
			void setAgent(workspace, activeId, change)
				.then((next) => {
					setState(next);
					clearFailure();
				})
				.catch(reportError);
		},
		[workspace, activeId, reportError, setState, clearFailure],
	);

	// The composer's three choices. `state.agent` is the effective binding, so a picker shows
	// what will actually run; `inherited` says whether this conversation chose it, and
	// `defaultsOrigin` names the layer when it did not.
	const modelChoices = useMemo(() => toModelChoices(state?.providers ?? []), [state]);
	const effectiveModel =
		state === undefined ? "" : `${state.agent.provider}/${state.agent.modelId}`;
	const contextWindow = useMemo(
		() => contextWindowFor(state?.providers ?? [], effectiveModel),
		[state, effectiveModel],
	);
	const thinkingLevels =
		modelChoices.find((choice) => choice.value === effectiveModel)?.thinkingLevels ?? [];
	const thinkingValue = state?.agent.thinkingLevel ?? "";
	const agentValue =
		state?.resolved?.name ??
		(state?.agent.definition !== undefined && state.agent.definition !== ""
			? state.agent.definition
			: (state?.defaultAgent ?? ""));

	// A pin can outlive what it names; the value is still shown — it is what the conversation
	// is pinned to — and it is marked when it cannot run here any more.
	const stale = state === undefined ? { model: false, agent: false } : stalePins(state);
	// A subagent runs what its creator gave it: the pickers show that rather than offering a
	// change the gateway would not apply.
	const subagent = state?.subagent === true;
	const can = session.slice.can;
	const fixed = activeId === "" || subagent || !can.model;
	// A personal room is never shared, on any install: journey 1 is "make a room and invite people", not
	// "open my room" (ADR 0034 §5). So the surface that shares one has nothing to say about it.
	const share = useMemo<ShareView | undefined>(() => {
		const room = state?.workspace;
		if (room === undefined || room.personal) return undefined;
		return {
			name: room.name,
			title: room.title,
			members: toMembers(room.members),
			administer: can.administer,
		};
	}, [state, can.administer]);
	const pinnedModel = modelChoices.find((choice) => choice.value === effectiveModel);

	/** Every picker can go back to inheriting: a choice has to be undoable. */
	const withDefault = (groups: PickerGroup[]): PickerGroup[] => [
		...groups,
		{ options: [{ value: "", label: "Use the default" }] },
	];

	const model: ChoicePicker = {
		value: pinnedModel?.label ?? state?.agent.modelId ?? "",
		invalid: stale.model,
		note: stale.model ? "not available here" : undefined,
		groups: withDefault(toModelGroups(state?.providers ?? [])),
		disabled: fixed || modelChoices.length === 0,
		onSelect: (value) => {
			if (value === "") {
				choose({ provider: "", modelId: "" });
				return;
			}
			const slash = value.indexOf("/");
			choose({ provider: value.slice(0, slash), modelId: value.slice(slash + 1) });
		},
	};

	const thinking: ChoicePicker = {
		value: thinkingValue,
		note:
			state !== undefined &&
			(state.agent.thinkingRequested ?? "") !== "" &&
			state.agent.thinkingRequested !== thinkingValue
				? `${state.agent.thinkingRequested ?? ""} was asked`
				: undefined,
		groups: withDefault(toThinkingGroups(thinkingLevels)),
		disabled: fixed || (thinkingLevels.length === 0 && thinkingValue === ""),
		onSelect: (value) => choose({ thinkingLevel: value }),
	};

	const agent: ChoicePicker = {
		value: agentValue,
		invalid: stale.agent,
		note: stale.agent ? "definition is gone" : subagent ? "set by its creator" : undefined,
		groups: withDefault(toAgentGroups(state?.agents ?? [])),
		disabled: fixed,
		onSelect: (value) => choose({ definition: value }),
	};

	const select = useCallback(
		(id: string) => {
			const name = state?.workspace?.name ?? workspace;
			window.history.pushState(
				null,
				"",
				`/c/${encodeURIComponent(name)}/${encodeURIComponent(id)}`,
			);
			setRailOpen(false);
			setRoute({
				screen: "conversation",
				workspace: name,
				conversation: id,
				settingsTab: "general",
				updateTag: "",
			});
		},
		[state, workspace, setRailOpen, setRoute],
	);

	const navigation = useScreens(nav, {
		workspace,
		shown: {
			workspace: state?.workspace?.name ?? workspace,
			conversation: state?.conversation?.id ?? activeId,
		},
	});

	const rename = useCallback(
		(id: string, title: string) => {
			act(renameConversation(workspace, id, title), () => void reload());
		},
		[act, workspace, reload],
	);
	const hide = useCallback(
		(id: string, hidden: boolean) => {
			act(hideConversation(workspace, id, hidden), () => void reload());
		},
		[act, workspace, reload],
	);
	const fork = useCallback(
		(at: string) => {
			act(forkConversation(workspace, activeId, at), () => void reload());
		},
		[act, workspace, activeId, reload],
	);
	const compact = useCallback(() => {
		act(compactConversation(workspace, activeId), () => void reload());
	}, [act, workspace, activeId, reload]);
	const [compactOpen, setCompactOpen] = useState(false);
	const [compactPreview, setCompactPreview] = useState<CompactPreview | undefined>(undefined);
	const openCompactDialog = useCallback(() => {
		setCompactOpen(true);
		setCompactPreview(undefined);
		fetchCompactPreview(workspace, activeId).then(
			(preview) => setCompactPreview(preview),
			(cause: unknown) => {
				setCompactOpen(false);
				reportError(cause);
			},
		);
	}, [workspace, activeId, reportError]);
	const closeCompactDialog = useCallback(() => {
		setCompactOpen(false);
		setCompactPreview(undefined);
	}, []);
	const send = useCallback(
		async (
			text: string,
			whenBusy: BusyAction,
			attachments?: string[] | undefined,
		): Promise<void> => {
			try {
				await sendMessage(workspace, activeId, text, whenBusy, attachments);
				clearFailure();
				// The send answers 202 with no state, so the busy sign would wait for the
				// next poll. Read it now: the prompt grace is already counting.
				void reload();
			} catch (cause) {
				// The caller restores the draft; the failure is also the strip's to show.
				reportError(cause);
				throw cause;
			}
		},
		[workspace, activeId, reload, reportError, clearFailure],
	);
	const upload = useCallback(
		(file: File): Promise<UploadedAttachment> => uploadAttachment(workspace, file),
		[workspace],
	);
	const abort = useCallback(() => {
		act(abortConversation(workspace, activeId));
	}, [act, workspace, activeId]);

	/** A conversation belongs to a workspace for life, so switching means starting over: the
	 * transcript is cleared and the next one arrives with the workspace. */
	const selectWorkspace = useCallback(
		(name: string) => {
			window.history.pushState(null, "", `/w/${encodeURIComponent(name)}`);
			setRailOpen(false);
			// Opening a workspace leaves the layer: the row is the layer's own action.
			setWorkspacesOpen(false);
			setView(undefined);
			setOpening(undefined);
			setRoute({
				screen: "conversation",
				workspace: name,
				conversation: "",
				settingsTab: "general",
				updateTag: "",
			});
		},
		[setRailOpen, setRoute, setWorkspacesOpen, setOpening],
	);

	// A row on the workspaces screen is not the workspace in view, so the name comes from the
	// caller: the gateway takes it either way.
	const setWorkspaceSandbox = useCallback(
		(
			name: string,
			change: {
				image: string;
				network: string;
				keepAlive?: boolean;
				private?: boolean;
				disableDevices?: boolean;
			},
		) => {
			act(updateWorkspace(name, change), () => void reload());
		},
		[act, reload],
	);

	const createWorkspace = useCallback(
		(name: string, engine?: string) => {
			act(createWorkspaceRequest(name, engine), () => {
				// The workspace opens on its first conversation, which the gateway makes for a
				// workspace that has none — so there is nothing to create here, and creating one
				// would leave the new workspace with two.
				window.history.pushState(null, "", `/w/${encodeURIComponent(name)}`);
				setView(undefined);
				setRoute({
					screen: "conversation",
					workspace: name,
					conversation: "",
					settingsTab: "general",
					updateTag: "",
				});
			});
		},
		[act, setRoute],
	);

	const removeWorkspace = useCallback(
		(name: string) => {
			act(removeWorkspaceRequest(name), () => {
				// Forgetting the workspace in view leaves nowhere to stand, so the general one
				// takes over rather than an error about a workspace that just went away.
				if (name === workspace) {
					window.history.pushState(null, "", `/w/${name}`);
					setView(undefined);
					setRoute({
						screen: "conversation",
						workspace: name,
						conversation: "",
						settingsTab: "general",
						updateTag: "",
					});
					return;
				}
				void reload();
			});
		},
		[act, workspace, reload, setRoute],
	);

	const renameWorkspace = useCallback(
		(name: string, title: string) => {
			act(updateWorkspace(name, { title }), () => void reload());
		},
		[act, reload],
	);

	// The room's members. Both answer the workspace listing, and a refusal — an address the book does not
	// hold says to ask the admin — lands in `failure`, which the dialog shows where the address was typed.
	const addGuest = useCallback(
		(name: string, email: string) => {
			act(addMember(name, email), () => void reload());
		},
		[act, reload],
	);
	const removeGuest = useCallback(
		(name: string, email: string) => {
			act(removeMember(name, email), () => void reload());
		},
		[act, reload],
	);

	const answerSecret = useCallback(
		async (requestId: string, input: SecretAnswerInput): Promise<void> => {
			const conversation = secretRequest?.conversationId ?? route.conversation;
			if (conversation === "") throw new Error("no conversation is open");
			await answerSecretRequest(workspace, conversation, requestId, input);
			setSecretRequest((current) => (current?.requestId === requestId ? undefined : current));
		},
		[workspace, route.conversation, secretRequest],
	);

	const cancelSecret = useCallback(
		async (requestId: string): Promise<void> => {
			const conversation = secretRequest?.conversationId ?? route.conversation;
			if (conversation === "") throw new Error("no conversation is open");
			await cancelSecretRequest(workspace, conversation, requestId);
			setSecretRequest((current) => (current?.requestId === requestId ? undefined : current));
		},
		[workspace, route.conversation, secretRequest],
	);

	const update = useUpdate(state?.update, act, reportError);

	const people = usePeople(route, session.slice.admin);

	const newConversation = useCallback(() => {
		// The drawer covers the conversation on a phone, and the new room is for typing:
		// close it now so it never lingers over the request, and again on arrival.
		setRailOpen(false);
		void createConversationRequest(workspace)
			.then((created) => {
				clearFailure();
				const id = created.conversation?.id ?? "";
				window.history.pushState(
					null,
					"",
					`/c/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}`,
				);
				setView(undefined);
				setRailOpen(false);
				setComposerFocusToken((token) => token + 1);
				setRoute({
					screen: "conversation",
					workspace,
					conversation: id,
					settingsTab: "general",
					updateTag: "",
				});
			})
			.catch(reportError);
	}, [workspace, reportError, setRailOpen, setRoute, clearFailure]);

	const setDefault = useCallback(
		(kind: "model" | "thinking" | "agent", scope: DefaultScope, value: string) => {
			const request =
				kind === "model"
					? setDefaultModel(workspace, scope, value)
					: kind === "thinking"
						? setDefaultThinking(workspace, scope, value)
						: setDefaultAgent(workspace, scope, value);
			act(request, () => void reload());
		},
		[act, workspace, reload],
	);

	const defaults = useMemo(
		() => ({
			workspace: toScopeDefaults(state?.workspaceDefaults),
			global: toScopeDefaults(state?.installDefaults),
		}),
		[state],
	);

	const modelLabel = useCallback(
		(value: string): string =>
			modelChoices.find((choice) => choice.value === value)?.label ??
			value.split("/").at(-1) ??
			"",
		[modelChoices],
	);

	const thinkingFor = useCallback(
		(model: string): PickerGroup[] =>
			toThinkingGroups(
				modelChoices.find((choice) => choice.value === model)?.thinkingLevels ?? [],
			),
		[modelChoices],
	);

	return {
		...preferences,
		...session.slice,
		...providers.slice,
		...drafts.slice,
		...navigation,
		...remote,
		...people,
		...update,
		conversations,
		deleted,
		entries,
		active:
			active === undefined ? undefined : toConversation(active, activeId, seen[active.id]),
		subagent,
		steerable: state?.capabilities.steer ?? false,
		model,
		thinking,
		agent,
		commands,
		context: contextFold,
		workspaces,
		share,
		addGuest,
		removeGuest,
		sandbox,
		selectWorkspace,
		defaults,
		setDefault,
		modelLabel,
		thinkingFor,
		setWorkspaceSandbox,
		createWorkspace,
		engines,
		removeWorkspace,
		usage: lastUsage(view?.entries ?? []),
		contextWindow,
		resolved: state?.resolved,
		thinkingRequested: state?.agent.thinkingRequested,
		harness: state?.harness,
		newConversation,
		composerFocusToken,
		select,
		rename,
		hide,
		fork,
		compact,
		compactOpen,
		compactPreview,
		openCompactDialog,
		closeCompactDialog,
		send,
		upload,
		abort,
		renameWorkspace,
		secretRequest,
		answerSecret,
		cancelSecret,
	};
}
