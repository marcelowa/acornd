/**
 * Compaction and reset: folding a conversation's context, or starting it fresh, and the
 * `/compact` and `/reset` builtins that do either in the open. See
 * docs/adr/0006-conversation-lifecycle.md.
 */
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { Models } from "@earendil-works/pi-ai";
import type { Conversation, Cursor, EntryId } from "@earendil-works/pi-durable";
import { isPersonMessage, MESSAGE_KIND, NOTICE_KIND } from "./authors.ts";
import { foldNotice } from "./commands.ts";
import type { CommandTable, WorkerContext } from "./worker.ts";

const CONTEXT = BACKGROUND_CONTEXT;

/**
 * The active context: which entries the next request would see, and what the
 * last reply read, in provider tokens. Read before a fold, so the note can say
 * what left: the fold buries exactly the entries missing afterwards.
 */
async function activeScope(conversation: Conversation): Promise<{
	entries: { id: EntryId; kind: string }[];
	used: number | undefined;
}> {
	const view = await conversation.context(CONTEXT);
	const entries = view.entries.map((entry) => ({ id: entry.id, kind: entry.kind }));
	let used: number | undefined;
	let usedId = -1;
	for (const entry of view.entries) {
		if (entry.kind !== "pi.assistant") continue;
		const message = entry.model?.[0];
		if (message?.role !== "assistant") continue;
		const tokens = message.usage.input + message.usage.cacheRead;
		if (tokens > 0 && entry.id > usedId) {
			used = tokens;
			usedId = entry.id;
		}
	}
	return { entries, used };
}

/**
 * The model's window, when the conversation named one the catalog knows. Absent
 * otherwise, and the note then reports tokens without a percent.
 */
async function windowOf(models: Models, conversation: Conversation): Promise<number | undefined> {
	try {
		const agent = await conversation.agent(CONTEXT);
		const ref = agent.model;
		if (ref === undefined) return undefined;
		return models.getModel(ref.provider, ref.modelId)?.contextWindow;
	} catch {
		return undefined;
	}
}

export function compactionCommands({
	harness,
	models,
	conversations,
	now,
}: WorkerContext): Pick<CommandTable, "compact" | "compact_preview" | "reset" | "action"> {
	return {
		compact: async (command) => {
			// Manual compaction: the summary replaces the old prefix while the
			// conversation keeps working, and the transcript keeps a notice.
			const conversation = await conversations.get(command.conversationId);
			await conversation.compact(undefined, CONTEXT);
			return { conversationId: command.conversationId };
		},
		compact_preview: async (command) => {
			// What a manual compaction would fold: entry and turn counts, so the
			// dialog states the scope. The cut itself stays the harness's call.
			const conversation = await conversations.get(command.conversationId);
			let entries = 0;
			let turns = 0;
			let cursor: Cursor | undefined = undefined;
			for (;;) {
				const page = await conversation.entries({}, 200, cursor, CONTEXT);
				for (const entry of page.items) {
					entries += 1;
					if (isPersonMessage(entry)) turns += 1;
				}
				if (page.next === undefined) break;
				cursor = page.next;
			}
			return { conversationId: command.conversationId, entries, turns };
		},
		reset: async (command) => {
			// A fresh context: the reset entry lands and the next turn sees nothing
			// before it. The transcript keeps a notice where the cut was.
			const conversation = await conversations.get(command.conversationId);
			await conversation.reset(undefined, CONTEXT);
			return { conversationId: command.conversationId };
		},
		action: async (command) => {
			// A builtin is visible: the action runs instead of a model turn, then the
			// invocation lands as a user message. The record goes last so a reset
			// cannot bury it before its own cut. A compaction that folds nothing
			// leaves a note, so the send has an answer either way.
			const conversation = await conversations.get(command.conversationId);
			let notice: string | undefined;
			if (command.action === "compact") {
				const before = await activeScope(conversation);
				const contextWindow = await windowOf(models, conversation);
				const taskId = await conversation.compact(undefined, CONTEXT);
				const settled = await harness.waitForTask(taskId, CONTEXT);
				const outcome = settled.state.outcome;
				if (outcome.status !== "completed") {
					throw new Error(
						outcome.status === "failed" || outcome.status === "faulted"
							? outcome.error.message
							: `compaction ${outcome.status}`,
					);
				}
				const result = outcome.result;
				if (result.entryId === undefined && result.submissionId === undefined) {
					notice = "Nothing to compact — the talk is short.";
				} else {
					// The fold buries exactly the entries missing afterwards: the
					// record and this note land later, so they cannot pollute it.
					const after = await conversation.context(CONTEXT);
					const kept = new Set(after.entries.map((entry) => entry.id));
					const folded = before.entries.filter((entry) => !kept.has(entry.id));
					const foldedTurns = folded.filter(isPersonMessage).length;
					notice =
						folded.length === 0
							? "Context compacted into one note."
							: foldNotice(folded.length, foldedTurns, before.used, contextWindow);
				}
			} else {
				await conversation.reset(undefined, CONTEXT);
			}
			const recorded = await conversation.submit(
				{
					type: "write",
					entry: {
						kind: MESSAGE_KIND,
						model: [{ role: "user", content: command.text, timestamp: now() }],
						// A builtin is somebody's act, so the record names them; the model reads the command
						// itself, because a prefix would turn `/compact` into a sentence about compacting.
						...(command.author === undefined
							? {}
							: { data: { author: command.author } }),
					},
					requestId: crypto.randomUUID(),
				},
				CONTEXT,
			);
			await recorded.wait(CONTEXT);
			if (notice !== undefined) {
				// Display only: no model messages, so the note never joins the
				// context it just described.
				const noted = await conversation.submit(
					{
						type: "write",
						entry: { kind: NOTICE_KIND, data: { text: notice } },
						requestId: crypto.randomUUID(),
					},
					CONTEXT,
				);
				await noted.wait(CONTEXT);
			}
			return { conversationId: command.conversationId };
		},
	};
}
