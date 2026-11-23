import { useEffect } from "react";
import { fetchState, fetchWorkspaceStatus, openStream } from "./client";
import { useNavigation, useScreens, type NavigationSlice } from "./use-navigation";
import { usePeople, type PeopleSlice } from "./use-people";
import { useWorkspaces, type WorkspacesSlice } from "./use-workspaces";
import { useChoices, type ChoicesSlice } from "./use-choices";
import { useConversation, type ConversationSlice } from "./use-conversation";
import { useDraft, type DraftSlice } from "./use-draft";
import { useProviders, type ProvidersSlice } from "./use-providers";
import { useRemote, type RemoteSlice } from "./use-remote";
import { useSession, type SessionSlice } from "./use-session";
import { useUpdate, type UpdateSlice } from "./use-update";
import { usePreferences, type PreferencesSlice } from "./use-preferences";

/** How often a workspace that is opening is asked which step it is on. Fast enough that the line
 * changes when the work does, slow enough that it is one request a second. */
const openingPollMs = 700;

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
		ConversationSlice,
		ChoicesSlice,
		WorkspacesSlice {}

/**
 * The app layer: it owns the platform conversation — state, the stream, the actions, and the
 * two preferences that survive a reload — and hands the screens view models plus callbacks.
 * Nothing below this hook knows a URL, and nothing above it knows a wire type.
 */
export function usePlatform(): Platform {
	const preferences = usePreferences();
	const nav = useNavigation();
	const { route } = nav;
	const remote = useRemote(route);
	const session = useSession(route);
	const {
		state,
		workspace,
		activeId,
		reportError,
		clearFailure,
		act,
		setState,
		setReady,
		setOpening,
		setConnection,
	} = session;
	const providers = useProviders(session);
	const { receiveAuth } = providers;
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

	const choices = useChoices(session);
	const rail = useWorkspaces(nav, session, clearView);
	const navigation = useScreens(nav, {
		workspace,
		shown: {
			workspace: state?.workspace?.name ?? workspace,
			conversation: state?.conversation?.id ?? activeId,
		},
	});

	const update = useUpdate(state?.update, act, reportError);

	const people = usePeople(route, session.slice.admin);

	return {
		...preferences,
		...session.slice,
		...providers.slice,
		...drafts.slice,
		...conversation.slice,
		...choices,
		...rail,
		...navigation,
		...remote,
		...people,
		...update,
	};
}
