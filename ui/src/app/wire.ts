/**
 * The gateway's HTTP shapes, as this layer consumes them.
 *
 * The Go types in `gateway/internal/control` and `internal/protocol` are the definition;
 * this is the mirror a JSON client needs, kept to the fields the interface reads — a rename
 * on the wire should fail a typecheck here rather than a render later. The boundary is
 * trusted: it is our own gateway, and every consumer of these shapes maps them to a view
 * model in `map.ts` before a component sees them.
 */

/** One person in a room: their address, and what they are in it. A room has one owner and its guests
 * (ADR 0034 §3); the name is the book's, and the room's record keeps the address alone. */
export interface WireMember {
	email: string;
	/** `owner` or `guest`. */
	role: string;
}

export interface WireWorkspace {
	name: string;
	path: string;
	title: string;
	/** This workspace is somebody's own room: derived from the stored kind in the gateway. */
	personal: boolean;
	/** `personal` or `room`. A personal room is never shared and never forgotten (ADR 0034 §5). */
	kind?: string | undefined;
	/** Who is in it: the owner, and its guests. */
	members?: WireMember[] | undefined;
	image?: string | undefined;
	network?: string | undefined;
	/** Which sandbox engine this workspace runs on; absent means the install default. */
	engine?: string | undefined;
	/** Kept running when nobody is watching: a workspace with a daemon or a cron job. */
	keep_alive?: boolean | undefined;
	/** May reach private networks as this machine: its LAN and its tailnet. Off unless asked. */
	private?: boolean | undefined;
	running: boolean;
	/** Where this workspace is between "asked for" and "usable": stopped, opening, ready, or
	 * failed. `running` is only the harness being up; a workspace that is opening has no harness
	 * yet, which is exactly what the interface has to say. */
	status?: string | undefined;
	/** The step an opening workspace is on, in the words of the code doing it. */
	stage?: string | undefined;
	/** Why it failed, when it failed. */
	error?: string | undefined;
}

/** One workspace's start, as the status endpoint answers it: a cheap question that never starts
 * anything, so the interface can show a step instead of an empty pane. */
export interface WireWorkspaceStatus {
	status: string;
	stage?: string | undefined;
	since?: string | undefined;
	error?: string | undefined;
}

/**
 * What the caller may change in the room in view, and what they are in it.
 *
 * It is the state's copy of the gateway's one permission table, so a component disables a control rather
 * than reading a right out of a role — a second copy of that table is the bug it prevents (ADR 0034 §4).
 */
export interface WireCan {
	/** The install's permission, not the room's: may this caller lead a room of their own. */
	makeRoom: boolean;
	create: boolean;
	fork: boolean;
	hide: boolean;
	rename: boolean;
	model: boolean;
	/** The room is this caller's: they invite and remove guests. */
	administer: boolean;
}

/** One sandbox engine this install can run, and whether this machine can. */
export interface WireEngine {
	name: string;
	/** True for the engine a workspace with no engine of its own gets. */
	default?: boolean | undefined;
	isolated: boolean;
	installed: boolean;
	/** True when a secret's value stays on the host and only a placeholder enters the sandbox. */
	substitutes?: boolean | undefined;
	version?: string | undefined;
	/** Why this machine cannot run it, in a sentence. Present means it cannot. */
	problem?: string | undefined;
}

export interface WireConversation {
	id: string;
	title: string;
	root: boolean;
	hidden: boolean;
	running: boolean;
	lastEntryId: string;
	/** Last activity, Unix ms, from the newest entry's message timestamp. Absent
	 * while the conversation has no entries, so its row renders flat. */
	updatedAt?: number | undefined;
	tokens: number;
	cost: number;
	parent?: string | undefined;
	children?: string[] | undefined;
	model?: string | undefined;
	thinking?: string | undefined;
	createdBy?: string | undefined;
}

/** One staged file riding a draft: the id a message will carry, and the chip's words. */
export interface WireDraftAttachment {
	id: string;
	name: string;
	size: number;
	mime: string;
}

/** One room's unsent message, as the workspace holds it. The stamp is taken by the gateway: it
 * orders one machine's save against another's, so a console can tell its own echo from a write
 * it did not make. */
export interface WireDraft {
	text?: string | undefined;
	attachments?: WireDraftAttachment[] | undefined;
	updatedAt?: number | undefined;
}

/** The self-update summary, as the state stream carries it. */
export interface WireUpdateSummary {
	current: string;
	latest?: string | undefined;
	available: boolean;
	failed?: string | undefined;
	restartRequired?: boolean | undefined;
}

export interface WireSandboxSecret {
	name: string;
	description?: string | undefined;
	scope?: string | undefined;
	hosts?: string[] | undefined;
	delivery?: string | undefined;
	status?: string | undefined;
	/** The literal the guest holds on a substitute engine; empty for an env engine. */
	placeholder?: string | undefined;
}

