/**
 * Worker wire protocol. See docs/adr/0002-layers-and-protocol.md.
 *
 * JSONL on stdin/stdout, one object per line, LF-framed. stderr is diagnostics.
 *
 * A conversation id on the wire is the harness's own id, stringified, and is
 * opaque to control: this layer never invents one.
 */

export type WhenBusy = "steer" | "followUp" | "reject";

/**
 * Who wrote a prompt: the address the door proved, the display name the book has for it, and the speaker the
 * model is told — empty in a room with one person in it (ADR 0032 §7, ADR 0034 §7).
 */
export type Author = {
	email: string;
	name?: string;
	/** The names the gateway would prefix: nothing in a personal room, and a non-owner's name in a shared one. */
	speaker?: string;
};

export interface ModelBinding {
	provider: string;
	modelId: string;
	thinkingLevel?: string;
	instructions?: string;
	/** Agent definition name; empty or absent selects the layered defaults only. */
	definition?: string;
}

export type Command =
	| { id: string; type: "describe" }
	| { id: string; type: "conversations" }
	| { id: string; type: "create" }
	| { id: string; type: "rename"; conversationId: string; title: string }
	| { id: string; type: "hide"; conversationId: string; hidden: boolean }
	| { id: string; type: "fork"; conversationId: string; at: string }
	| { id: string; type: "compact"; conversationId: string }
	| { id: string; type: "compact_preview"; conversationId: string }
	| { id: string; type: "reset"; conversationId: string }
	| {
			id: string;
			type: "action";
			conversationId: string;
			text: string;
			action: "compact" | "reset";
			author?: Author;
	  }
	| { id: string; type: "open"; conversationId: string; agent: ModelBinding }
	| { id: string; type: "attach"; conversationId: string }
	| { id: string; type: "detach"; conversationId: string }
	| {
			id: string;
			type: "prompt";
			conversationId: string;
			text: string;
			whenBusy?: WhenBusy;
			/** Stored attachment ids riding with the text. */
			attachments?: string[];
			/** Who sent it; absent for an agent, and for an install with no admin to name. */
			author?: Author;
	  }
	| {
			id: string;
			type: "attachment";
			name: string;
			mime: string;
			size: number;
			/** Host file the gateway staged; it removes it after the answer. */
			tempPath: string;
	  }
	| {
			id: string;
			type: "abort";
			conversationId: string;
			/** Who stopped the run: the note it lands as names them, because a run has no author of its own. */
			author?: Author;
	  }
	| { id: string; type: "agents" }
	| { id: string; type: "commands" }
	| { id: string; type: "expand"; text: string }
	| { id: string; type: "policy"; conversationId: string }
	| { id: string; type: "messages"; conversationId: string }
	| { id: string; type: "reload" }
	| {
			id: string;
			type: "login";
			authId: string;
			providerId: string;
			authType: "api_key" | "oauth";
	  }
	| { id: string; type: "logout"; providerId: string }
	| {
			id: string;
			type: "auth_answer";
			authId: string;
			promptId: string;
			value?: string;
			cancelled?: boolean;
	  }
	| { id: string; type: "auth_cancel"; authId: string }
	| {
			id: string;
			type: "secret_answer";
			secretRequestId: string;
			cancelled?: boolean;
			secretScope?: "global" | "workspace";
			restartRequired?: boolean;
			reason?: string;
	  }
	| {
			id: string;
			type: "secret_list_answer";
			secretRequestId: string;
			secrets: SecretInfo[];
			reason?: string;
	  }
	| { id: string; type: "usage" }
	| { id: string; type: "shutdown" };

export interface ProviderModelInfo {
	id: string;
	name: string;
	contextWindow?: number;
	/** Thinking levels this model actually supports; ["off"] means it cannot reason. */
	thinkingLevels: string[];
}

export interface ProviderInfo {
	id: string;
	name: string;
	authenticated: boolean;
	source?: string;
	/** How this install is authenticated, when it is: key or sign-in. */
	authType?: "api_key" | "oauth";
	/** Login methods the provider offers here. Empty means ambient-only:
	 * the key comes from the environment or a file, and there is nothing to type. */
	authTypes: ("api_key" | "oauth")[];
	models: ProviderModelInfo[];
}

export interface DescribeData {
	provider: string;
	workdir: string;
	providers: ProviderInfo[];
	capabilities: {
		steer: boolean;
		subagents: boolean;
		sandbox: string;
	};
}

/** A secret as the agent may know it: name, description, and delivery. Never a value. */
export interface SecretInfo {
	name: string;
	description?: string;
	scope?: string;
	hosts?: string[];
	delivery?: string;
	status?: string;
	/** The literal the guest environment holds on a substitute engine; empty for env. */
	placeholder?: string;
}

