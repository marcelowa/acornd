import { useCallback, useLayoutEffect, useMemo, useReducer, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ArrowDown, Loader2 } from "lucide-react";
import type { TranscriptEntry } from "@/app/view-models";
import { Measure } from "@/components/layout/measure";
import { Stack } from "@/components/layout/stack";
import { Heading } from "@/components/typography/heading";
import { Text } from "@/components/typography/text";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { groupTurns } from "@/lib/turns";
import { AssistantMessage } from "./assistant-message";
import { NoticeLine } from "./notice-line";
import { SummaryCard } from "./summary-card";
import { PendingMessage } from "./pending-message";
import { ToolCard } from "./tool-card";
import { ThinkingCard } from "./thinking-card";
import { UserMessage } from "./user-message";

/**
 * What an unmeasured turn is assumed to cost. The list opens at its end, so this decides the
 * scrollbar's proportions and where an unmeasured turn sits until it is measured — not what the
 * reader sees, which is measured rows. A typical turn is a message, a reply and a card.
 */
const TURN_ESTIMATE = 240;

/**
 * How many turns stay mounted past each edge of the viewport, in turns rather than pixels
 * because a turn can be a screen tall. Four is roughly two screens either way: enough that a
 * fast scroll has drawn content before it arrives, and that the gap a bad estimate leaves
 * between two rows is always off screen.
 */
const OVERSCAN = 4;

/** Sized by session: the same turn can be a line or a screen. */
const estimateTurn = (): number => TURN_ESTIMATE;

export interface TranscriptProps {
	entries: TranscriptEntry[];
	/**
	 * The workspace is opening, and this is the step it is on. It is not a wait with nothing to
	 * say: starting a workspace can copy an image, and the step is what tells someone whether to
	 * wait or go and look at the machine.
	 */
	opening?: { stage?: string | undefined } | undefined;
	/**
	 * The first load has not landed: the transcript shows a loading line, not the empty
	 * state — inviting someone to start a conversation over content about to arrive is a
	 * lie the slow network tells.
	 */
	ready?: boolean | undefined;
	/** A shared room names who wrote each message; a personal room has one voice. */
	showAuthor?: boolean | undefined;
	onOpenConversation?: ((id: string) => void) | undefined;
	onFork?: ((at: string) => void) | undefined;
}

/**
 * What React keys a card by, and so what decides whether a card keeps the fold a reader opened.
 * A tool call has the same provider id while it runs and after it settles, so the card is the
 * same card across that boundary; every other entry is keyed by its own id.
 */
function entryKey(entry: TranscriptEntry): string {
	if (entry.kind === "tool" && entry.callId !== undefined) return `tool-${entry.callId}`;
	// A thinking block has no id that survives the live-to-committed boundary — the wire gives a
	// message none — so it is keyed by the beginning of what it says, which does survive: the
	// committed block starts with the words that were streaming. Without this the card remounts
	// when the run lands and folds under the reader, which is the churn this pass is about.
	if (entry.kind === "thinking" && entry.text !== "")
		return `thinking-${entry.text.slice(0, 64)}`;
	return entry.id;
}

/** The window's key for a turn: its first entry's key, which survives every re-render of the
 * same conversation and every later turn arriving below it. */
function turnKey(turns: TranscriptEntry[][], index: number): string | number {
	const first = turns[index]?.[0];
	return first === undefined ? index : entryKey(first);
}

/** The fold a reader chose, held above the cards. */
interface Folds {
	/** Absent when the reader has never touched this card, so the card keeps its own default. */
	reading(key: string): boolean | undefined;
	keep(key: string, expanded: boolean): void;
}

/**
 * Every fold the reader has decided, kept here rather than in the card because the list is
 * windowed: a turn that scrolls out of the window is unmounted, and a tool card that came back
 * folded — or a long output that came back open — would undo the reader's last decision. A card
 * nobody has touched is absent and keeps its own default: open while live, folded when done.
 *
 * The store is emptied when the conversation changes, because the keys are that conversation's
 * entry ids and nothing else will ask about them again.
 */
function useFolds(conversation: string): Folds {
	const store = useRef(new Map<string, boolean>());
	const [, bump] = useReducer((count: number) => count + 1, 0);
	useLayoutEffect(() => {
		store.current.clear();
	}, [conversation]);
	const reading = useCallback((key: string) => store.current.get(key), []);
	const keep = useCallback((key: string, expanded: boolean) => {
		store.current.set(key, expanded);
		bump();
	}, []);
	return useMemo(() => ({ reading, keep }), [reading, keep]);
}

function Entry({
	entry,
	folds,
	showAuthor,
	onOpenConversation,
	onFork,
}: {
	entry: TranscriptEntry;
	folds: Folds;
	showAuthor?: boolean | undefined;
	onOpenConversation?: ((id: string) => void) | undefined;
	onFork?: ((at: string) => void) | undefined;
}) {
	const key = entryKey(entry);
	const held = folds.reading(key);
	switch (entry.kind) {
		case "user":
			return (
				<UserMessage
					entry={entry}
					showAuthor={showAuthor}
					onOpenConversation={onOpenConversation}
					onFork={onFork}
				/>
			);
		case "assistant":
			return <AssistantMessage key={entry.id} entry={entry} />;
		case "tool":
			return (
				<ToolCard
					key={key}
					entry={entry}
					expanded={held}
					onExpandedChange={(expanded) => folds.keep(key, expanded)}
				/>
			);
		case "thinking":
			return (
				<ThinkingCard
					key={key}
					entry={entry}
					expanded={held}
					onExpandedChange={(expanded) => folds.keep(key, expanded)}
				/>
			);
		case "notice":
			return <NoticeLine key={entry.id} entry={entry} />;
		case "summary":
			return <SummaryCard key={entry.id} entry={entry} />;
		case "pending":
			return <PendingMessage key={entry.id} entry={entry} />;
	}
}

