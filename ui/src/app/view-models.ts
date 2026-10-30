/**
 * The shapes the screens render.
 *
 * The app layer maps the platform's protocol — state frames, entries, sandbox state — onto
 * these. A screen or a component never sees a wire type, a frame, or a fetch: it is handed a
 * view model and reports a callback. That is the boundary that makes every state renderable
 * in Storybook with a literal.
 */

/** What a workspace is: somebody's own room, or a room with an owner and its guests (ADR 0034 §5). */
export type RoomKind = "personal" | "room";

/** What somebody is in a room, and never what they are on the install (ADR 0034 §1). */
export type RoomRole = "owner" | "guest";

/** One person in a room. The room's record keeps the address; the name is the book's. */
export interface RoomMember {
	email: string;
	role: RoomRole;
}

/**
 * What the caller may change in the room in view: the state's copy of the gateway's one permission table.
 * A component reads this instead of inferring a right from a role (ADR 0034 §4).
 */
export interface RoomRights {
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

/** One selectable workspace: where it points, what runs it, and whether it is the one in view. */
export interface WorkspaceSummary {
	name: string;
	title: string;
	path: string;
	/** This workspace is somebody's own room: never shared, never forgotten. */
	personal: boolean;
	/** What it is, in the record's words. A room with one member is still a room. */
	kind: RoomKind;
	/** Who is in it: the owner first, then its guests. Empty on an install that has not claimed its admin. */
	members: RoomMember[];
	/** The worker process for this workspace is up. */
	running: boolean;
	active: boolean;
	image?: string | undefined;
	network?: string | undefined;
	/** The sandbox engine this workspace runs on, resolved: a workspace that names none gets the
	 * install's default, and the row says which that is rather than leaving a blank. */
	engine?: string | undefined;
	/** What the row shows for it: the product's name for the engine, or its own. */
	engineLabel?: string | undefined;
	/** Its sandbox is exempt from idle eviction: it runs something. */
	keepAlive?: boolean | undefined;
	/** Its sandbox may reach private networks as this machine: its LAN and its tailnet. */
	private?: boolean | undefined;
	/** stopped, opening, ready, or failed: where it is between being asked for and usable. */
	status?: string | undefined;
	/** The step an opening workspace is on, in the server's words. */
	stage?: string | undefined;
	/** Why it failed, when it failed. */
	error?: string | undefined;
}

/** One sandbox engine the create form offers. */
export interface EngineSummary {
	name: string;
	/** The engine a workspace with no choice of its own gets. */
	isDefault: boolean;
	/** False means this machine cannot run it; `problem` says why. */
	usable: boolean;
	/** True when choosing it starts with a download: the engine is not on the machine yet. */
	downloads: boolean;
	/** True when a secret's value stays on the host and only a placeholder enters the sandbox. */
	substitutes: boolean;
	problem?: string | undefined;
	/** A short word for a narrow control: the engine's own name, or its family. */
	label: string;
}

/** One secret's delivery, never its value. */
export interface SandboxSecretSummary {
	name: string;
	description?: string | undefined;
	scope?: string | undefined;
	hosts?: string[] | undefined;
	delivery?: string | undefined;
	status?: string | undefined;
	placeholder?: string | undefined;
}

/** What the agent's tools can reach, as the interface has to be able to say. */
export interface SandboxSummary {
	engine: string;
	version: string;
	status: string;
	/** False means the tools refuse rather than running on the host. */
	tools: boolean;
	/** False is the host engine: no isolation, and the interface has to say so. */
	isolated: boolean;
	problem?: string | undefined;
	/** The effective secrets, with blocked entries and their reasons. */
	secrets: SandboxSecretSummary[];
	/** True when the engine stored a change for the next start. */
	secretsRestart: boolean;
	secretsProblem?: string | undefined;
}

/** One conversation as the rail needs it. */
export interface ConversationSummary {
	id: string;
	title: string;
	/** Last activity, when the conversation has moved: the rail groups by it, and a
	 * conversation that never moved renders flat, above the groups. */
	updatedAt?: Date | undefined;
	/** The conversation the workspace started with. Identity is text, not a second dot. */
	root: boolean;
	/** Tombstoned, not deleted: the rail tucks it under "Deleted" and offers restore. */
	hidden: boolean;
	/** A run is underway right now. */
	running: boolean;
	/** Entries arrived since this conversation was last looked at. Derived by the app. */
	unread: boolean;
	/** Set when another conversation created this one: a subagent, which belongs to its parent. */
	parent?: string | undefined;
	/** Set when another conversation an agent created owns itself: a peer. */
	createdBy?: string | undefined;
	model?: string | undefined;
	thinking?: string | undefined;
	tokens: number;
	cost: number;
}

/** A message another conversation sent, which arrives as a user entry and must not be read as yours. */
export interface AgentSender {
	kind: "ask" | "report";
	name?: string | undefined;
	conversationId: string;
}

export type TranscriptEntry =
	| UserEntry
	| AssistantEntry
	| ToolEntry
	| ThinkingEntry
	| NoticeEntry
	| SummaryEntry
	| PendingEntry;

/**
 * The model's reasoning, when the provider shares it. It renders as a card that starts
 * open and collapses when done — evidence, not the reply. A redacted block carries no
 * words, only the fact that words were hidden.
 */
export interface ThinkingEntry {
	kind: "thinking";
	id: string;
	at?: Date | undefined;
	text: string;
	live?: boolean | undefined;
}

export interface UserEntry {
	kind: "user";
	id: string;
	text: string;
	at?: Date | undefined;
	/**
	 * How this message was sent while a turn was running: it joined the turn, or waited behind
	 * it. It is claimed by the interface as the message lands — the wire keeps no link between
	 * an inbox item and the entry it becomes — because a mark that dies with the pending card
	 * leaves the reader unable to say afterwards whether the model got it now or later.
	 */
	action?: BusyAction | undefined;
	/** Set when another conversation's words arrived here. */
	sender?: AgentSender | undefined;
	/**
	 * Who wrote this, when the transcript knows: absent for a message written before authors were recorded,
	 * and for an agent's. The interface shows nothing for an absent author rather than a guess (ADR 0034 §7).
	 */
	author?: { email: string; name?: string | undefined } | undefined;
	/** Set when this message can be branched from. */
	forkAt?: string | undefined;
}

export interface AssistantEntry {
	kind: "assistant";
	id: string;
	markdown: string;
	at?: Date | undefined;
	/** A provider failure: the entry has no content and this is the only explanation there is. */
	failure?: string | undefined;
	/** Still being written: render the text, but no controls until it settles. */
	live?: boolean | undefined;
}

export interface ToolEntry {
	kind: "tool";
	id: string;
	/**
	 * The provider's id for this call, when there is one. It is the same while the call runs and
	 * after it settles — the entry id is not: a live card is `live-<callId>` on the wire and the
	 * settled one is the transcript entry's id — so the fold a reader opened survives the settle.
	 */
	callId?: string | undefined;
	name: string;
	status: "running" | "done";
	isError: boolean;
	args?: unknown;
	output: string;
	at?: Date | undefined;
}

export interface NoticeEntry {
	kind: "notice";
	id: string;
	text: string;
	at?: Date | undefined;
}

/**
 * The note a compaction left behind: what the folded turns said, in the
 * summarizer's own words. Starts put away — the talk moved on.
 */
export interface SummaryEntry {
	kind: "summary";
	id: string;
	markdown: string;
	at?: Date | undefined;
}

/**
 * Something sent while a turn was running. It is real — the server took it — but it has not
 * committed yet, so the transcript says so instead of pretending it is not there.
 */
export interface PendingEntry {
	kind: "pending";
	id: string;
	text: string;
	action: "steer" | "followUp";
}

/** One model provider as the settings screen lists it. */ export interface ProviderSummary {
	id: string;
	name: string;
	authenticated: boolean;
	/** Where the working key came from, when one works: env var, OAuth, a file. */
	source?: string | undefined;
	/** How this install authenticated, when it did: typed key or sign-in. */
	authType?: "api_key" | "oauth" | undefined;
	/** Login methods on offer. Empty is ambient-only: the key lives
	 * outside the app, and there is nothing to type. */
	authTypes: ("api_key" | "oauth")[];
	/** Models on offer once connected. A count, not a list: the picker owns the list. */
	modelCount: number;
}

/** How an install's book may answer for a person: a state, not a role (ADR 0034 §2). */
export type PersonState = "invited" | "active" | "revoked";

/** What a record may do on the install, and never what it may do in a room. */
export type InstallRole = "admin" | "member";

/** One person in the book, as the People page lists them. */
export interface PersonSummary {
	email: string;
	name?: string | undefined;
	role: InstallRole;
	state: PersonState;
	/** What they may run, as `provider/modelId`. Empty is every model, which is what no list means. */
	models: string[];
	/** May they lead a room of their own (ADR 0034 §3). */
	canCreate: boolean;
	created?: string | undefined;
	lastSeen?: string | undefined;
}

/** One choice in a picker: the value is what the wire takes, the label is what a person reads. */
export interface PickerOption {
	value: string;
	label: string;
	detail?: string | undefined;
	/** A sentence under the label, for choices whose name is not the whole story. */
	description?: string | undefined;
}

export interface PickerGroup {
	label?: string | undefined;
	options: PickerOption[];
}

/** One picker's whole state: the value, where it came from, and what choosing does. */
export interface ChoicePicker {
	value: string;
	origin?: string | undefined;
	note?: string | undefined;
	/** The pinned value is not something this install can run any more: a model that went
	 * away, a provider that is no longer connected, a definition that was deleted. */
	invalid?: boolean | undefined;
	groups: PickerGroup[];
	disabled?: boolean | undefined;
	onSelect: (value: string) => void;
}

/** A slash command the server would expand; the interface only offers the list. */
export interface SlashCommand {
	name: string;
	description?: string | undefined;
	argumentHint?: string | undefined;
	source: string;
}

/** How a turn is sent while one is already running. */
export type BusyAction = "steer" | "followUp";

/** One selectable model, flattened from the providers that are actually authenticated. */
export interface ModelChoice {
	/** `provider/modelId`, the value the binding takes. */
	value: string;
	provider: string;
	modelId: string;
	label: string;
	/** The levels this model supports; the picker never offers one that is a lie. */
	thinkingLevels: string[];
}
