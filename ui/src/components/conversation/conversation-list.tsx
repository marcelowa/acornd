import { useMemo, useRef, useState, type ReactNode } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";
import { ChevronRight } from "lucide-react";
import type { ConversationSummary, RoomRights } from "@/app/view-models";
import { Text } from "@/components/typography/text";
import { Button } from "@/components/ui/button";
import { cn } from "cn";
import { ConversationRow } from "./conversation-row";

export interface ConversationListProps {
	/** Visible conversations: the app filters tombstoned and subagent rows out before this. */
	conversations: ConversationSummary[];
	/** Tombstoned conversations, restorable. An empty list renders no group. */
	deleted: ConversationSummary[];
	activeId?: string | undefined;
	/** What this caller may do with a conversation here: a guest is not offered rename or delete. */
	can?: Pick<RoomRights, "rename" | "hide"> | undefined;
	onSelect?: ((id: string) => void) | undefined;
	onRename?: ((id: string) => void) | undefined;
	onSecrets?: ((id: string) => void) | undefined;
	onHide?: ((id: string) => void) | undefined;
	onRestore?: ((id: string) => void) | undefined;
	className?: string | undefined;
}

interface Group {
	key: string;
	label: string;
	items: ConversationSummary[];
}

/**
 * What an unmeasured line is assumed to cost. Two kinds, two heights: a conversation is one
 * title line and a 44px target, a group's name is small type with its own step above it. Only
 * unmeasured lines use these — a line on screen is measured — but close is what keeps a rail
 * opening mid-history from jumping as the first rows correct themselves.
 */
const ROW_HEIGHT = 52;
const LABEL_HEIGHT = 32;

/** Rows past each edge of the viewport, so a flick lands on drawn content. A rail row is a
 * single line, so this can be generous. */
const OVERSCAN = 8;

/** One line of the rail: a date group's name, the Deleted control, or a conversation. */
type Item =
	| { kind: "label"; key: string; label: string }
	| { kind: "deleted"; key: string }
	| { kind: "row"; key: string; conversation: ConversationSummary };

/**
 * The rail as one flat list, because a window needs line numbers: each group contributes its
 * name and then its rows, the undated rows follow on their own (a tail, never a displacement of
 * Today), and the Deleted control is the last line until it is opened.
 */
function itemsOf(
	groups: Group[],
	undated: ConversationSummary[],
	deleted: ConversationSummary[],
	deletedOpen: boolean,
): Item[] {
	const items: Item[] = [];
	for (const group of groups) {
		items.push({ kind: "label", key: `label-${group.key}`, label: group.label });
		for (const conversation of group.items) {
			items.push({ kind: "row", key: `row-${conversation.id}`, conversation });
		}
	}
	for (const conversation of undated) {
		items.push({ kind: "row", key: `row-${conversation.id}`, conversation });
	}
	if (deleted.length > 0) {
		items.push({ kind: "deleted", key: "deleted" });
		if (deletedOpen) {
			for (const conversation of deleted) {
				items.push({ kind: "row", key: `row-${conversation.id}`, conversation });
			}
		}
	}
	return items;
}

function startOfDay(date: Date): Date {
	return new Date(date.getFullYear(), date.getMonth(), date.getDate());
}

function daysAgo(date: Date, now: Date): number {
	return Math.round((startOfDay(now).getTime() - startOfDay(date).getTime()) / 86_400_000);
}

/** Sort key: dated rows by time, undated rows last. */
function timeOf(conversation: ConversationSummary): number {
	return conversation.updatedAt instanceof Date
		? conversation.updatedAt.getTime()
		: Number.NEGATIVE_INFINITY;
}

/**
 * Date groups for the rail, newest first. Conversations without `updatedAt` — which is all
 * of them until the gateway provides timestamps — render flat, exactly as before. Once
 * dates exist, the groups lead and undated rows fall to the tail, so a straggler never
 * displaces Today. Within each group the most recently updated comes first.
 */