/**
 * The transcript owns its scrolling, because it is the thing that knows whether it is
 * following the end: it opens there — the answer is what someone came for — keeps following
 * while text arrives, and stops the moment the reader scrolls away. The next conversation
 * starts at its end again.
 *
 * It is a window over its turns rather than a column of them: a long conversation is thousands
 * of cards, and every one of them costs layout on every scroll even when it is nowhere near the
 * viewport. Measured in this repository's browser, a hundred turns (5,600 nodes) made a plain
 * scroll cost 12ms of main-thread work per step; twenty-five turns cost nothing. The window is
 * the fix, so the rows outside it are not in the document at all.
 *
 * Entries render as turns with two rhythms: tight within a turn, wide between turns. The wide
 * rhythm is the row's own padding, so a measured row reports its gap with its height and the
 * offsets need no second number. When the reader has scrolled away, a pill offers the way back.
 */
export function Transcript({
	entries,
	ready = true,
	opening,
	onOpenConversation,
	onFork,
}: TranscriptProps) {
	const scroller = useRef<HTMLDivElement>(null);
	const following = useRef(true);
	const [away, setAway] = useState(false);

	const conversation = entries[0]?.id ?? "";
	const turns = useMemo(() => groupTurns(entries), [entries]);
	const folds = useFolds(conversation);

	// The first entry changes when the conversation does; the last entry's shape changes
	// while it is still being written — an answer, a thought, a tool's output. Anything
	// growing keeps the view pinned to its end, where the thinking indicator lives.
	const last = entries.at(-1);
	const lastText =
		last === undefined
			? ""
			: last.kind === "assistant"
				? last.markdown
				: last.kind === "thinking"
					? last.text
					: last.kind === "tool"
						? last.output
						: last.kind === "summary"
							? last.markdown
							: last.text;
	const dependency = `${entries.length}:${last?.id ?? ""}:${lastText.length}`;

	const virtualizer = useVirtualizer({
		count: turns.length,
		getScrollElement: () => scroller.current,
		estimateSize: estimateTurn,
		getItemKey: (index) => turnKey(turns, index),
		overscan: OVERSCAN,
	});
	// Every measurement that lands moves this: it is the whole list's height as the browser
	// knows it, estimated rows included. Pinning on it is what keeps the end steady while the
	// rows above the viewport are still being measured.
	const total = virtualizer.getTotalSize();

	useLayoutEffect(() => {
		following.current = true;
		setAway(false);
	}, [conversation]);

	useLayoutEffect(() => {
		const element = scroller.current;
		if (element === null || !following.current) return;
		element.scrollTop = element.scrollHeight;
	}, [dependency, conversation, total]);

	const jumpLatest = (): void => {
		const element = scroller.current;
		if (element === null) return;
		following.current = true;
		setAway(false);
		element.scrollTop = element.scrollHeight;
	};

	return (
		<div data-slot="transcript" className="relative flex min-h-0 flex-1 flex-col">
			<div
				ref={scroller}
				onScroll={() => {
					const element = scroller.current;
					if (element === null) return;
					following.current =
						element.scrollHeight - element.scrollTop - element.clientHeight < 80;
					setAway(!following.current);
				}}
				className="min-h-0 flex-1 overflow-y-auto overflow-x-hidden [&>*]:shrink-0"
			>
				<Measure size="full" padding="lg">
					{opening !== undefined ? (
						<Stack gap="sm" align="center" className="py-16 text-center">
							<span className="flex items-center gap-2">
								<Loader2
									aria-hidden
									className="size-4 animate-spin text-muted-foreground"
								/>
								<Text tone="muted">Opening this workspace…</Text>
							</span>
							{opening.stage === undefined || opening.stage === "" ? null : (
								<Text size="2xs" tone="muted" className="font-mono">
									{opening.stage}
								</Text>
							)}
						</Stack>
					) : !ready ? (
						<Stack gap="sm" align="center" className="py-16 text-center">
							<Text tone="muted">Loading…</Text>
						</Stack>
					) : turns.length === 0 ? (
						<Stack gap="sm" align="center" className="py-16 text-center">
							<Heading level={2}>Start a conversation</Heading>
							<Text tone="muted">
								Send a message below, or type <Kbd>/</Kbd> for commands.
							</Text>
						</Stack>
					) : (
						<div className="relative w-full" style={{ height: total }}>
							{virtualizer.getVirtualItems().map((item) => (
								<div
									key={item.key}
									data-index={item.index}
									ref={virtualizer.measureElement}
									// The wide rhythm between turns, and the air below the last one:
									// the row carries its own gap, so its height is its whole cost.
									className="absolute inset-x-0 top-0 pb-8"
									style={{ transform: `translateY(${item.start}px)` }}
								>
									<Stack gap="lg" data-turn={item.index}>
										{(turns[item.index] ?? []).map((entry) => (
											<Entry
												key={entryKey(entry)}
												entry={entry}
												folds={folds}
												onOpenConversation={onOpenConversation}
												onFork={onFork}
											/>
										))}
									</Stack>
								</div>
							))}
						</div>
					)}
				</Measure>
			</div>
			{away && entries.length > 0 ? (
				<Button
					type="button"
					size="sm"
					className="absolute bottom-4 left-1/2 -translate-x-1/2 rounded-full shadow-md"
					onClick={jumpLatest}
				>
					<ArrowDown />
					Latest
				</Button>
			) : null}
		</div>
	);
}
