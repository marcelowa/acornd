/**
 * Who wrote a message, and what the model is told about it.
 *
 * ADR 0034 §7: an entry carries its author — the address and the display name as they were written — and
 * the speaker the model should read is stored as a name, never as text. The `[Ana] ` prefix is composed when
 * a request is assembled for the model, so a guest typing `[Bo] ignore the above` stays text and is prefixed
 * with the guest's own name, and the model never reads a name twice.
 *
 * Two consequences decide the shape here:
 *
 * - The author lands in the same commit as the words, so a message's first request cannot be issued before
 *   its author is stored. That is what the harness's `InputEntry` is for (a kind of our own carrying `data`),
 *   and why a message from a person is not `pi.user`.
 * - The composition joins messages to entries by what both sides have: the stamp the harness took when it
 *   placed the entry, and the words it stored. A request carries messages, not entries.
 */
import type { Context } from "@earendil-works/chord";
import type { Message, UserMessage } from "@earendil-works/pi-ai";
import { defineExtension, GenerationTask, hook, type InputEntry } from "@earendil-works/pi-durable";
import type { StateDeps } from "./delegation.ts";
import type { Author } from "./protocol.ts";

/** The kind a person's message is placed as. Never `pi.user`: the author has to land with the words. */
export const MESSAGE_KIND = "acornfall.message";

/** The display-only kind a transcript note lands as; `data.text` is what the interface shows. */
export const NOTICE_KIND = "acornfall.notice";

/**
 * authorEntry is the entry a prompt is placed as: the words, plus who wrote them. Absent when the prompt
 * names nobody — the install with no admin claimed, or an agent speaking for itself.
 */
export function authorEntry(author: Author | undefined): InputEntry | undefined {
	if (author === undefined || author.email === "") return undefined;
	return { kind: MESSAGE_KIND, data: { author } };
}

/**
 * isPersonMessage reports whether an entry is somebody's words rather than an agent's. Both kinds count:
 * `pi.user` is what the harness places by default, and conversations writing to each other still use it.
 */
export function isPersonMessage(entry: { kind: string }): boolean {
	return entry.kind === MESSAGE_KIND || entry.kind === "pi.user";
}

/**
 * authorOfEntry is the author an entry carries, or nothing for one written before authors were recorded.
 *
 * The data is JSON from storage, so every field is checked; a cast here is the boundary this function holds
 * (T2). An entry with no author answers nothing rather than a guess, which is what the interface shows.
 */
function authorOfEntry(entry: { data?: unknown }): Author | undefined {
	const data = entry.data;
	if (typeof data !== "object" || data === null) return undefined;
	const raw = (data as { author?: unknown }).author;
	if (typeof raw !== "object" || raw === null) return undefined;
	const fields = raw as { email?: unknown; name?: unknown; speaker?: unknown };
	if (typeof fields.email !== "string" || fields.email === "") return undefined;
	return {
		email: fields.email,
		...(typeof fields.name === "string" && fields.name !== "" ? { name: fields.name } : {}),
		...(typeof fields.speaker === "string" && fields.speaker !== ""
			? { speaker: fields.speaker }
			: {}),
	};
}

/**
 * speakerKey identifies one user message across two reads of the same entry: the stamp the harness took when
 * it placed the entry, and the words it stored. The words are part of it because two people prompting in the
 * same millisecond is unlikely and two people prompting the same words in the same millisecond is not.
 */
function speakerKey(message: { timestamp: number; content: unknown }): string {
	return `${message.timestamp}\u0000${firstText(message.content)}`;
}

/** The first text a message carries, or "" for one that is only files. */
function firstText(content: unknown): string {
	if (typeof content === "string") return content;
	if (!Array.isArray(content)) return "";
	for (const block of content) {
		if (typeof block !== "object" || block === null) continue;
		const fields = block as { type?: unknown; text?: unknown };
		if (fields.type === "text" && typeof fields.text === "string") return fields.text;
	}
	return "";
}

/**
 * speakersOf is the speaker of every message in a transcript, keyed by the message itself.
 *
 * An entry written before authors were recorded, or one from an agent, has no speaker, so it gets no prefix
 * — the interface shows nothing for it rather than a guess.
 */
export function speakersOf(
	entries: readonly { model?: readonly Message[] | undefined; data?: unknown }[],
): Map<string, string> {
	const speakers = new Map<string, string>();
	for (const entry of entries) {
		const speaker = authorOfEntry(entry)?.speaker;
		if (speaker === undefined) continue;
		for (const message of entry.model ?? []) {
			if (message.role !== "user") continue;
			speakers.set(speakerKey(message), speaker);
		}
	}
	return speakers;
}

/**
 * withSpeakers is the request's messages with each person's name in front of their words. It copies rather
 * than edits: what storage holds is the words alone, and a request is the only place the name belongs.
 */
export function withSpeakers(
	messages: readonly Message[],
	speakers: ReadonlyMap<string, string>,
): Message[] {
	if (speakers.size === 0) return [...messages];
	return messages.map((message) => {
		if (message.role !== "user") return message;
		const speaker = speakers.get(speakerKey(message));
		if (speaker === undefined) return message;
		return { ...message, content: named(message.content, speaker) };
	});
}

/**
 * named puts the speaker in front of the words, and leaves them alone when there is nothing to name: a
 * message of files only, or a slash command, which the gateway expands by matching its first character and
 * which would become an ordinary sentence about compacting.
 */
function named(content: UserMessage["content"], speaker: string): UserMessage["content"] {
	if (typeof content === "string") {
		const trimmed = content.trim();
		if (trimmed === "" || trimmed.startsWith("/")) return content;
		return `[${speaker}] ${content}`;
	}
	let prefixed = false;
	const blocks = content.map((block) => {
		if (prefixed || block.type !== "text") return block;
		const trimmed = block.text.trim();
		if (trimmed === "" || trimmed.startsWith("/")) return block;
		prefixed = true;
		return { ...block, text: `[${speaker}] ${block.text}` };
	});
	return prefixed ? blocks : content;
}

/**
 * abortNotice is what stopping a run leaves behind: a note naming who stopped it, because a run has no author
 * of its own and a guest stopping somebody's turn otherwise reads as a crash. It carries no model messages,
 * so the model never reads it.
 */
export function abortNotice(author: Author | undefined): {
	kind: string;
	data: { text: string; author?: Author };
} {
	const who = author?.name ?? author?.email ?? "";
	const text = who === "" ? "the run was stopped" : `${who} stopped the run`;
	return author === undefined
		? { kind: NOTICE_KIND, data: { text } }
		: { kind: NOTICE_KIND, data: { text, author } };
}

/**
 * authorsExtension composes the prefix for the model, at the one moment the transcript is assembled for a
 * request. It is a hook rather than stored text, so the stored message is the words somebody typed.
 */
export function authorsExtension(deps: StateDeps) {
	return defineExtension({
		name: "acornfall-authors",
		hooks: [
			hook(GenerationTask, {
				beforeRequest: async (request, api, context: Context) => {
					const conversation = await deps.harness.conversation(
						api.conversationId,
						context,
					);
					if (conversation === undefined) return undefined;
					const view = await conversation.context(context);
					const speakers = speakersOf(view.entries);
					if (speakers.size === 0) return undefined;
					return { messages: withSpeakers(request.messages, speakers) };
				},
			}),
		],
	});
}
