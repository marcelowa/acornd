/**
 * The conversations a worker has open, what they are called, and the agent each one runs.
 * See docs/adr/0005-multi-conversation.md and docs/adr/0006-conversation-lifecycle.md.
 *
 * A conversation id is pi-durable's own id, stringified. This layer never invents one;
 * control treats it as opaque.
 */
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import {
	AgentDoc,
	type Conversation,
	type EntryId,
	type Harness,
	LiveDoc,
	ROOT_CONVERSATION_ID,
	type ToolRegistration,
	UsageDoc,
} from "@earendil-works/pi-durable";
import { activityOf } from "./activity.ts";
import { isPersonMessage } from "./authors.ts";
import { resolveAgent, type ResolvedAgent } from "./config.ts";
import { conversationIdOf, ConversationMetaDoc } from "./meta.ts";
import type { ConversationInfo, ModelBinding } from "./protocol.ts";
import { thinkingLevelFor } from "./thinking.ts";
import { totalCost, totalTokens } from "./usage.ts";
import type { CommandTable, Registry, WorkerContext } from "./worker.ts";

const CONTEXT = BACKGROUND_CONTEXT;

const TITLE_SCAN_LIMIT = 20;
const TITLE_MAX = 64;

/** Live handles by conversation id; created on first use. */
export interface Conversations {
	get(id: string): Promise<Conversation>;
	add(conversation: Conversation): void;
}

export function conversationRegistry(harness: Harness): Conversations {
	const handles = new Map<string, Conversation>();
	return {
		async get(id) {
			const existing = handles.get(id);
			if (existing !== undefined) return existing;
			const handle = await harness.conversation(conversationIdOf(id), CONTEXT);
			if (handle === undefined) throw new Error(`no conversation ${id}`);
			handles.set(id, handle);
			return handle;
		},
		add(conversation) {
			handles.set(String(conversation.id), conversation);
		},
	};
}

/** A given name wins; otherwise the first user message is the name. */
async function nameOf(
	{ harness, conversations }: WorkerContext,
	id: string,
	conversation: Conversation,
): Promise<{ title: string; hidden: boolean }> {
	const meta = await harness.snapshot(ConversationMetaDoc, conversationIdOf(id), CONTEXT);
	const given = meta?.title?.trim() ?? "";
	if (given.length > 0) return { title: given, hidden: meta?.hidden === true };
	const derived = await deriveTitle(conversation);
	if (derived !== "New conversation") {
		// Freeze the first prompt as the name. The scan below is newest-first, so
		// without this the title would follow the latest message instead of the
		// first, renaming the rail row under its reader. A failed freeze is not a
		// failed listing: the derived name still answers this one.
		await setMeta(conversations, id, { title: derived }).catch(() => undefined);
	}
	return { title: derived, hidden: meta?.hidden === true };
}

async function deriveTitle(conversation: Conversation): Promise<string> {
	const page = await conversation.entries({}, TITLE_SCAN_LIMIT, undefined, CONTEXT);
	for (const entry of page.items) {
		if (!isPersonMessage(entry)) continue;
		const message = (entry.model ?? [])[0] as { content?: unknown } | undefined;
		const text =
			typeof message?.content === "string"
				? message.content
				: Array.isArray(message?.content)
					? message.content
							.filter(
								(block): block is { type: "text"; text: string } =>
									typeof block === "object" &&
									block !== null &&
									(block as { type?: string }).type === "text",
							)
							.map((block) => block.text)
							.join(" ")
					: "";
		const trimmed = text.replace(/\s+/g, " ").trim();
		if (trimmed.length > 0) {
			return trimmed.length > TITLE_MAX ? `${trimmed.slice(0, TITLE_MAX - 1)}…` : trimmed;
		}
	}
	return "New conversation";
}

async function listConversations(context: WorkerContext): Promise<ConversationInfo[]> {
	const { harness, storage, conversations } = context;
	const found: ConversationInfo[] = [];
	let cursor: Parameters<typeof storage.scanConversations>[2];
	for (;;) {
		const page = await storage.scanConversations({}, 50, cursor, CONTEXT);
		for (const record of page.items) {
			// Every conversation is listed, including subagent ones. Ownership is
			// reported rather than used as a filter: the rail shows conversations
			// without a parent, and the inspector groups the ones that have one, so
			// hiding them here would leave the UI unable to show them at all.
			const id = String(record.id);
			const conversation = await conversations.get(id);
			const { title, hidden } = await nameOf(context, id, conversation);
			const owner = record.owner;
			const parent = owner === undefined ? undefined : String(owner.conversationId);
			// No catches: a snapshot answers undefined for a document that is not there, so a
			// failure here is a failure, and reading it as idle/0/unnamed is a lie the interface
			// would then show. `conversation.entries` below was always allowed to fail.
			const [live, usage, meta, agentConfig, newest] = await Promise.all([
				harness.snapshot(LiveDoc, conversationIdOf(id), CONTEXT),
				harness.snapshot(UsageDoc, conversationIdOf(id), CONTEXT),
				harness.snapshot(ConversationMetaDoc, conversationIdOf(id), CONTEXT),
				harness.snapshot(AgentDoc, conversationIdOf(id), CONTEXT),
				conversation.entries({}, 1, undefined, CONTEXT),
			]);
			const effectiveModel = agentConfig?.model;
			const effectiveThinking = agentConfig?.thinkingLevel;
			// Read from committed state, so a run that survived a restart still
			// shows as running, and a background run is never invisible.
			const running = live?.run !== undefined;
			const activity = activityOf(newest.items[0], running);
			found.push({
				id,
				title,
				hidden,
				root: record.id === ROOT_CONVERSATION_ID,
				running,
				lastEntryId: String(newest.items[0]?.id ?? ""),
				...(activity === undefined ? {} : { updatedAt: activity }),
				tokens: totalTokens(usage),
				cost: totalCost(usage),
				...(parent === undefined ? {} : { parent }),
				// The stored configuration is the truth for a conversation control does
				// not manage, which is every subagent.
				...(effectiveModel === undefined
					? {}
					: { model: `${effectiveModel.provider}/${effectiveModel.modelId}` }),
				...(effectiveThinking === undefined ? {} : { thinking: String(effectiveThinking) }),
				...(meta?.createdBy === undefined ? {} : { createdBy: meta.createdBy }),
				...(meta?.children === undefined || meta.children.length === 0
					? {}
					: { children: meta.children }),
			});
		}
		if (page.next === undefined) break;
		cursor = page.next;
	}
	// Newest first; ids are monotonic.
	return found.sort((a, b) => Number(b.id) - Number(a.id));
}