export interface WireSandbox {
	engine?: string | undefined;
	version?: string | undefined;
	status?: string | undefined;
	tools: boolean;
	isolated: boolean;
	problem?: string | undefined;
	/** The workspace's secrets as the engine delivers them; names and status only. */
	secrets?: WireSandboxSecret[] | undefined;
	secretsRestart?: boolean | undefined;
	secretsProblem?: string | undefined;
}

export interface WireModel {
	id: string;
	name: string;
	contextWindow?: number | undefined;
	/** The levels this model supports; ["off"] means it cannot reason at all. */
	thinkingLevels?: string[] | undefined;
}

export interface WireProvider {
	id: string;
	name: string;
	authenticated: boolean;
	source?: string | undefined;
	/** How this install authenticated, when it did: key or sign-in. */
	authType?: "api_key" | "oauth" | undefined;
	/** Login methods the provider offers. Empty is ambient-only. */
	authTypes?: ("api_key" | "oauth")[] | undefined;
	models: WireModel[];
}

/** One step of a provider login, a 1:1 projection of the worker's auth frames. */
export type WireAuthEvent =
	| {
			kind: "prompt";
			promptId: string;
			promptType: "text" | "secret" | "select" | "manual_code";
			message: string;
			placeholder?: string | undefined;
			options?: { id: string; label: string; description?: string }[] | undefined;
	  }
	| {
			kind: "info";
			message: string;
			links?: { url: string; label?: string }[] | undefined;
	  }
	| { kind: "auth_url"; url: string; instructions?: string | undefined }
	| {
			kind: "device_code";
			userCode: string;
			verificationUri: string;
			intervalSeconds?: number | undefined;
			expiresInSeconds?: number | undefined;
	  }
	| { kind: "progress"; message: string }
	/** The provider resolved a prompt itself — a callback won the race — so it is no longer waiting. */
	| { kind: "prompt_cancelled"; promptId: string }
	| { kind: "done"; providerId: string }
	| { kind: "error"; message: string };

export interface WireAuthFrame {
	type: "auth";
	authId: string;
	event: WireAuthEvent;
}

/** The events a dialog renders as steps: prompts and outcomes are not notices. */
export type WireAuthNotice = Extract<
	WireAuthEvent,
	{ kind: "info" | "auth_url" | "device_code" | "progress" }
>;

export interface WireCapabilities {
	steer: boolean;
	subagents: boolean;
	sandbox: string;
}

/** What a conversation chose; empty fields are inheriting. */
export interface WireBinding {
	provider: string;
	modelId: string;
	thinkingLevel?: string | undefined;
	/** The level asked for when the model could not honour it, so the UI can say why. */
	thinkingRequested?: string | undefined;
	definition?: string | undefined;
}

export interface WireAgentSummary {
	name: string;
	description?: string | undefined;
	source: string;
	layer: string;
}

export interface WireCommands {
	commands: {
		name: string;
		description?: string | undefined;
		argumentHint?: string | undefined;
		source: string;
	}[];
}

export interface WireLayerTrace {
	layer: string;
	path: string;
	/** The layer's own contribution document, left raw on purpose. */
	contributes?: unknown;
}

export interface WireResolvedAgent {
	name: string;
	source?: string | undefined;
	model?: string | undefined;
	thinking?: string | undefined;
	layers?: WireLayerTrace[] | undefined;
}

export interface WireState {
	workspace: WireWorkspace;
	workspaces: WireWorkspace[];
	/** The engines the chooser offers, with this machine's answer for each. */
	engines?: WireEngine[] | undefined;
	conversation?: WireConversation | undefined;
	conversations: WireConversation[];
	/** The conversation's unsent message, if it ever had one. It is the workspace's, not the
	 * browser's: a half-typed line waits in the room on every machine. */
	draft?: WireDraft | undefined;
	workerUp: boolean;
	workerError?: string | undefined;
	sandbox: WireSandbox;
	/** The self-update summary: the header dot without fetching. */
	update: WireUpdateSummary;
	capabilities: WireCapabilities;
	harness: string;
	/** The effective binding: what will actually run, resolved across the layers. */
	agent: WireBinding;
	resolved?: WireResolvedAgent | undefined;
	defaultModel: WireBinding;
	defaultThinking: string;
	defaultAgent: string;
	/** Each scope's own layer, unresolved: what the settings screen shows per tab. */
	workspaceDefaults: WireScopeDefaults;
	installDefaults: WireScopeDefaults;
	/** True when another conversation created this one: its configuration is that one's. */
	subagent: boolean;
	/** What this caller may change here, and what they are in the room (ADR 0034 §4). */
	can: WireCan;
	/** `owner` or `guest` in the room in view; absent when the caller is not in it. */
	role?: string | undefined;
	/** The install's role, not the room's: it is what shows the People page (ADR 0034 §1). */
	admin: boolean;
	agents: WireAgentSummary[];
	providers: WireProvider[];
	seq: number;
}

