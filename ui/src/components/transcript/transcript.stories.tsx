import type { Meta, StoryObj } from "@storybook/react-vite";
import type { TranscriptEntry } from "@/app/view-models";
import { Transcript } from "./transcript";

const meta = {
	title: "Conversation/Transcript",
	component: Transcript,
	parameters: { layout: "fullscreen" },
	decorators: [
		// The transcript is its own scroll region: the frame has to give it a height, or there
		// is nothing for it to scroll inside.
		(Story) => (
			<div className="flex h-full w-full flex-col bg-background">
				<Story />
			</div>
		),
	],
} satisfies Meta<typeof Transcript>;

export default meta;
type Story = StoryObj<typeof meta>;

const at = new Date(2026, 9, 6, 14, 32);

export const Conversation: Story = {
	args: {
		entries: [
			{ kind: "user", id: "1", at, text: "Why did the worker restart?", forkAt: "0" },
			{
				kind: "assistant",
				id: "2",
				at,
				markdown: [
					"The supervisor saw the process exit and restarted it. The recovery path is",
					"idempotent, so the second death announcement was the bug:",
					"",
					"- the death was announced twice",
					"- the second announcement raced the first",
					"",
					"```go",
					"if !stopped {",
					"\trestart()",
					"}",
					"```",
				].join("\n"),
			},
			{
				kind: "tool",
				id: "3",
				at,
				name: "bash",
				status: "done",
				isError: false,
				args: { command: "go test ./internal/workerpool" },
				output: "ok  \tgithub.com/marcelowa/acornd/gateway/internal/workerpool\t0.21s",
			},
			{
				kind: "tool",
				id: "4",
				at,
				name: "edit",
				status: "done",
				isError: true,
				args: { path: "worker/src/main.ts" },
				output: "the file changed since it was read",
			},
		],
	},
};

export const Empty: Story = { args: { entries: [] } };

/** Two user turns: the gap between turns is wider than the gap within one. */
export const MultiTurn: Story = {
	args: {
		entries: [
			{ kind: "user", id: "1", at, text: "Why did the worker restart?", forkAt: "0" },
			{
				kind: "assistant",
				id: "2",
				at,
				markdown: "The supervisor saw the process exit and restarted it.",
			},
			{
				kind: "tool",
				id: "3",
				at,
				name: "bash",
				status: "done",
				isError: false,
				args: { command: "go test ./internal/workerpool" },
				output: "ok  \tgithub.com/marcelowa/acornd/gateway/internal/workerpool\t0.21s",
			},
			{ kind: "user", id: "4", at, text: "And the second announcement?", forkAt: "1" },
			{
				kind: "assistant",
				id: "5",
				at,
				markdown: "That was the race: the second death announcement beat the first.",
			},
		],
	},
};

export const Notice: Story = {
	args: {
		entries: [
			{ kind: "assistant", id: "1", at, markdown: "Long reply before the context ran out." },
			{ kind: "notice", id: "2", at, text: "context compacted" },
			{ kind: "assistant", id: "3", at, markdown: "And the reply after it." },
		],
	},
};

export const NothingToCompact: Story = {
	args: {
		entries: [
			{ kind: "user", id: "1", at, text: "/compact" },
			{
				kind: "notice",
				id: "2",
				at,
				text: "Nothing to compact — the talk is short.",
			},
		],
	},
};

export const FoldReport: Story = {
	args: {
		entries: [
			{ kind: "user", id: "1", at, text: "/compact" },
			{
				kind: "notice",
				id: "2",
				at,
				text: "Folded 3 entries (1 turn) into one note. The last reply read 28,948 tokens (23% of 128,000).",
			},
		],
	},
};

export const CompactionSummary: Story = {
	args: {
		entries: [
			{ kind: "assistant", id: "1", at, markdown: "Long reply before the context ran out." },
			{
				kind: "summary",
				id: "2",
				at,
				markdown: "Fixed the announcement. Tests green. The rail still sorts by date.",
			},
			{ kind: "assistant", id: "3", at, markdown: "And the reply after it." },
		],
	},
};

