import { useCallback, useMemo, useRef, useState } from "react";
import {
	answerAuthPrompt,
	cancelAuthFlow,
	fetchProviderAllowlist,
	fetchProviderCatalog,
	logoutProvider,
	refreshProviders,
	saveProviderAllowlist,
	startProviderLogin,
	type ProviderAuthType,
	type ProviderCatalogEntry,
} from "./client";
import {
	authFlowAnswerFailed,
	authFlowAnswered,
	authFlowStarted,
	foldAuthEvent,
	rememberAuthEvent,
	takeAuthEvents,
	type AuthFlow,
	type AuthWaiting,
} from "./auth-flow";
import { toProviders } from "./map";
import type { Session } from "./use-session";
import type { ProviderSummary } from "./view-models";
import type { WireAuthEvent } from "./wire";

/** The Providers page: who the models come from, the login dialog, and which models each offers. */
export interface ProvidersSlice {
	/** Every provider, connected first: the settings screen lists these. */
	providers: ProviderSummary[];
	/** A login in flight, if any: the connect dialog renders it. */
	authFlow?: AuthFlow | undefined;
	/** Start connecting a provider. Prompts arrive on the stream. A refusal rejects, so the
	 * dialog can say so where the method was chosen. */
	startLogin: (provider: ProviderSummary, method: ProviderAuthType) => Promise<void>;
	/** Answer the waiting prompt. */
	answerLoginPrompt: (value: string) => void;
	/** Abort the running flow. */
	cancelLogin: () => void;
	/** Dismiss a finished flow. */
	closeLogin: () => void;
	/** Forget one provider's stored key. */
	logoutProvider: (id: string) => void;
	/** Re-read the catalog, for a key added outside the app. */
	refreshProviders: () => void;
	/** The provider whose models are being chosen, with the catalog and the list as they stand. */
	modelChoice?: { provider: ProviderCatalogEntry; allowed: string[] } | undefined;
	openProviderModels: (provider: ProviderSummary) => void;
	saveProviderModels: (models: string[]) => void;
	closeProviderModels: () => void;
}

export interface Providers {
	slice: ProvidersSlice;
	/** An auth frame from the stream: it lands in the flow it names, or waits for the reply that
	 * names that flow. */
	receiveAuth: (id: string, event: WireAuthEvent) => void;
}

/**
 * Provider logins. The start answers 202 with a flow id; every step after that arrives as an auth
 * frame on the stream, matched to the dialog that started it.
 */
export function useProviders(session: Session): Providers {
	const { state, workspace, reportError, clearFailure, act, reload } = session;
	const providers = useMemo(() => toProviders(state?.providers ?? []), [state]);
	const [authFlow, setAuthFlow] = useState<AuthFlow | undefined>(undefined);
	/**
	 * The auth id the dialog is rendering, and the frames that arrived before the POST that
	 * named it. A worker can answer a login start before the reply is parsed, and a frame with
	 * no flow to land in would otherwise be dropped — the dialog then waits forever on a prompt
	 * the provider already sent. The ref is written where the flow is set, not in an effect, so
	 * a frame cannot slip between the state and its own id.
	 */
	const authId = useRef<string | undefined>(undefined);
	const authWaiting = useRef<AuthWaiting>(new Map());

	const receiveAuth = useCallback((id: string, event: WireAuthEvent): void => {
		if (authId.current !== id) {
			rememberAuthEvent(authWaiting.current, id, event);
			return;
		}
		setAuthFlow((current) => (current === undefined ? current : foldAuthEvent(current, event)));
	}, []);

	const startLogin = useCallback(
		(provider: ProviderSummary, method: ProviderAuthType): Promise<void> => {
			return startProviderLogin(workspace, provider.id, method).then(
				(next) => {
					clearFailure();
					authId.current = next;
					// Whatever arrived while the reply was in flight belongs to this flow.
					setAuthFlow(
						authFlowStarted(
							next,
							provider,
							method,
							takeAuthEvents(authWaiting.current, next),
						),
					);
				},
				(cause: unknown) => {
					reportError(cause);
					throw cause;
				},
			);
		},
		[workspace, reportError, clearFailure],
	);
	const answerLoginPrompt = useCallback(
		(value: string) => {
			if (authFlow?.prompt === undefined) return;
			const { authId: id, prompt } = { authId: authFlow.authId, prompt: authFlow.prompt };
			setAuthFlow({ ...authFlow, answering: true });
			void answerAuthPrompt(workspace, id, prompt.promptId, value).then(
				() => {
					clearFailure();
					// The answer landed: the question it answered goes, and only that one.
					setAuthFlow((current) =>
						current?.authId === id
							? authFlowAnswered(current, prompt.promptId)
							: current,
					);
				},
				(cause: unknown) => {
					reportError(cause);
					// The question stays, so the answer can be given again.
					setAuthFlow((current) =>
						current?.authId === id
							? authFlowAnswerFailed(current, prompt.promptId)
							: current,
					);
				},
			);
		},
		[authFlow, reportError, workspace, clearFailure],
	);
	const cancelLogin = useCallback(() => {
		const id = authFlow?.authId;
		authId.current = undefined;
		setAuthFlow(undefined);
		if (id !== undefined) void cancelAuthFlow(workspace, id).catch(reportError);
	}, [authFlow, reportError, workspace]);
	const closeLogin = useCallback(() => {
		authId.current = undefined;
		setAuthFlow(undefined);
	}, []);
	const logoutProviderById = useCallback(
		(id: string) => {
			act(logoutProvider(workspace, id), () => void reload());
		},
		[act, workspace, reload],
	);
	const refreshProvidersNow = useCallback(() => {
		act(refreshProviders(workspace), () => void reload());
	}, [act, workspace, reload]);

	const [modelChoice, setModelChoice] = useState<
		{ provider: ProviderCatalogEntry; allowed: string[] } | undefined
	>(undefined);
	const openProviderModels = useCallback(
		(provider: ProviderSummary): void => {
			// The catalog and the list come from the install, not from the narrowed state: choosing from what is left
			// would make the list impossible to widen from the screen that writes it.
			void Promise.all([fetchProviderCatalog(), fetchProviderAllowlist()]).then(
				([catalog, allowed]) => {
					const entry = catalog.find((one) => one.id === provider.id);
					if (entry === undefined) return;
					setModelChoice({ provider: entry, allowed: allowed[provider.id] ?? [] });
				},
				reportError,
			);
		},
		[reportError],
	);
	const saveProviderModels = useCallback(
		(models: string[]): void => {
			const provider = modelChoice?.provider.id;
			if (provider === undefined) return;
			void saveProviderAllowlist(provider, models).then(() => {
				setModelChoice(undefined);
				refreshProvidersNow();
			}, reportError);
		},
		[modelChoice, reportError, refreshProvidersNow],
	);
	const closeProviderModels = useCallback((): void => setModelChoice(undefined), []);

	return {
		slice: {
			providers,
			authFlow,
			startLogin,
			answerLoginPrompt,
			cancelLogin,
			closeLogin,
			logoutProvider: logoutProviderById,
			refreshProviders: refreshProvidersNow,
			modelChoice,
			openProviderModels,
			saveProviderModels,
			closeProviderModels,
		},
		receiveAuth,
	};
}
