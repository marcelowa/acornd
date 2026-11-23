import { useChoices, type ChoicesSlice } from "./use-choices";
import { useConversation, type ConversationSlice } from "./use-conversation";
import { useDraft, type DraftSlice } from "./use-draft";
import { useNavigation, useScreens, type NavigationSlice } from "./use-navigation";
import { usePeople, type PeopleSlice } from "./use-people";
import { usePreferences, type PreferencesSlice } from "./use-preferences";
import { useProviders, type ProvidersSlice } from "./use-providers";
import { useRemote, type RemoteSlice } from "./use-remote";
import { useSession, type SessionSlice } from "./use-session";
import { useStream } from "./use-stream";
import { useUpdate, type UpdateSlice } from "./use-update";
import { useWorkspaces, type WorkspacesSlice } from "./use-workspaces";

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
 * The app layer: the platform conversation — state, the stream, the actions, and the preferences
 * that survive a reload — handed to the screens as view models plus callbacks. Nothing below this
 * hook knows a URL, and nothing above it knows a wire type.
 *
 * It is a composition: each domain hook owns its state and hands back its slice, and `Platform`
 * is those slices together, so a screen and a story see one object as before.
 */
export function usePlatform(): Platform {
	const preferences = usePreferences();
	const nav = useNavigation();
	const session = useSession(nav.route);
	const providers = useProviders(session);
	const drafts = useDraft(session);
	const conversation = useConversation(session, nav.route);
	// The hooks a frame lands in come before the stream that feeds them.
	useStream(nav.route, session, {
		clearView: conversation.clearView,
		flushDrafts: drafts.flushDrafts,
		receiveView: conversation.receiveView,
		receiveSecret: conversation.receiveSecret,
		receiveAuth: providers.receiveAuth,
	});
	const choices = useChoices(session);
	const rail = useWorkspaces(nav, session, conversation.clearView);
	const navigation = useScreens(nav, {
		workspace: session.workspace,
		shown: {
			workspace: session.state?.workspace?.name ?? session.workspace,
			conversation: session.state?.conversation?.id ?? session.activeId,
		},
	});
	const remote = useRemote(nav.route);
	const people = usePeople(nav.route, session.slice.admin);
	const update = useUpdate(session.state?.update, session.act, session.reportError);

	return {
		...preferences,
		...navigation,
		...session.slice,
		...providers.slice,
		...drafts.slice,
		...conversation.slice,
		...choices,
		...rail,
		...remote,
		...people,
		...update,
	};
}
