import { useCallback, useEffect, useMemo, useState } from "react";
import {
	addMember,
	createConversation as createConversationRequest,
	createWorkspace as createWorkspaceRequest,
	fetchState,
	fetchWorkspaceStatus,
	hideConversation,
	openStream,
	removeMember,
	removeWorkspace as removeWorkspaceRequest,
	renameConversation,
	setAgent,
	setDefaultAgent,
	setDefaultModel,
	setDefaultThinking,
	updateWorkspace,
	type AgentChange,
	type DefaultScope,
} from "./client";
import {
	contextWindowFor,
	stalePins,
	toAgentGroups,
	toConversation,
	toEngines,
	toMembers,
	toModelChoices,
	toModelGroups,
	toSandbox,
	toScopeDefaults,
	toThinkingGroups,
	toWorkspaces,
} from "./map";
import type {
	ChoicePicker,
	ConversationSummary,
	EngineSummary,
	PickerGroup,
	SandboxSummary,
	ShareView,
	WorkspaceSummary,
} from "./view-models";
import { useNavigation, useScreens, type NavigationSlice } from "./use-navigation";
import { usePeople, type PeopleSlice } from "./use-people";
import { useConversation, type ConversationSlice } from "./use-conversation";
import { useDraft, type DraftSlice } from "./use-draft";
import { useProviders, type ProvidersSlice } from "./use-providers";
import { useRemote, type RemoteSlice } from "./use-remote";
import { useSession, type SessionSlice } from "./use-session";
import { useUpdate, type UpdateSlice } from "./use-update";
import { read, usePreferences, type PreferencesSlice } from "./use-preferences";
import type { WireResolvedAgent } from "./wire";

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
		DraftSlice,
		ConversationSlice {
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
	active?: ConversationSummary | undefined;
	/** The conversation in view was created by another one, so its choices are not its own. */
	subagent: boolean;
	/** Model, thinking, and agent as the conversation resolves them, with their layer. */
	model: ChoicePicker;
	thinking: ChoicePicker;
	agent: ChoicePicker;
	/** The running model's window, when the providers name it. */
	contextWindow?: number | undefined;
	resolved?: WireResolvedAgent | undefined;
	/** The level that was asked for when the model could not express it. */
	thinkingRequested?: string | undefined;
	select: (id: string) => void;
	rename: (id: string, title: string) => void;
	hide: (id: string, hidden: boolean) => void;
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
	const [seen, setSeen] = useState<Record<string, string>>(() => read(SEEN_KEY, {}));
	const [composerFocusToken, setComposerFocusToken] = useState(0);
	const drafts = useDraft(session);
	const { flushDrafts } = drafts;
	const conversation = useConversation(session, route);
	const { receiveView, receiveSecret, resetView, clearView } = conversation;

	useEffect(() => {
		let cancelled = false;
		// A stale transcript from the previous conversation is worse than an empty one.
		resetView();

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
				receiveSecret(event);
			},
			onView: (nextView, nextSenders) => {
				if (cancelled) return;
				receiveView(nextView, nextSenders);
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
		receiveAuth,
		resetView,
		receiveSecret,
		receiveView,
	]);

	// Reading a conversation marks it read, which is what clears the dot.
	useEffect(() => {
		const current = state?.conversations.find((conversation) => conversation.id === activeId);
		if (current === undefined || seen[current.id] === current.lastEntryId) return;
		const next = { ...seen, [current.id]: current.lastEntryId };
		setSeen(next);
		window.localStorage.setItem(SEEN_KEY, JSON.stringify(next));
	}, [state, activeId, seen]);

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
	/** A conversation belongs to a workspace for life, so switching means starting over: the
	 * transcript is cleared and the next one arrives with the workspace. */
	const selectWorkspace = useCallback(
		(name: string) => {
			window.history.pushState(null, "", `/w/${encodeURIComponent(name)}`);
			setRailOpen(false);
			// Opening a workspace leaves the layer: the row is the layer's own action.
			setWorkspacesOpen(false);
			clearView();
			setOpening(undefined);
			setRoute({
				screen: "conversation",
				workspace: name,
				conversation: "",
				settingsTab: "general",
				updateTag: "",
			});
		},
		[setRailOpen, setRoute, setWorkspacesOpen, setOpening, clearView],
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
				clearView();
				setRoute({
					screen: "conversation",
					workspace: name,
					conversation: "",
					settingsTab: "general",
					updateTag: "",
				});
			});
		},
		[act, setRoute, clearView],
	);

	const removeWorkspace = useCallback(
		(name: string) => {
			act(removeWorkspaceRequest(name), () => {
				// Forgetting the workspace in view leaves nowhere to stand, so the general one
				// takes over rather than an error about a workspace that just went away.
				if (name === workspace) {
					window.history.pushState(null, "", `/w/${name}`);
					clearView();
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
		[act, workspace, reload, setRoute, clearView],
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
				clearView();
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
	}, [workspace, reportError, setRailOpen, setRoute, clearFailure, clearView]);

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
		...conversation.slice,
		...navigation,
		...remote,
		...people,
		...update,
		conversations,
		deleted,
		active:
			active === undefined ? undefined : toConversation(active, activeId, seen[active.id]),
		subagent,
		model,
		thinking,
		agent,
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
		contextWindow,
		resolved: state?.resolved,
		thinkingRequested: state?.agent.thinkingRequested,
		newConversation,
		composerFocusToken,
		select,
		rename,
		hide,
		renameWorkspace,
	};
}