/** One layer's own defaults. Empty means the layer says nothing, not that it agrees. */
export interface WireScopeDefaults {
	model?: WireBinding | undefined;
	thinking?: string | undefined;
	agent?: string | undefined;
}

export interface WireContentBlock {
	type: string;
	text?: string | undefined;
	thinking?: string | undefined;
	redacted?: boolean | undefined;
	id?: string | undefined;
	name?: string | undefined;
	arguments?: unknown;
}

/** A tool the model was given, as a system entry adds it. */
export interface WireTool {
	name: string;
	description?: string | undefined;
	parameters?: unknown;
}

/** What a provider reported for one call. */
export interface WireUsage {
	input: number;
	output: number;
	cacheRead?: number | undefined;
	cacheWrite?: number | undefined;
}

export interface WireMessage {
	content: string | WireContentBlock[];
	timestamp?: number | undefined;
	stopReason?: string | undefined;
	errorMessage?: string | undefined;
	provider?: string | undefined;
	model?: string | undefined;
	toolCallId?: string | undefined;
	toolName?: string | undefined;
	isError?: boolean | undefined;
	/** System entries carry the prompt as patches: `null` removes a section. */
	sections?: Record<string, string | null> | undefined;
	toolsAdded?: WireTool[] | undefined;
	toolsRemoved?: { name: string }[] | undefined;
	usage?: WireUsage | undefined;
}

export interface WireEntry {
	id: number;
	kind: string;
	model?: WireMessage[] | undefined;
	/**
	 * The payload an entry carries beside its messages: a notice's text, and the author of a message a
	 * person wrote. Entries written before authors were recorded carry none, and the interface shows
	 * nothing for them rather than a guess (ADR 0034 §7).
	 */
	data?: { text?: string | undefined; author?: WireAuthor | undefined } | undefined;
}

/** One record in the book of people: who may use this install, and what they may do on it (ADR 0032 §3). */
export interface WirePerson {
	email: string;
	name?: string | undefined;
	/** `admin` or `member`. */
	role: string;
	/** `invited`, `active`, or `revoked`. */
	state: string;
	/** What they may run, as `provider/modelId`. Absent is every model, which is what no list means. */
	models?: string[] | undefined;
	/** May they lead a room of their own (ADR 0034 §3). Absent is false. */
	can_create?: boolean | undefined;
	created?: string | undefined;
	last_seen?: string | undefined;
}

/** The book as every People route answers it: one document, after whatever the request changed. */
export interface WirePeople {
	people: WirePerson[];
}

/** Who wrote a message. `speaker` is the name the model reads, and it is empty in a personal room. */
export interface WireAuthor {
	email: string;
	name?: string | undefined;
	speaker?: string | undefined;
}

export interface WireToolSlot {
	callId: string;
	name: string;
	output?: string | undefined;
	status?: string | undefined;
	diagnostics?: { severity?: string | undefined }[] | undefined;
}

/** A message that has been accepted but not yet committed: it steers the run or waits behind it. */
export interface WireInboxItem {
	id: string;
	/** `write` items are the harness's own drafts, not something a person sent. */
	mode: "steer" | "followUp" | "write";
	content?: string | WireContentBlock[] | undefined;
}

export interface WireLive {
	generation?: { message?: WireMessage | undefined } | undefined;
	tools?: WireToolSlot[] | undefined;
}

export interface WireView {
	entries?: WireEntry[] | undefined;
	docs?:
		| {
				"pi.live"?: WireLive | undefined;
				"pi.inbox"?: { items?: WireInboxItem[] | undefined } | undefined;
		  }
		| undefined;
}

export interface WireSender {
	kind: "ask" | "report";
	name?: string | undefined;
	conversationId: string;
}

/** A tool's secret request or its outcome. The value is never in this event. */
export interface WireSecretEvent {
	kind: "request" | "resolved" | "cancelled";
	requestId: string;
	conversationId?: string | undefined;
	name?: string | undefined;
	description?: string | undefined;
	hosts?: string[] | undefined;
	scope?: "global" | "workspace" | undefined;
	restartRequired?: boolean | undefined;
	reason?: string | undefined;
}

export interface WireFrame {
	type: string;
	conversationId?: string | undefined;
	value?: WireView | undefined;
	acornfall?: { senders?: Record<string, WireSender> | undefined } | undefined;
	authId?: string | undefined;
	event?: WireAuthEvent | undefined;
	secret?: WireSecretEvent | undefined;
}
