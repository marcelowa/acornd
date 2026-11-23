/**
 * View frames: the whole committed view of each attached conversation, sent whole and numbered.
 * See docs/adr/0002-layers-and-protocol.md, "Attachment and resume".
 */
import { BACKGROUND_CONTEXT, type Context } from "@earendil-works/chord/context";
import {
	type ConversationView,
	ConversationTranscript,
	type Harness,
} from "@earendil-works/pi-durable";
import type { Conversations } from "./conversations.ts";
import { MessagesDoc } from "./delegation-tasks.ts";
import { conversationIdOf } from "./meta.ts";
import { type Frame, log } from "./protocol.ts";
import type { CommandTable, WorkerContext } from "./worker.ts";

const CONTEXT = BACKGROUND_CONTEXT;

export interface Views {
	/** Watch a conversation and send its view, now and on every change. */
	attach(conversationId: string): Promise<void>;
	detach(conversationId: string): void;
	/** The last sequence number sent for a conversation, 0 before the first. */
	seq(conversationId: string): number;
}

export function createViews({
	harness,
	conversations,
	write,
}: {
	harness: Harness;
	conversations: Conversations;
	write: (frame: Frame) => void;
}): Views {
	const subscriptions = new Map<string, { dispose: () => void }>();
	const seqs = new Map<string, number>();

	function nextSeq(conversationId: string): number {
		const next = (seqs.get(conversationId) ?? 0) + 1;
		seqs.set(conversationId, next);
		return next;
	}

	/**
	 * View frames, serialized per conversation and carrying our own annotation.
	 *
	 * The view mounts only durable's documents, so anything of ours the interface needs —
	 * here, which user-visible entries were sent by another conversation — travels beside
	 * the value. The chain keeps sequence numbers in order when a frame's annotation has to
	 * be read asynchronously.
	 */
	const viewChains = new Map<string, Promise<void>>();
	// The last view per conversation, so a change to our own documents can be announced
	// without re-reading the view or waiting for durable to commit something it mounts.
	const lastViewValue = new Map<string, unknown>();

	function enqueueView(conversationId: string, value: unknown): void {
		lastViewValue.set(conversationId, value);
		const previous = viewChains.get(conversationId) ?? Promise.resolve();
		const next = previous.then(async () => {
			const messages = await harness
				.snapshot(MessagesDoc, conversationIdOf(conversationId), CONTEXT)
				.catch((error: unknown) => {
					// Never swallow this: a silent failure here is why attribution looked
					// like it was never recorded.
					log(`senders read failed for ${conversationId}:`, error);
					return undefined;
				});
			const senders = messages?.senders ?? {};
			write({
				type: "view",
				conversationId,
				seq: nextSeq(conversationId),
				value,
				...(Object.keys(senders).length === 0 ? {} : { acornd: { senders } }),
			});
		});
		viewChains.set(conversationId, next);
	}

	async function attach(conversationId: string): Promise<void> {
		const conversation = await conversations.get(conversationId);
		subscriptions.get(conversationId)?.dispose();

		// The view is a window over the transcript, not the transcript: it starts at the newest 50
		// entries and drops its oldest in one splice past 100, so a long talk would reach the
		// interface with its beginning missing. `ConversationTranscript` is the client for that —
		// it merges each window and pages what it skipped with the conversation's own entries —
		// and what this worker sends stays the shape the gateway and the interface already read.
		const transcript = new ConversationTranscript((query, limit, context) =>
			conversation.entries(query, limit, undefined, context),
		);
		// One publish at a time, in the order the frames arrived: paging the history in is a
		// round trip, and a later window finishing first would hand the gateway an older value
		// with a newer sequence number.
		let publishing: Promise<void> = Promise.resolve();
		const publish = (value: ConversationView, context: Context): Promise<void> => {
			publishing = publishing.then(async () => {
				let merged = transcript.merge(value);
				// The first window of a long talk starts at its newest entries. `earlier` is the
				// marker saying the transcript goes on above it, so the page is loaded before
				// publishing: an interface that opens on a default install shows the whole talk.
				while (merged.items[0]?.kind === "earlier") {
					merged = await transcript.loadEarlier(context);
				}
				enqueueView(conversationId, {
					conversation: merged.conversation,
					docs: merged.docs,
					entries: merged.items.flatMap((item) =>
						item.kind === "entry" ? [item.entry] : [],
					),
				});
			});
			publishing = publishing.catch((error: unknown) =>
				log(`view publish failed for ${conversationId}:`, error),
			);
			return publishing;
		};

		const view = await conversation.watch(CONTEXT, transcript.resume);
		await publish(view.value, CONTEXT);
		view.start(async (call, context) => {
			// A failure the source recovers from does not end the watch: the next value arrives as
			// a replacement, and the interface redraws from it. Nothing is lost but this frame.
			if (call.kind === "failed") {
				log(`view watch failed for ${conversationId}:`, call.error);
				return;
			}
			await publish(call.value, context);
		});

		// Durable's view mounts only its own documents, so a commit that writes ours does
		// not advance it and produces no frame. `watchDoc` is the documented way to observe
		// a document of our own; when it changes, the annotation the interface reads has
		// changed with it, so the current view is emitted again.
		const messages = await harness.watchDoc(
			MessagesDoc,
			conversationIdOf(conversationId),
			CONTEXT,
		);
		messages?.start(async () => {
			const current = lastViewValue.get(conversationId);
			if (current !== undefined) enqueueView(conversationId, current);
		});

		subscriptions.set(conversationId, {
			dispose: () => {
				void view.stop();
				void messages?.stop();
			},
		});
	}

	return {
		attach,
		detach(conversationId) {
			subscriptions.get(conversationId)?.dispose();
			subscriptions.delete(conversationId);
		},
		seq: (conversationId) => seqs.get(conversationId) ?? 0,
	};
}

export function viewCommands({ views }: WorkerContext): Pick<CommandTable, "attach" | "detach"> {
	return {
		attach: async (command) => {
			await views.attach(command.conversationId);
			return {
				conversationId: command.conversationId,
				seq: views.seq(command.conversationId),
			};
		},
		detach: async (command) => {
			views.detach(command.conversationId);
			return undefined;
		},
	};
}
