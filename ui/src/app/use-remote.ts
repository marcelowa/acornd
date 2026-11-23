import { useCallback, useEffect, useState } from "react";
import {
	applyRemote,
	checkRemote as checkRemoteRequest,
	enableRemote as enableRemoteRequest,
	fetchRemote,
	forgetRemoteToken,
	previewRemote as previewRemoteRequest,
	type DoorReport,
	type RemotePreview,
	type RemoteStatus,
} from "./client";
import type { Route } from "./use-navigation";

/** The remote page: the door this install opens to the internet, and the form that sets it up. */
export interface RemoteSlice {
	/** The door as this install recorded it, asked for when the remote page is opened. */
	remote?: RemoteStatus | undefined;
	/** One press of the remote form: make Cloudflare match what the owner typed. */
	applyRemote: (request: { hostname: string; zone: string; token: string }) => Promise<void>;
	/** Forget the stored token, so the door stops following the book until a new one is pasted. */
	forgetRemote: () => Promise<void>;
	remoteBusy: boolean;
	remoteFailure?: string | undefined;
	/** What the token can see, once somebody checks it: the zones to choose from, and the team domain. */
	remotePreview?: RemotePreview | undefined;
	/** A token is being checked. */
	remotePreviewBusy: boolean;
	previewRemote: (token: string) => void;
	/** Ask the gateway for the remote state again, which is how the tunnel line stays current. */
	refreshRemote: () => void;
	/** The file records a door this process did not start with; one press restarts into it. */
	enableRemote: () => void;
	remoteRestarting: boolean;
	remoteRestarted: boolean;
	/** The last answer from outside, and whether a check is in flight. */
	remoteCheck?: DoorReport | undefined;
	checkRemote: () => void;
	remoteChecking: boolean;
}

/** The remote page's state. It is the install's, not the workspace's, so it reads only the route. */
export function useRemote(route: Route): RemoteSlice {
	const [remote, setRemote] = useState<RemoteStatus | undefined>(undefined);
	const [remoteFailure, setRemoteFailure] = useState<string | undefined>(undefined);
	const [remoteBusy, setRemoteBusy] = useState(false);
	const [remotePreview, setRemotePreview] = useState<RemotePreview | undefined>(undefined);
	const [remotePreviewBusy, setRemotePreviewBusy] = useState(false);
	const [remoteRestarting, setRemoteRestarting] = useState(false);
	const [remoteRestarted, setRemoteRestarted] = useState(false);
	const [remoteCheck, setRemoteCheck] = useState<DoorReport | undefined>(undefined);
	const [remoteChecking, setRemoteChecking] = useState(false);
	useEffect(() => {
		if (route.screen !== "settings" || route.settingsTab !== "remote") return;
		let cancelled = false;
		void fetchRemote().then(
			(next) => {
				if (!cancelled) setRemote(next);
			},
			() => {
				// A gateway that cannot answer says nothing here: the form still works, and a refusal with words
				// is what the person acts on, not "failed to fetch".
				if (!cancelled) setRemote(undefined);
			},
		);
		return () => {
			cancelled = true;
		};
	}, [route.screen, route.settingsTab]);
	const applyRemoteNow = useCallback(
		async (request: { hostname: string; zone: string; token: string }): Promise<void> => {
			setRemoteBusy(true);
			setRemoteFailure(undefined);
			try {
				setRemote(await applyRemote(request));
			} catch (cause) {
				setRemoteFailure(cause instanceof Error ? cause.message : String(cause));
			} finally {
				setRemoteBusy(false);
			}
		},
		[],
	);

	const forgetRemoteNow = useCallback(async (): Promise<void> => {
		setRemoteBusy(true);
		setRemoteFailure(undefined);
		try {
			setRemote(await forgetRemoteToken());
		} catch (cause) {
			setRemoteFailure(cause instanceof Error ? cause.message : String(cause));
		} finally {
			setRemoteBusy(false);
		}
	}, []);

	const checkRemoteNow = useCallback((): void => {
		setRemoteChecking(true);
		void checkRemoteRequest()
			.then(setRemoteCheck, (cause: unknown) => {
				// A check that cannot run is a finding, not a crash: it is shown where the answer would be.
				setRemoteCheck({
					guarded: false,
					status: 0,
					keys: false,
					detail: cause instanceof Error ? cause.message : String(cause),
				});
			})
			.finally(() => setRemoteChecking(false));
	}, []);

	const refreshRemoteNow = useCallback((): void => {
		void fetchRemote().then(setRemote, () => undefined);
	}, []);

	const previewRemoteNow = useCallback((token: string): void => {
		setRemotePreviewBusy(true);
		setRemoteFailure(undefined);
		void previewRemoteRequest(token)
			.then(setRemotePreview, (cause: unknown) => {
				// A token that cannot do this is a sentence about the token, which is what the person pasted.
				setRemoteFailure(cause instanceof Error ? cause.message : String(cause));
			})
			.finally(() => setRemotePreviewBusy(false));
	}, []);

	const enableRemoteNow = useCallback((): void => {
		if (remoteRestarting) return;
		setRemoteRestarting(true);
		setRemoteFailure(undefined);
		void enableRemoteRequest().then(
			(answer) => {
				// The answer arrives before the process replaces itself: either it is already serving the door, or
				// it is about to, and the address this page was opened on will not answer again.
				setRemoteRestarting(false);
				setRemoteRestarted(true);
				if (!answer.restarting) setRemotePreview(undefined);
			},
			(cause: unknown) => {
				setRemoteRestarting(false);
				setRemoteFailure(cause instanceof Error ? cause.message : String(cause));
			},
		);
	}, [remoteRestarting]);

	return {
		remote,
		applyRemote: applyRemoteNow,
		forgetRemote: forgetRemoteNow,
		remoteBusy,
		remoteFailure,
		remotePreview,
		remotePreviewBusy,
		previewRemote: previewRemoteNow,
		refreshRemote: refreshRemoteNow,
		enableRemote: enableRemoteNow,
		remoteRestarting,
		remoteRestarted,
		remoteCheck,
		checkRemote: checkRemoteNow,
		remoteChecking,
	};
}
