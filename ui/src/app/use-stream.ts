import { useEffect } from "react";
import { fetchState, fetchWorkspaceStatus, openStream } from "./client";
import type { Route } from "./use-navigation";
import type { Session } from "./use-session";
import type { WireAuthEvent, WireSecretEvent, WireSender, WireView } from "./wire";

/** How often a workspace that is opening is asked which step it is on. Fast enough that the line
 * changes when the work does, slow enough that it is one request a second. */
const openingPollMs = 700;

/** Where the stream's frames go: each to the hook that owns what the frame carries. */
export interface StreamSinks {
	/** Forget the transcript before anything else: a new stream is starting. */
	resetView: () => void;
	/** The stream opening is when a draft saved while it was away can land. */
	flushDrafts: () => void;
	receiveView: (view: WireView, senders: Record<string, WireSender>) => void;
	receiveSecret: (event: WireSecretEvent) => void;
	receiveAuth: (id: string, event: WireAuthEvent) => void;
}

/**
 * The workspace's first read and the stream that follows it, as one effect: both start and stop
 * together whenever the workspace or the conversation changes, and every frame of a stream that
 * was closed is dropped by the same `cancelled` check.
 *
 * The state frames are the session's; the others go to their sinks. The sinks other than
 * `flushDrafts` never change identity, and `flushDrafts` changes with the workspace, so the
 * stream reopens exactly when the workspace or the conversation does.
 */
export function useStream(route: Route, session: Session, sinks: StreamSinks): void {
	const { workspace, reportError, clearFailure, setState, setReady, setOpening, setConnection } =
		session;
	const { resetView, flushDrafts, receiveView, receiveSecret, receiveAuth } = sinks;

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
}
