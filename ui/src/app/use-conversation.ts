import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
	abortConversation,
	answerSecretRequest,
	cancelSecretRequest,
	compactConversation,
	fetchCommands,
	fetchCompactPreview,
	forkConversation,
	sendMessage,
	uploadAttachment,
	type CompactPreview,
	type SecretAnswerInput,
	type UploadedAttachment,
} from "./client";
import { foldContext, lastUsage, type ContextRequest } from "./context";
import { textOf, toTranscript } from "./map";
import type { Route } from "./use-navigation";
import type { Session } from "./use-session";
import type { BusyAction, SlashCommand, TranscriptEntry } from "./view-models";
import type { WireSecretEvent, WireSender, WireUsage, WireView } from "./wire";

/** The conversation in view: its transcript, what the composer can do in it, and its cards. */
export interface ConversationSlice {
	entries: TranscriptEntry[];
	/** The worker can take a steering input mid-turn; without it the choice is stop or queue. */
	steerable: boolean;
	/** The commands the server would expand, for the slash menu. */
	commands: SlashCommand[];
	/** What the model saw, request by request — the inspector's fold. */
	context: ContextRequest[];
	usage?: WireUsage | undefined;
	harness?: string | undefined;
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
	/** A tool's open secret request, shown as a card in the conversation. */
	secretRequest?: WireSecretEvent | undefined;
	/** Send the card's value to the gateway; the tool waiting on it gets only the outcome. */
	answerSecret: (requestId: string, input: SecretAnswerInput) => Promise<void>;
	/** Close the card without setting anything. */
	cancelSecret: (requestId: string) => Promise<void>;
}

/** The conversation's side of the stream. None of these change identity. */
export interface Conversation {
	slice: ConversationSlice;
	/** A view frame: claim the marks of the messages it lands, then show it. */
	receiveView: (view: WireView, senders: Record<string, WireSender>) => void;
	/** A secret frame: a request opens the card, anything else closes the card it names. */
	receiveSecret: (event: WireSecretEvent) => void;
	/** Forget the transcript and its senders: a new stream is starting. */
	resetView: () => void;
	/** Forget the transcript: the next one arrives with the workspace or conversation switched to. */
	clearView: () => void;
}

export function useConversation(session: Session, route: Route): Conversation {
	const { state, workspace, activeId, activeIdRef, reportError, clearFailure, act, reload } =
		session;
	const [secretRequest, setSecretRequest] = useState<WireSecretEvent | undefined>(undefined);
	const [view, setView] = useState<WireView | undefined>(undefined);
	const [senders, setSenders] = useState<Record<string, WireSender>>({});
	/**
	 * The steer-or-queued mark a message was sent with, per conversation and entry id. The wire
	 * keeps no link between an inbox item and the entry it becomes, so this is the interface's own
	 * memory: `receiveView` claims it as the message lands. See
	 * `docs/ui-workflow/refactor-ux/09-build.md`.
	 */
	const [marks, setMarks] = useState<Record<string, Record<string, BusyAction>>>({});
	const [commands, setCommands] = useState<SlashCommand[]>([]);

	/** What is in the inbox and not committed yet, by its own words: how it was sent. */
	const sentMarks = useRef(new Map<string, BusyAction>());

	const receiveView = useCallback(
		(nextView: WireView, nextSenders: Record<string, WireSender>): void => {
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
			// The view frame does not say which conversation it belongs to: it is the one the
			// stream is following.
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
		[activeIdRef],
	);

	const receiveSecret = useCallback((event: WireSecretEvent): void => {
		if (event.kind === "request") {
			setSecretRequest(event);
			return;
		}
		setSecretRequest((current) =>
			current?.requestId === event.requestId ? undefined : current,
		);
	}, []);

	const resetView = useCallback((): void => {
		setView(undefined);
		setSenders({});
	}, []);

	const clearView = useCallback((): void => setView(undefined), []);

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

	const contextFold = useMemo(() => foldContext(view?.entries ?? []), [view]);

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

	return {
		slice: {
			entries,
			steerable: state?.capabilities.steer ?? false,
			commands,
			context: contextFold,
			usage: lastUsage(view?.entries ?? []),
			harness: state?.harness,
			fork,
			compact,
			compactOpen,
			compactPreview,
			openCompactDialog,
			closeCompactDialog,
			send,
			upload,
			abort,
			secretRequest,
			answerSecret,
			cancelSecret,
		},
		receiveView,
		receiveSecret,
		resetView,
		clearView,
	};
}
