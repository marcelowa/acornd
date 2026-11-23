/**
 * Starting and stopping a run: a prompt, with what rides along, and an abort that says who
 * stopped it. See docs/adr/0002-layers-and-protocol.md and docs/adr/0034-install-roles-and-room-roles.md.
 */
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { LiveDoc } from "@earendil-works/pi-durable";
import { attachmentFiles, messageContent } from "./attachments.ts";
import { abortNotice, authorEntry } from "./authors.ts";
import { conversationIdOf } from "./meta.ts";
import type { CommandTable, WorkerContext } from "./worker.ts";

const CONTEXT = BACKGROUND_CONTEXT;

export function runCommands({
	harness,
	conversations,
	sandbox,
	workspace,
	write,
}: WorkerContext): Pick<CommandTable, "prompt" | "abort"> {
	return {
		prompt: async (command) => {
			const conversation = await conversations.get(command.conversationId);
			// The gateway's command carries the text with omitempty, so a message that is only files
			// arrives without one at all. A prompt with no words is a message — a screenshot on its
			// own is one — and reading it as undefined made the whole send fail with a TypeError.
			const content = messageContent(
				command.text,
				await attachmentFiles(sandbox, workspace, command.attachments ?? []),
			);
			// The author rides the same commit as the words: the run starts the moment the entry
			// lands, so a prefix stored afterwards would miss the first request.
			const entry = authorEntry(command.author);
			const submission = await conversation.submit(
				{
					type: "input",
					content,
					whenBusy: command.whenBusy ?? "followUp",
					requestId: crypto.randomUUID(),
					...(entry === undefined ? {} : { entry }),
				},
				CONTEXT,
			);
			void submission.wait(CONTEXT).then(
				(settled) =>
					write({
						type: "submission",
						conversationId: command.conversationId,
						submissionId: String(submission.id),
						status: settled.status,
					}),
				(error: Error) =>
					write({
						type: "submission",
						conversationId: command.conversationId,
						submissionId: String(submission.id),
						status: `error: ${error.message}`,
					}),
			);
			return { submissionId: String(submission.id), disposition: "started" };
		},
		abort: async (command) => {
			const conversation = await conversations.get(command.conversationId);
			// Whether there is anything to stop is read before the abort takes it away: a note that
			// says a run was stopped when none was running is a sentence that teaches the reader to
			// ignore the notes.
			const live = await harness.snapshot(
				LiveDoc,
				conversationIdOf(command.conversationId),
				CONTEXT,
			);
			const stopping = live?.run !== undefined;
			await conversation.abort(CONTEXT);
			if (stopping) {
				// Display only: no model messages, so who stopped a run never joins the context.
				const noted = await conversation.submit(
					{
						type: "write",
						entry: abortNotice(command.author),
						requestId: crypto.randomUUID(),
					},
					CONTEXT,
				);
				await noted.wait(CONTEXT);
			}
			return undefined;
		},
	};
}