export function groupConversations(
	conversations: ConversationSummary[],
	now = new Date(),
): { groups: Group[]; undated: ConversationSummary[] } {
	const sorted = [...conversations].sort((a, b) => timeOf(b) - timeOf(a));
	const dated = sorted.filter(
		(conversation): conversation is ConversationSummary & { updatedAt: Date } =>
			conversation.updatedAt instanceof Date,
	);
	const undated = sorted.filter((conversation) => !(conversation.updatedAt instanceof Date));
	if (dated.length === 0) return { groups: [], undated };

	const buckets: Record<"today" | "yesterday" | "week" | "older", ConversationSummary[]> = {
		today: [],
		yesterday: [],
		week: [],
		older: [],
	};
	for (const conversation of dated) {
		const age = daysAgo(conversation.updatedAt, now);
		if (age <= 0) buckets["today"]?.push(conversation);
		else if (age === 1) buckets["yesterday"]?.push(conversation);
		else if (age <= 7) buckets["week"]?.push(conversation);
		else buckets["older"]?.push(conversation);
	}
	const groups: Group[] = [
		{ key: "today", label: "Today", items: buckets["today"] ?? [] },
		{ key: "yesterday", label: "Yesterday", items: buckets["yesterday"] ?? [] },
		{ key: "week", label: "This week", items: buckets["week"] ?? [] },
		{ key: "older", label: "Older", items: buckets["older"] ?? [] },
	].filter((group) => group.items.length > 0);
	return { groups, undated };
}

/**
 * The rail's conversations. It is a window over its rows rather than a column of them: a long
 * history is hundreds of rows, and every one of them cost layout on every scroll even when it
 * was nowhere near the viewport. Measured in this repository's browser, five hundred rows
 * (7,065 nodes) made a plain scroll cost 10ms of main-thread work per step.
 *
 * The groups, the undated tail and the Deleted section are unchanged — they are lines of one
 * flat list now, so the window has numbers to count. The gap between rows is each row's own
 * bottom padding, so a measured row reports its gap with its height.
 */
export function ConversationList({
	conversations,
	deleted,
	activeId,
	can,
	className,
	onSelect,
	onRename,
	onSecrets,
	onHide,
	onRestore,
}: ConversationListProps) {
	const [deletedOpen, setDeletedOpen] = useState(false);
	const scroller = useRef<HTMLDivElement>(null);
	const { groups, undated } = groupConversations(conversations);
	const items = useMemo(
		() => itemsOf(groups, undated, deleted, deletedOpen),
		[groups, undated, deleted, deletedOpen],
	);

	const virtualizer = useVirtualizer({
		count: items.length,
		getScrollElement: () => scroller.current,
		estimateSize: (index) => (items[index]?.kind === "row" ? ROW_HEIGHT : LABEL_HEIGHT),
		getItemKey: (index) => items[index]?.key ?? index,
		overscan: OVERSCAN,
	});

	const row = (conversation: ConversationSummary): ReactNode => (
		<ConversationRow
			conversation={conversation}
			active={conversation.id === activeId}
			can={can}
			onSelect={onSelect}
			onRename={onRename}
			onSecrets={onSecrets}
			onHide={onHide}
			onRestore={onRestore}
		/>
	);

	const line = (item: Item): ReactNode => {
		switch (item.kind) {
			case "label":
				return (
					<Text size="2xs" weight="medium" tone="muted" className="px-2 pt-3 uppercase">
						{item.label}
					</Text>
				);
			case "deleted":
				return (
					<Button
						type="button"
						variant="ghost"
						size="sm"
						className="mt-1 justify-start text-muted-foreground"
						aria-expanded={deletedOpen}
						onClick={() => setDeletedOpen((open) => !open)}
					>
						<ChevronRight
							className={cn("transition-transform", deletedOpen && "rotate-90")}
						/>
						Deleted ({deleted.length})
					</Button>
				);
			case "row":
				return row(item.conversation);
		}
	};

	return (
		<div
			ref={scroller}
			data-slot="conversation-list"
			className={cn("flex min-h-0 flex-1 flex-col overflow-y-auto", className)}
		>
			{conversations.length === 0 ? (
				<Text size="sm" tone="muted" className="px-2">
					No conversations yet.
				</Text>
			) : (
				<div
					className="relative w-full shrink-0"
					style={{ height: virtualizer.getTotalSize() }}
				>
					{virtualizer.getVirtualItems().map((entry) => {
						const item = items[entry.index];
						return item === undefined ? null : (
							<div
								key={entry.key}
								data-index={entry.index}
								ref={virtualizer.measureElement}
								className="absolute inset-x-0 top-0 pb-1"
								style={{ transform: `translateY(${entry.start}px)` }}
							>
								{line(item)}
							</div>
						);
					})}
				</div>
			)}
		</div>
	);
}