/** Writes metadata through the conversation's own commit, so it is durable. */
async function setMeta(
	conversations: Conversations,
	id: string,
	patch: { title?: string; hidden?: boolean },
): Promise<void> {
	const conversation = await conversations.get(id);
	await conversation.commit(async (tx) => {
		const meta = await tx.doc(ConversationMetaDoc, conversationIdOf(id));
		if (patch.title !== undefined) meta.title = patch.title;
		if (patch.hidden !== undefined) meta.hidden = patch.hidden;
	}, CONTEXT);
}

/** Registered tools by name. Unknown names are a config error, not a silent no-op. */
function toolsByName(registry: Registry): Map<string, ToolRegistration> {
	const byName = new Map<string, ToolRegistration>();
	for (const { tool } of registry.snapshot().tools()) byName.set(tool.name, tool);
	return byName;
}

function resolveTool(
	byName: Map<string, ToolRegistration>,
	name: string,
	agent: string,
): ToolRegistration {
	const tool = byName.get(name);
	if (tool === undefined) {
		throw new Error(
			`agent "${agent}" names unknown tool "${name}"; registered: ${[...byName.keys()].join(", ")}`,
		);
	}
	return tool;
}

/**
 * tools: null clears a previous restriction, an array offers exactly those names,
 * and { remove } subtracts. A deny always beats an allow.
 */
function toolFilter(
	registry: Registry,
	resolved: ResolvedAgent,
): readonly ToolRegistration[] | { remove: readonly ToolRegistration[] } | null {
	const byName = toolsByName(registry);
	if (resolved.allowTools !== undefined) {
		return resolved.allowTools.map((name) => resolveTool(byName, name, resolved.name));
	}
	if (resolved.denyTools.length > 0) {
		return {
			remove: resolved.denyTools.map((name) => resolveTool(byName, name, resolved.name)),
		};
	}
	return null;
}

/** One resolved agent application: definition first, explicit choices second. */
async function applyAgent(
	context: WorkerContext,
	conversationId: string,
	input: ModelBinding,
): Promise<{ resolved: ResolvedAgent; model: { provider: string; modelId: string } }> {
	const conversation = await context.conversations.get(conversationId);
	const resolved = resolveAgent(context.resources.catalog, input.definition);
	context.skillFilter.set(conversationId, resolved.skills);
	// Written into the conversation, not into control's settings: delegation and
	// the UI both need it, and it belongs with the transcript.
	await conversation.commit(async (tx) => {
		const meta = await tx.doc(ConversationMetaDoc, conversationIdOf(conversationId));
		meta.definition = input.definition ?? "";
	}, CONTEXT);

	// An explicit provider/modelId is the user's choice in the picker and beats
	// the definition; the definition beats nothing.
	const fromDefinition = resolved.model?.split("/");
	const provider = input.provider || fromDefinition?.[0] || "";
	const modelId = input.modelId || fromDefinition?.[1] || "";

	const instructions = [resolved.instructions, input.instructions ?? ""]
		.filter((text) => text.trim().length > 0)
		.join("\n\n");

	await conversation.configure(
		{
			...(provider && modelId ? { model: { provider, modelId } } : {}),
			thinkingLevel: thinkingLevelFor(input.thinkingLevel ?? resolved.thinking),
			instructions,
			tools: toolFilter(context.registry, resolved),
		},
		CONTEXT,
	);
	return { resolved, model: { provider, modelId } };
}

export function conversationCommands(
	context: WorkerContext,
): Pick<CommandTable, "conversations" | "create" | "open" | "rename" | "hide" | "fork"> {
	const { harness, conversations } = context;
	return {
		conversations: async () => ({ conversations: await listConversations(context) }),
		create: async () => {
			const created = await harness.createConversation(
				{ ownership: { kind: "ownerless" } },
				CONTEXT,
			);
			conversations.add(created);
			return { conversationId: String(created.id) };
		},
		open: async (command) => {
			const applied = await applyAgent(context, command.conversationId, command.agent);
			return {
				conversationId: command.conversationId,
				agent: applied.resolved,
				model: applied.model,
			};
		},
		rename: async (command) => {
			const title = command.title.trim();
			await setMeta(conversations, command.conversationId, { title });
			return { conversationId: command.conversationId, title };
		},
		hide: async (command) => {
			await setMeta(conversations, command.conversationId, { hidden: command.hidden });
			return { conversationId: command.conversationId, hidden: command.hidden };
		},
		fork: async (command) => {
			const conversation = await conversations.get(command.conversationId);
			const forked = await conversation.fork(
				Number(command.at) as EntryId,
				{ ownership: { kind: "ownerless" } },
				CONTEXT,
			);
			conversations.add(forked);
			const parent = await nameOf(context, command.conversationId, conversation);
			await setMeta(conversations, String(forked.id), { title: `Fork of ${parent.title}` });
			return { conversationId: String(forked.id) };
		},
	};
}
