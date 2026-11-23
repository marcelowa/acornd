import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
	applyUpdate,
	checkUpdate,
	fetchUpdateStatus,
	restartUpdate,
	rollbackUpdate,
	setUpdatePrefs as saveUpdatePrefs,
	type UpdatePrefs,
	type UpdateStatus,
} from "./client";
import { trackBuild } from "./update";
import type { WireUpdateSummary } from "./wire";

/** Self-update: the release this install runs, the one it could, and the presses between them. */
export interface UpdateSlice {
	/** The self-update state, if the gateway answered yet. One dot, not a list. */
	update?: UpdateStatus | undefined;
	/** Ask the gateway for the update state again; a success clears the last refusal. */
	refreshUpdate: () => void;
	/** An apply is in flight: the update button is disabled until it settles. */
	applyingUpdate: boolean;
	/** Force a release check now. */
	checkUpdateNow: () => void;
	/** Back up, download, verify, swap. Refusals land in `failure`. */
	applyUpdateNow: (tag: string, cleanVMs: boolean, drain: "wait" | "stop") => void;
	/** Finish a pending update: the process replaces itself. */
	restartUpdateNow: () => void;
	/** Restore the last good build. */
	rollbackUpdateNow: () => void;
	/** The Updates tab: channel, auto-check, pinned tag. */
	setUpdatePrefs: (prefs: UpdatePrefs) => void;
}

/**
 * The self-update state: the stream carries the summary (dot data) on every state event; a fetch
 * fills the details (notes, prefs, sizes) once at load and every 15 minutes. A failed poll falls
 * back to the stream summary rather than failing the whole screen: the update screen is advisory,
 * the conversation is not.
 *
 * `act` and `reportError` are the session's, so a refused update lands in the same strip as every
 * other refusal.
 */
export function useUpdate(
	summary: WireUpdateSummary | undefined,
	act: (request: Promise<unknown>) => void,
	reportError: (cause: unknown) => void,
): UpdateSlice {
	const [fetchedUpdate, setFetchedUpdate] = useState<UpdateStatus | undefined>(undefined);
	const updateFromState = useMemo<UpdateStatus | undefined>(
		() =>
			summary === undefined
				? undefined
				: {
						current: summary.current,
						latest: summary.latest ?? "",
						notes: "",
						channel: "",
						autoCheck: false,
						checking: false,
						available: summary.available,
						failed: summary.failed,
						restartRequired: summary.restartRequired,
						backups: [],
						downloadSize: 0,
						workerSize: 0,
						// The runs gate is the gateway's own answer, and the stream
						// summary does not carry it: the page's fetch does.
						runsActive: false,
						runs: [],
					},
		[summary],
	);
	const update = fetchedUpdate ?? updateFromState;
	// The build this page first saw: an apply or rollback restarts the server
	// under us, the stream's next state names the new build, and this bundle
	// is old — reload to fetch the new one. The stream summary is the
	// freshest sighting; the fetch fills in when the stream has said nothing.
	const runningBuild = useRef<string | undefined>(undefined);
	const streamBuild = updateFromState?.current;
	const fetchedBuild = fetchedUpdate?.current;
	useEffect(() => {
		const tracked = trackBuild(runningBuild.current, streamBuild ?? fetchedBuild);
		runningBuild.current = tracked.seen;
		if (tracked.reload) window.location.reload();
	}, [streamBuild, fetchedBuild]);
	useEffect(() => {
		let cancelled = false;
		const load = async (): Promise<void> => {
			try {
				const next = await fetchUpdateStatus();
				if (!cancelled) setFetchedUpdate(next);
			} catch {
				if (!cancelled) setFetchedUpdate(undefined);
			}
		};
		void load();
		const timer = window.setInterval(
			() => {
				void load();
			},
			15 * 60 * 1000,
		);
		return () => {
			cancelled = true;
			window.clearInterval(timer);
		};
	}, []);

	const refreshUpdate = useCallback((): void => {
		void fetchUpdateStatus().then(setFetchedUpdate, reportError);
	}, [reportError]);
	const checkUpdateNow = useCallback((): void => {
		act(checkUpdate().then(setFetchedUpdate));
	}, [act]);
	const [applyingUpdate, setApplyingUpdate] = useState(false);
	const applyUpdateNow = useCallback(
		(tag: string, cleanVMs: boolean, drain: "wait" | "stop"): void => {
			const request = applyUpdate(cleanVMs, drain, tag);
			// Immediate and single: the button disables on this render, so a
			// second tap has nothing to press. Settles either way, so a
			// refusal re-enables instead of sticking disabled.
			setApplyingUpdate(true);
			const done = (): void => setApplyingUpdate(false);
			void request.then(done, done);
			act(request.then(setFetchedUpdate));
		},
		[act],
	);
	const restartUpdateNow = useCallback((): void => {
		// The gateway answers before it becomes the new binary; the stream drops
		// and reconnects on its own, and the page follows the build from there.
		act(restartUpdate().then(setFetchedUpdate));
	}, [act]);
	const rollbackUpdateNow = useCallback((): void => {
		act(rollbackUpdate().then(setFetchedUpdate));
	}, [act]);
	const setUpdatePrefs = useCallback(
		(prefs: UpdatePrefs): void => {
			act(saveUpdatePrefs(prefs).then(setFetchedUpdate));
		},
		[act],
	);

	return {
		update,
		refreshUpdate,
		applyingUpdate,
		checkUpdateNow,
		applyUpdateNow,
		restartUpdateNow,
		rollbackUpdateNow,
		setUpdatePrefs,
	};
}