export const Failure: Story = {
	args: {
		entries: [
			{ kind: "user", id: "1", at, text: "Try that again" },
			{
				kind: "assistant",
				id: "2",
				at,
				markdown: "",
				failure: "opencode-go/opus-5: the provider refused the request (429)",
			},
		],
	},
};

export const PeerAsk: Story = {
	args: {
		entries: [
			{
				kind: "user",
				id: "1",
				at,
				text: "[from conversation 2] Can you check the migration while I hold the lock?",
				sender: { kind: "ask", name: "scout", conversationId: "2" },
			},
		],
	},
};

/** The answer arriving: streaming text with the thinking dots still promising more. */
/** The first load: the reader waits, and is told so. */
export const Loading: Story = {
	args: { entries: [], ready: false },
};

export const Streaming: Story = {
	args: {
		entries: [
			{ kind: "user", id: "1", at, text: "Why did the worker restart?" },
			{
				kind: "assistant",
				id: "2",
				at,
				markdown: "The supervisor saw the process exit and",
				live: true,
			},
		],
	},
};

export const ToolRunning: Story = {
	args: {
		entries: [
			{
				kind: "tool",
				id: "1",
				at,
				name: "edit",
				status: "running",
				isError: false,
				args: { path: "worker/src/main.ts" },
				output: "",
			},
		],
	},
};

/** Sent while a turn was running: one steers it, one waits behind it. */
export const Pending: Story = {
	args: {
		entries: [
			{ kind: "user", id: "1", at, text: "Run the tests" },
			{ kind: "assistant", id: "2", at, markdown: "Running them now." },
			{
				kind: "pending",
				id: "pending-3",
				text: "also check the lock order",
				action: "steer",
			},
			{
				kind: "pending",
				id: "pending-4",
				text: "then update the changelog",
				action: "followUp",
			},
		],
	},
};

/**
 * The window, at the size it exists for: three hundred turns, one of them a tool output of
 * four hundred lines. Only the rows around the viewport are in the document — the rest are an
 * offset and an estimate — so this scrolls like a short transcript, and opens at its end.
 */
function longConversation(): TranscriptEntry[] {
	const entries: TranscriptEntry[] = [];
	for (let turn = 0; turn < 300; turn += 1) {
		entries.push({
			kind: "user",
			id: `u-${turn}`,
			at,
			text:
				turn % 7 === 0
					? `Turn ${turn}: and what about the lock order in the reconciler?`
					: `Turn ${turn}: check the suite again`,
		});
		if (turn % 5 === 0) {
			entries.push({
				kind: "thinking",
				id: `th-${turn}`,
				at,
				text:
					turn === 150
						? Array.from({ length: 40 }, (_, line) => `Reasoning line ${line}.`).join(
								"\n",
							)
						: "The failure is in the pool, not the handler.",
			});
		}
		entries.push({
			kind: "tool",
			id: `t-${turn}`,
			at,
			name: turn % 3 === 0 ? "bash" : "read",
			status: "done",
			isError: turn % 11 === 0,
			callId: `call-${turn}`,
			args: { command: `go test ./internal/workerpool -run Test${turn}` },
			output:
				turn === 221
					? Array.from(
							{ length: 400 },
							(_, line) => `output line ${line}: ok  github.com/marcelowa/acornd`,
						).join("\n")
					: `ok  \tgithub.com/marcelowa/acornd/gateway/internal/workerpool\t0.2${turn % 10}s`,
		});
		entries.push({
			kind: "assistant",
			id: `a-${turn}`,
			at,
			markdown:
				turn % 4 === 0
					? [
							`Turn ${turn} is green.`,
							"",
							"- the pool drains before the exit",
							"- the second announcement is the one that raced",
							"",
							"```go",
							"if !stopped {",
							"\tpool.Restart()",
							"}",
							"```",
						].join("\n")
					: `Turn ${turn}: the suite passed, nothing else to report.`,
		});
	}
	return entries;
}

export const Long: Story = {
	args: { entries: longConversation() },
};
