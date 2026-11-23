import { useCallback, useEffect, useMemo, useState } from "react";
import {
	addMember,
	createConversation as createConversationRequest,
	createWorkspace as createWorkspaceRequest,
	hideConversation,
	removeMember,
	removeWorkspace as removeWorkspaceRequest,
	renameConversation,
	updateWorkspace,
} from "./client";
import { toConversation, toEngines, toMembers, toSandbox, toWorkspaces } from "./map";
import type { Navigation } from "./use-navigation";
import { read } from "./use-preferences";
import type { Session } from "./use-session";
import type {
	ConversationSummary,
	EngineSummary,
	SandboxSummary,
	ShareView,
	WorkspaceSummary,
} from "./view-models";

const SEEN_KEY = "acornd.seen";

/** Sort key for the rail: newest first, undated last. */
function timeOf(updatedAt: Date | undefined): number {
	return updatedAt instanceof Date ? updatedAt.getTime() : Number.NEGATIVE_INFINITY;
}

/** What the rail lists: the workspaces, the room in view, and the conversations in it. */
export interface WorkspacesSlice {
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
	select: (id: string) => void;
	rename: (id: string, title: string) => void;
	hide: (id: string, hidden: boolean) => void;
}

/**
 * The workspaces and the conversations of the one in view. Opening either moves the route and
 * puts the rail away; `clearView` drops the transcript of the room being left.
 */
export function useWorkspaces(
	nav: Navigation,
	session: Session,
	clearView: () => void,
): WorkspacesSlice {
	const { setRoute, setRailOpen, setWorkspacesOpen } = nav;
	const { state, workspace, activeId, reportError, clearFailure, act, reload, setOpening } =
		session;
	const [seen, setSeen] = useState<Record<string, string>>(() => read(SEEN_KEY, {}));
	const [composerFocusToken, setComposerFocusToken] = useState(0);

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

	const { can } = session.slice;
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

	return {
		conversations,
		deleted,
		active:
			active === undefined ? undefined : toConversation(active, activeId, seen[active.id]),
		workspaces,
		share,
		addGuest,
		removeGuest,
		selectWorkspace,
		newConversation,
		composerFocusToken,
		sandbox,
		setWorkspaceSandbox,
		createWorkspace,
		engines,
		removeWorkspace,
		renameWorkspace,
		select,
		rename,
		hide,
	};
}