/** One conversation as the UI lists it. The id is the harness's own, stringified. */
export interface ConversationInfo {
	id: string;
	/** The given name, or the first user message truncated; "New conversation" before either. */
	title: string;
	/** True for the root conversation, which exists in every install. */
	root: boolean;
	/** Hidden conversations are listed so they can be restored, never dropped. */
	hidden: boolean;
	/** True while a run is live. Read from committed state, so it survives a restart. */
	running: boolean;
	/** Newest committed entry: the client's unread cursor. */
	lastEntryId: string;
	/** Last activity, Unix ms: the newest entry's message timestamp. Absent while
	 * the conversation has no entries, so a fresh one renders flat until it moves. */
	updatedAt?: number;
	/** Committed spend, so a background run is never invisible in cost. */
	tokens: number;
	cost: number;
	/** Set when this conversation was created by another one. */
	parent?: string;
	/** Conversations this one created, for the subagent list. */
	children?: string[];
	/** The agent this conversation actually runs, from its committed configuration. */
	model?: string;
	thinking?: string;
	/** Set when a conversation created this one without owning it: a peer. */
	createdBy?: string;
}

/**
 * Provider auth prompts: a 1:1 projection of pi-ai's AuthPrompt and AuthEvent, plus the one
 * event the interface needs and pi-ai has no name for — a prompt the provider resolved itself
 * (a callback winning the race), so the field it is holding can go.
 */
export type AuthEvent =
	| {
			kind: "prompt";
			promptId: string;
			promptType: "text" | "secret" | "select" | "manual_code";
			message: string;
			placeholder?: string;
			options?: readonly { id: string; label: string; description?: string }[];
	  }
	| { kind: "info"; message: string; links?: readonly { url: string; label?: string }[] }
	| { kind: "auth_url"; url: string; instructions?: string }
	| {
			kind: "device_code";
			userCode: string;
			verificationUri: string;
			intervalSeconds?: number;
			expiresInSeconds?: number;
	  }
	| { kind: "progress"; message: string }
	/** The provider resolved a prompt itself — a callback won the race — so the answer is gone. */
	| { kind: "prompt_cancelled"; promptId: string }
	| { kind: "done"; providerId: string }
	| { kind: "error"; message: string };

export type Frame =
	| { type: "response"; id: string; ok: true; data?: unknown }
	| { type: "response"; id: string; ok: false; error: string }
	| {
			type: "view";
			conversationId: string;
			seq: number;
			value: unknown;
			/** Our own annotation: the view mounts only durable's documents. */
			acornd?: {
				senders: Record<string, { conversationId: string; name?: string; kind: string }>;
			};
	  }
	| { type: "submission"; conversationId: string; submissionId: string; status: string }
	| { type: "auth"; authId: string; event: AuthEvent }
	| {
			type: "secret_request";
			id: string;
			conversationId: string;
			name: string;
			description?: string;
			hosts?: string[];
	  }
	| { type: "secret_list"; id: string; conversationId: string };

/** One line of JSON on stdout. A single write keeps frames from interleaving. */
export function emit(frame: Frame): void {
	process.stdout.write(`${JSON.stringify(frame)}\n`);
}

export function log(...parts: unknown[]): void {
	process.stderr.write(`${parts.map(String).join(" ")}\n`);
}

/** The result of reading one stdin line: a command, or the reason it is not one. */
export type ParsedCommand = { ok: true; command: Command } | { ok: false; reason: string };

/**
 * parseCommand turns one line of stdin into a command.
 *
 * `JSON.parse` returns `unknown`, and casting that to `Command` is the one place a value that is
 * not a command would be treated as one: the switch over `command.type` is exhaustive over the
 * union, not over what a line can contain. So `null` and `[]` reached `.type` outside every
 * handler, and the throw that followed ended the process — taking every conversation in it (T8).
 * The envelope is checked here, because an object with a string type and a string id is what every
 * member of the union has.
 */
export function parseCommand(line: string): ParsedCommand {
	let value: unknown;
	try {
		value = JSON.parse(line);
	} catch (error) {
		return { ok: false, reason: `not JSON: ${String(error)}` };
	}
	if (typeof value !== "object" || value === null || Array.isArray(value)) {
		return { ok: false, reason: "not an object" };
	}
	const fields = value as Record<string, unknown>;
	if (typeof fields.type !== "string" || fields.type === "") {
		return { ok: false, reason: "no type" };
	}
	if (typeof fields.id !== "string") {
		return { ok: false, reason: "no id" };
	}
	// The cast is the boundary this function exists to hold: the envelope is checked above, and
	// each command's own fields are read by the handler that owns them, which answers an error
	// rather than throwing.
	return { ok: true, command: value as Command };
}

/** Serializes command handling so a long command cannot reorder the stream. */
export class CommandQueue {
	#tail: Promise<unknown> = Promise.resolve();

	push(task: () => Promise<void>): void {
		this.#tail = this.#tail.then(task, task);
	}
}
