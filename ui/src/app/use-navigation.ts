import {
	useCallback,
	useEffect,
	useRef,
	useState,
	type Dispatch,
	type SetStateAction,
} from "react";

export interface Route {
	/** Which of the interface's screens is in view; the conversation is the one behind them. */
	screen: Screen;
	workspace: string;
	conversation: string;
	/** Which settings page is open. Only the settings screen reads it. */
	settingsTab: SettingsTab;
	/** Which release the update page is on; empty means the newest. */
	updateTag: string;
}

/** The screens the URL can name. A conversation is one of them, because it has a URL too. The
 * workspaces list is not: it is a layer over the conversation, and it keeps no URL of its own
 * (step 06), so no address can land on it. */
export type Screen = "conversation" | "settings" | "update";

/** The settings pages, each with its own URL under /settings. `general` is the install-wide
 * page: what runs and how it keeps itself current, the defaults a new conversation starts from,
 * and the theme. The old `defaults`, `updates` and `install` pages kept no redirect — the
 * release page has its own address now, and an unknown page lands on general. */
export type SettingsTab = "general" | "providers" | "secrets" | "people" | "remote";

export const SETTINGS_TABS: SettingsTab[] = ["general", "providers", "secrets", "people", "remote"];

/**
 * Whether a string names a settings page this build has. It is the one list: the URL parser and the tab bar
 * both ask it, so a page added here cannot be drawn by one and refused by the other.
 */
export function isSettingsTab(value: string | undefined): value is SettingsTab {
	return value !== undefined && (SETTINGS_TABS as string[]).includes(value);
}

function settingsTabFrom(segment: string | undefined): SettingsTab {
	return isSettingsTab(segment) ? segment : "general";
}

/** The URL scheme the gateway routes: /c/<workspace>/<conversation>, /w/<workspace>, the
 * settings screen, and one page per settings tab. Settings remembers the workspace in a query
 * so a reload shows the one it was about. `/workspaces` is no longer an address: it was the
 * list's own page, and the list is a layer now, so an old link lands on the conversation
 * instead of on nothing. */
export function routeFromUrl(path: string, search: string): Route {
	const settings = /^\/settings(?:\/([^/]+))?\/?$/.exec(path);
	if (settings !== null) {
		return {
			screen: "settings",
			workspace: new URLSearchParams(search).get("workspace") ?? "",
			conversation: "",
			settingsTab: settingsTabFrom(settings[1]),
			updateTag: "",
		};
	}
	// The release page: /update is the newest release, /update/<tag> a version page.
	const update = /^\/update(?:\/([^/]+))?\/?$/.exec(path);
	if (update !== null) {
		return {
			screen: "update",
			workspace: new URLSearchParams(search).get("workspace") ?? "",
			conversation: "",
			settingsTab: "general",
			updateTag: update[1] === undefined ? "" : decodeURIComponent(update[1]),
		};
	}
	const conversation = /^\/c\/([^/]+)\/([^/]+)\/?$/.exec(path);
	if (conversation !== null) {
		return {
			screen: "conversation",
			workspace: decodeURIComponent(conversation[1] ?? ""),
			conversation: decodeURIComponent(conversation[2] ?? ""),
			settingsTab: "general",
			updateTag: "",
		};
	}
	const workspace = /^\/w\/([^/]+)\/?$/.exec(path);
	if (workspace !== null) {
		return {
			screen: "conversation",
			workspace: decodeURIComponent(workspace[1] ?? ""),
			conversation: "",
			settingsTab: "general",
			updateTag: "",
		};
	}
	return {
		screen: "conversation",
		workspace: "",
		conversation: "",
		settingsTab: "general",
		updateTag: "",
	};
}

function fromUrl(): Route {
	return routeFromUrl(window.location.pathname, window.location.search);
}

/** Where the person is: the screen, its address, the workspaces layer and the rail. */
export interface NavigationSlice {
	/** The screen in view: a conversation or settings. The workspaces list is a level of the rail
	 * over the conversations, not a screen (`workspacesOpen`). */
	screen: Screen;
	showScreen: (screen: "conversation" | "settings") => void;
	/** The workspaces list, the level above the conversations in the rail: it keeps no URL, so Back
	 * goes back to the conversations instead of leaving the console. The rail stays where it is. */
	workspacesOpen: boolean;
	openWorkspaces: () => void;
	closeWorkspaces: () => void;
	/** The settings page in view, with its own URL. */
	settingsTab: SettingsTab;
	/** Which release the update page is on, from the URL; empty means the newest. */
	updateTag: string;
	showSettingsTab: (tab: SettingsTab) => void;
	/** The release page, at its own URL, optionally on one version. */
	showUpdate: (tag?: string) => void;
	railOpen: boolean;
	setRailOpen: (open: boolean) => void;
}

/** The navigation state, for the hooks that move it: a press that opens a conversation or a
 * workspace also sets the route and puts the rail away. */
export interface Navigation {
	route: Route;
	setRoute: Dispatch<SetStateAction<Route>>;
	railOpen: boolean;
	setRailOpen: Dispatch<SetStateAction<boolean>>;
	workspacesOpen: boolean;
	setWorkspacesOpen: Dispatch<SetStateAction<boolean>>;
}

/** The route from the address bar, the two layers over it, and Back. */
export function useNavigation(): Navigation {
	const [route, setRoute] = useState<Route>(fromUrl);
	const [railOpen, setRailOpen] = useState(false);
	/** The workspaces layer. It has no URL of its own, so it lives here and not in `route`; the
	 * entry a `pushState` leaves behind is what makes Back close it. */
	const [workspacesOpen, setWorkspacesOpen] = useState(false);

	const workspacesOpenRef = useRef(workspacesOpen);
	useEffect(() => {
		workspacesOpenRef.current = workspacesOpen;
	}, [workspacesOpen]);

	useEffect(() => {
		const back = (): void => {
			// The level has no address of its own, so Back is its own gesture: the entry it consumed
			// is the one it pushed when it opened, which carried the same address. Popping the level
			// is all that is left — the address, the rail and the conversation are where they were.
			if (workspacesOpenRef.current) {
				setWorkspacesOpen(false);
				return;
			}
			setRoute(fromUrl());
		};
		window.addEventListener("popstate", back);
		return () => window.removeEventListener("popstate", back);
	}, []);

	return { route, setRoute, railOpen, setRailOpen, workspacesOpen, setWorkspacesOpen };
}

/**
 * Which workspace and conversation a press is about. `workspace` is the route's, or the state's
 * when the route names none; `shown` is the state's own names, which are what the page shows
 * while a switch has not landed yet.
 */
export interface Here {
	workspace: string;
	shown: { workspace: string; conversation: string };
}

/** The presses that move between screens. Each screen keeps its own URL. */
export function useScreens(nav: Navigation, here: Here): NavigationSlice {
	const { route, setRoute, setRailOpen, setWorkspacesOpen } = nav;
	const { workspace } = here;
	const shownWorkspace = here.shown.workspace;
	const shownConversation = here.shown.conversation;

	/** The rail is the way between the screens; each one keeps its own URL so a reload and a
	 * shared link land where someone was. The workspaces list is not one of them: it is a level of
	 * the rail and keeps no address. */
	const showScreen = useCallback(
		(screen: "conversation" | "settings") => {
			if (screen === "conversation") {
				window.history.pushState(
					null,
					"",
					shownConversation === ""
						? `/w/${encodeURIComponent(shownWorkspace)}`
						: `/c/${encodeURIComponent(shownWorkspace)}/${encodeURIComponent(shownConversation)}`,
				);
			} else {
				// Settings are about a workspace, and each page has its own URL;
				// coming back lands on the page that was open.
				setRoute((current) => {
					window.history.pushState(
						null,
						"",
						`/settings/${current.settingsTab}?workspace=${encodeURIComponent(workspace)}`,
					);
					return { ...current, screen };
				});
				setRailOpen(false);
				return;
			}
			setRailOpen(false);
			// The conversation stays the one behind the screen: the stream keeps following it, so
			// coming back shows what happened while the screen was open.
			setRoute((current) => ({ ...current, screen }));
		},
		[shownWorkspace, shownConversation, workspace, setRoute, setRailOpen],
	);

	/** The workspace list is a level of the rail, not a layer beside it: opening it leaves the rail
	 * where it is — open on a desktop, the drawer on a phone — and fills it, so the X goes back to
	 * the conversations before it puts the rail away.
	 *
	 * It pushes its own history entry, with the address it already has, so a press of Back has
	 * something to consume: consuming it leaves the address unchanged and the conversation behind
	 * the rail never moves (J10). Without that entry Back would leave the console on a fresh tab. */
	const openWorkspaces = useCallback(() => {
		window.history.pushState(null, "", window.location.pathname + window.location.search);
		setWorkspacesOpen(true);
	}, [setWorkspacesOpen]);

	const closeWorkspaces = useCallback(() => setWorkspacesOpen(false), [setWorkspacesOpen]);

	/** One settings page to another: the URL moves so a reload stays where someone was. */
	const showSettingsTab = useCallback(
		(tab: SettingsTab) => {
			window.history.pushState(
				null,
				"",
				`/settings/${tab}?workspace=${encodeURIComponent(workspace)}`,
			);
			setRoute((current) => ({ ...current, screen: "settings", settingsTab: tab }));
		},
		[workspace, setRoute],
	);

	/** The release page: /update is the newest release, /update/<tag> a version page. It is one of
	 * the screens, so Back returns to whatever the press came from, and a shared link lands on the
	 * release it named. */
	const showUpdate = useCallback(
		(tag?: string) => {
			const tail = tag === undefined || tag === "" ? "" : `/${encodeURIComponent(tag)}`;
			window.history.pushState(
				null,
				"",
				`/update${tail}?workspace=${encodeURIComponent(workspace)}`,
			);
			setRoute((current) => ({ ...current, screen: "update", updateTag: tag ?? "" }));
			setRailOpen(false);
		},
		[workspace, setRoute, setRailOpen],
	);

	return {
		screen: route.screen,
		showScreen,
		workspacesOpen: nav.workspacesOpen,
		openWorkspaces,
		closeWorkspaces,
		settingsTab: route.settingsTab,
		updateTag: route.updateTag,
		showSettingsTab,
		showUpdate,
		railOpen: nav.railOpen,
		setRailOpen,
	};
}
