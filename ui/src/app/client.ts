import type { SlashCommand } from "./view-models";
import type {
	WireAuthEvent,
	WireCommands,
	WireDraft,
	WireDraftAttachment,
	WireFrame,
	WireSecretEvent,
	WireSender,
	WireState,
	WireView,
	WireWorkspaceStatus,
} from "./wire";

/**
 * The gateway client. One fetch for state, one EventSource for everything after it, and the
 * command endpoints. This is the only module in the interface that knows a URL.
 */

function workspacePath(workspace: string, tail: string): string {
	return `/api/workspaces/${encodeURIComponent(workspace)}${tail}`;
}

export async function fetchState(workspace: string, conversation: string): Promise<WireState> {
	const query = `workspace=${encodeURIComponent(workspace)}&conversation=${encodeURIComponent(conversation)}`;
	const response = await fetch(`/api/state?${query}`);
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as WireState;
}

/**
 * Where a workspace is, without opening it. The state request starts a workspace and can take
 * seconds; this one answers immediately, so the interface can say what is happening meanwhile.
 */
export async function fetchWorkspaceStatus(workspace: string): Promise<WireWorkspaceStatus> {
	const response = await fetch(workspacePath(workspace, "/status"));
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as WireWorkspaceStatus;
}

/** One piece of the door: what the apply did with it, and what it is. */
export interface RemoteStep {
	name: string;
	created: boolean;
	moved?: boolean | undefined;
	detail?: string | undefined;
}

/** What the install recorded about how a browser reaches it. Never a token: the API token is used and dropped. */
export interface RemoteStatus {
	configured: boolean;
	hostname?: string | undefined;
	zoneId?: string | undefined;
	accountId?: string | undefined;
	audience?: string | undefined;
	updated?: string | undefined;
	tunnelId?: string | undefined;
	steps?: RemoteStep[] | undefined;
}

/** A provider with every model it has, for choosing what the install may spend on it. */
export interface ProviderCatalogEntry {
	id: string;
	name: string;
	models: { id: string; name: string }[];
}

/** Every provider and every model: the list the owner chooses from, never the narrowed one. */
export async function fetchProviderCatalog(): Promise<ProviderCatalogEntry[]> {
	const response = await fetch("/api/providers/catalog");
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	const body = (await response.json()) as { providers?: ProviderCatalogEntry[] };
	return body.providers ?? [];
}

/** What this install may use from each provider: `{openai: ["gpt-5"]}`. Absent means every model it has. */
export async function fetchProviderAllowlist(): Promise<Record<string, string[]>> {
	const response = await fetch("/api/providers/allowlist");
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	const body = (await response.json()) as { providers?: Record<string, string[]> };
	return body.providers ?? {};
}

/** Write one provider's list. An empty list puts it back to "every model it has". */
export async function saveProviderAllowlist(
	provider: string,
	models: string[],
): Promise<Record<string, string[]>> {
	const response = await fetch("/api/providers/allowlist", {
		method: "PUT",
		headers: { "content-type": "application/json" },
		body: JSON.stringify({ provider, models }),
	});
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	const body = (await response.json()) as { providers?: Record<string, string[]> };
	return body.providers ?? {};
}

/** What a check of the door found, from outside: the same request an unauthenticated browser makes. */
export interface DoorReport {
	guarded: boolean;
	status: number;
	login?: string | undefined;
	keys: boolean;
	detail: string;
}

/** Ask, from outside, whether the door opens and whether it is guarded. Needs no API token. */
export async function checkRemote(): Promise<DoorReport> {
	const response = await fetch("/api/remote/check", { method: "POST" });
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as DoorReport;
}

/** The door as it stands: what is recorded, and whether anything has been made yet. */
export async function fetchRemote(): Promise<RemoteStatus> {
	const response = await fetch("/api/remote");
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as RemoteStatus;
}

/**
 * Make Cloudflare match what the owner types. The token is the account's, the call uses it and nothing keeps
 * it — so it travels in the body of this one request and never comes back.
 */
export async function applyRemote(request: {
	hostname: string;
	zone: string;
	token?: string | undefined;
}): Promise<RemoteStatus> {
	const response = await fetch("/api/remote/apply", {
		method: "POST",
		headers: { "content-type": "application/json" },
		body: JSON.stringify(request),
	});
	if (!response.ok) {
		// A failed apply still carries what it made, because the screen shows the steps it took.
		const text = (await response.text()).trim();
		throw new Error(text || response.statusText);
	}
	return (await response.json()) as RemoteStatus;
}

export interface StreamHandlers {
	onOpen: () => void;
	onState: (state: WireState) => void;
	onView: (view: WireView, senders: Record<string, WireSender>) => void;
	onAuth?: ((authId: string, event: WireAuthEvent) => void) | undefined;
	onSecret?: ((event: WireSecretEvent) => void) | undefined;
	onReconnecting: () => void;
}

/** What a caller holds: one close, whatever is going on underneath. */
export interface StreamHandle {
	close: () => void;
}

/** How the stream comes back. Milliseconds, and tests bring their own. */
export interface StreamTiming {
	retryMin: number;
	retryMax: number;
	/** A stream that says nothing for this long is dead, whatever the socket claims. */
	quiet: number;
	/** How often the quiet above is checked. */
	poll: number;
}

const defaultTiming: StreamTiming = {
	retryMin: 1000,
	retryMax: 30_000,
	quiet: 20_000,
	poll: 5_000,
};

/**
 * Opens the stream, and keeps it open.
 *
 * The browser reconnects an EventSource after a network blip, but it gives up for good when the
 * response is anything other than a live event stream — and a 502 is exactly what a gateway that
 * is restarting answers with. That is the failure this loop exists for: a page that went quiet
 * during a deploy and never came back, with a transcript that stopped moving while the console
 * kept working for everybody else.
 *
 * Two other things are deliberate. A stream is declared dead when its pings stop rather than when
 * its socket closes, because a connector swapped underneath leaves a socket that is open and
 * silent. And a stream that comes back refetches the state it missed: frames are not replayed, so
 * reconnecting without catching up would leave the page showing the last thing it saw before the
 * restart, which looks exactly like a page that is still broken.
 */
export function openStream(
	workspace: string,
	conversation: string,
	handlers: StreamHandlers,
	timing: Partial<StreamTiming> = {},
): StreamHandle {
	const clock = { ...defaultTiming, ...timing };
	const query = `workspace=${encodeURIComponent(workspace)}&conversation=${encodeURIComponent(conversation)}`;
	let source: EventSource | undefined;
	let closed = false;
	let attempt = 0;
	let retry: ReturnType<typeof setTimeout> | undefined;
	let lastSeen = Date.now();
	// Every connection is a generation: a listener left over from a socket that was replaced must
	// never feed the page.
	let generation = 0;

	const catchUp = async (current: number): Promise<void> => {
		try {
			const state = await fetchState(workspace, conversation);
			if (closed || current !== generation) return;
			handlers.onState(state);
		} catch {
			// The stream is the thing that reports a workspace it cannot read; a failed catch-up
			// here would only say the same sentence twice.
		}
	};

	const connect = (): void => {
		if (closed) return;
		const current = ++generation;
		lastSeen = Date.now();
		const next = new EventSource(`/api/stream?${query}`);
		source = next;
		const mine = (): boolean => !closed && current === generation;
		next.addEventListener("ping", () => {
			if (mine()) lastSeen = Date.now();
		});
		next.addEventListener("open", () => {
			if (!mine()) return;
			attempt = 0;
			lastSeen = Date.now();
			handlers.onOpen();
			void catchUp(current);
		});
		next.addEventListener("state", (event) => {
			if (!mine()) return;
			handlers.onState(JSON.parse((event as MessageEvent<string>).data) as WireState);
		});
		next.addEventListener("frame", (event) => {
			if (!mine()) return;
			const frame = JSON.parse((event as MessageEvent<string>).data) as WireFrame;
			if (frame.type === "auth" && frame.authId !== undefined && frame.event !== undefined) {
				handlers.onAuth?.(frame.authId, frame.event);
				return;
			}
			if (frame.type === "secret" && frame.secret !== undefined) {
				handlers.onSecret?.(frame.secret);
				return;
			}
			if (frame.type !== "view" || frame.value === undefined) return;
			handlers.onView(frame.value, frame.acornfall?.senders ?? {});
		});
		next.addEventListener("error", () => {
			if (!mine()) return;
			handlers.onReconnecting();
			// The socket's own retry is left to itself while it is still trying; a socket the
			// browser has closed for good is ours to replace.
			if (next.readyState === 2) schedule();
		});
	};

	const schedule = (): void => {
		if (closed) return;
		source?.close();
		source = undefined;
		generation += 1;
		if (retry !== undefined) return;
		const wait = Math.min(clock.retryMax, clock.retryMin * 2 ** attempt);
		attempt += 1;
		retry = setTimeout(() => {
			retry = undefined;
			connect();
		}, wait);
	};

	const watchdog = setInterval(() => {
		if (closed) return;
		if (Date.now() - lastSeen < clock.quiet) return;
		handlers.onReconnecting();
		schedule();
	}, clock.poll);

	connect();

	return {
		close: () => {
			closed = true;
			generation += 1;
			if (retry !== undefined) clearTimeout(retry);
			clearInterval(watchdog);
			source?.close();
			source = undefined;
		},
	};
}

async function post(path: string, body: unknown): Promise<void> {
	const response = await fetch(path, {
		method: "POST",
		headers: { "content-type": "application/json" },
		body: JSON.stringify(body ?? {}),
	});
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
}

export function sendMessage(
	workspace: string,
	conversation: string,
	text: string,
	whenBusy: "steer" | "followUp",
	attachments?: string[] | undefined,
): Promise<void> {
	return post(
		workspacePath(workspace, `/conversations/${encodeURIComponent(conversation)}/messages`),
		{
			text,
			whenBusy,
			attachments,
		},
	);
}

/**
 * Record one conversation's unsent message: the words, and the staged files riding them.
 * `keepalive` is for the write that has to leave while the page does — a reload mid-sentence
 * is the reload the draft exists for. The answer is what was stored, stamp included.
 */
export async function saveDraft(
	workspace: string,
	conversation: string,
	draft: { text: string; attachments: WireDraftAttachment[] },
	keepalive = false,
): Promise<WireDraft> {
	const response = await fetch(
		workspacePath(workspace, `/conversations/${encodeURIComponent(conversation)}/draft`),
		{
			method: "POST",
			headers: { "content-type": "application/json" },
			body: JSON.stringify(draft),
			keepalive,
		},
	);
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as WireDraft;
}

export function renameConversation(
	workspace: string,
	conversation: string,
	title: string,
): Promise<void> {
	return post(
		workspacePath(workspace, `/conversations/${encodeURIComponent(conversation)}/rename`),
		{
			title,
		},
	);
}

export function hideConversation(
	workspace: string,
	conversation: string,
	hidden: boolean,
): Promise<void> {
	return post(
		workspacePath(workspace, `/conversations/${encodeURIComponent(conversation)}/hide`),
		{
			hidden,
		},
	);
}

export function forkConversation(
	workspace: string,
	conversation: string,
	at: string,
): Promise<void> {
	return post(
		workspacePath(workspace, `/conversations/${encodeURIComponent(conversation)}/fork`),
		{ at },
	);
}

export function compactConversation(workspace: string, conversation: string): Promise<void> {
	return post(
		workspacePath(workspace, `/conversations/${encodeURIComponent(conversation)}/compact`),
		{},
	);
}

export interface CompactPreview {
	conversationId: string;
	entries: number;
	turns: number;
}

/** What a manual compaction would fold, so the dialog states the scope. */
export function fetchCompactPreview(
	workspace: string,
	conversation: string,
): Promise<CompactPreview> {
	return postJson<CompactPreview>(
		workspacePath(
			workspace,
			`/conversations/${encodeURIComponent(conversation)}/compact-preview`,
		),
		{},
	);
}

export function abortConversation(workspace: string, conversation: string): Promise<void> {
	return post(
		workspacePath(workspace, `/conversations/${encodeURIComponent(conversation)}/abort`),
		{},
	);
}

/** What this conversation chose. An absent field is left alone; an empty string clears it. */
export interface AgentChange {
	provider?: string;
	modelId?: string;
	thinkingLevel?: string;
	definition?: string;
}

/** Applied changes answer with the new state, so a picker does not need a second round trip. */
export async function setAgent(
	workspace: string,
	conversation: string,
	change: AgentChange,
): Promise<WireState> {
	const response = await fetch(
		workspacePath(workspace, `/conversations/${encodeURIComponent(conversation)}/agent`),
		{
			method: "POST",
			headers: { "content-type": "application/json" },
			body: JSON.stringify(change),
		},
	);
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as WireState;
}

/**
 * Put somebody the book already names into a room. Only the room's owner may invite, and the answer is the
 * workspace listing: the room's members changed, and the screen that shows them is the one that asked.
 *
 * An address the book does not hold is a 400 with the sentence that says to ask the admin, so the refusal is
 * shown rather than swallowed — never inviting somebody into a room that would refuse them (ADR 0034 §1).
 */
export async function addMember(workspace: string, email: string): Promise<void> {
	const response = await fetch(workspacePath(workspace, "/members"), {
		method: "POST",
		headers: { "content-type": "application/json" },
		body: JSON.stringify({ email }),
	});
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
}

/** Take a guest out of a room, or let a guest leave. The owner cannot be removed: ending a room is forgetting it. */
export async function removeMember(workspace: string, email: string): Promise<void> {
	const response = await fetch(
		workspacePath(workspace, `/members/${encodeURIComponent(email)}`),
		{ method: "DELETE" },
	);
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
}

export function removeWorkspace(name: string): Promise<void> {
	return post(`/api/workspaces/${encodeURIComponent(name)}/remove`, {});
}

/**
 * A new workspace, with the engine chosen before it exists: the engine decides what a
 * workspace's sandbox is, and a workspace that wanted a microVM should not have to run once
 * on another engine to change it.
 */
export function createWorkspace(name: string, engine?: string): Promise<void> {
	return post("/api/workspaces", {
		name,
		...(engine === undefined || engine === "" ? {} : { engine }),
	});
}

/** A new conversation, created and selected; the answer is the state it is now in. */
export async function createConversation(workspace: string): Promise<WireState> {
	const response = await fetch(workspacePath(workspace, "/conversations"), { method: "POST" });
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as WireState;
}

/** A partial update: an absent field is left alone, so `image` and `network` are independent. */
export interface WorkspaceChange {
	title?: string;
	image?: string;
	network?: string;
	/** Kept running when idle: for a workspace that runs a daemon or a cron job. */
	keepAlive?: boolean;
	/** May reach private networks as this machine; absent leaves the workspace's setting alone. */
	private?: boolean;
}

export function updateWorkspace(workspace: string, change: WorkspaceChange): Promise<void> {
	// The wire says keep_alive, the interface says keepAlive: one conversion, here.
	const { keepAlive, ...rest } = change;
	const body = keepAlive === undefined ? rest : { ...rest, keep_alive: keepAlive };
	return post(workspacePath(workspace, "/update"), body);
}

/** Where a default is being set: this workspace, or every workspace. */
export type DefaultScope = "workspace" | "install";

function defaultPath(workspace: string, scope: DefaultScope, kind: string): string {
	return scope === "install"
		? `/api/defaults/${kind}`
		: workspacePath(workspace, `/default-${kind}`);
}

export function setDefaultModel(
	workspace: string,
	scope: DefaultScope,
	value: string,
): Promise<void> {
	const slash = value.indexOf("/");
	return post(defaultPath(workspace, scope, "model"), {
		provider: slash === -1 ? "" : value.slice(0, slash),
		modelId: slash === -1 ? "" : value.slice(slash + 1),
	});
}

export function setDefaultThinking(
	workspace: string,
	scope: DefaultScope,
	level: string,
): Promise<void> {
	return post(defaultPath(workspace, scope, "thinking"), { level });
}

export function setDefaultAgent(
	workspace: string,
	scope: DefaultScope,
	definition: string,
): Promise<void> {
	return post(defaultPath(workspace, scope, "agent"), { definition });
}

export async function fetchCommands(workspace: string): Promise<SlashCommand[]> {
	const response = await fetch(workspacePath(workspace, "/commands"));
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	const body = (await response.json()) as WireCommands;
	return body.commands;
}

/** The login methods a provider offers. Empty is ambient-only: nothing to type. */
export type ProviderAuthType = "api_key" | "oauth";

/** One stored file, answered with the id messages carry. */
export interface UploadedAttachment {
	id: string;
	name: string;
	size: number;
	mime: string;
}

/** Stage one file under the workspace's hidden folder. */
export async function uploadAttachment(workspace: string, file: File): Promise<UploadedAttachment> {
	const body = new FormData();
	body.append("file", file, file.name);
	const response = await fetch(workspacePath(workspace, "/attachments"), {
		method: "POST",
		body,
	});
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as UploadedAttachment;
}

/** Start a login flow. Prompts arrive as auth frames on the stream. Answers with the flow id. */
export async function startProviderLogin(
	workspace: string,
	providerId: string,
	authType: ProviderAuthType,
): Promise<string> {
	const response = await fetch(workspacePath(workspace, "/providers/login"), {
		method: "POST",
		headers: { "content-type": "application/json" },
		body: JSON.stringify({ providerId, authType }),
	});
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return ((await response.json()) as { authId: string }).authId;
}

/** Forget one provider's stored key. Ambient keys cannot be removed this way. */
export function logoutProvider(workspace: string, providerId: string): Promise<void> {
	return post(workspacePath(workspace, "/providers/logout"), { providerId });
}

/** Re-read the provider catalog, for a key added outside the app. */
export function refreshProviders(workspace: string): Promise<void> {
	return post(workspacePath(workspace, "/providers/refresh"), {});
}

/** Answer one login prompt. A blank answer with cancelled rejects it. */
export function answerAuthPrompt(
	workspace: string,
	authId: string,
	promptId: string,
	value: string,
	cancelled = false,
): Promise<void> {
	return post(workspacePath(workspace, "/auth/answer"), { authId, promptId, value, cancelled });
}

/** Abort a running login flow. */
export function cancelAuthFlow(workspace: string, authId: string): Promise<void> {
	return post(workspacePath(workspace, "/auth/cancel"), { authId });
}

/** The release page, as the gateway reports it. `available` means the newest GA is newer. */
export interface UpdateStatus {
	current: string;
	latest: string;
	notes: string;
	channel: string;
	autoCheck: boolean;
	checking: boolean;
	/** Download sizes in bytes, for the size line. Zero when unknown. */
	downloadSize: number;
	workerSize: number;
	available: boolean;
	failed?: string | undefined;
	restartRequired?: boolean | undefined;
	backups: string[];
	/** The gateway's own runs gate, and what stopping would stop. */
	runsActive: boolean;
	runs: { workspace: string; runs: number }[];
}

/** One release, as the picker and the version page read it. */
export interface UpdateRelease {
	tag: string;
	name: string;
	notes: string;
	publishedAt: string;
	prerelease: boolean;
	downloadSize: number;
	workerSize: number;
	/** What it is next to the running build: running, newer, older, unknown. */
	relation: "running" | "newer" | "older" | "unknown";
}

async function postJson<T>(path: string, body: unknown): Promise<T> {
	const response = await fetch(path, {
		method: "POST",
		headers: { "content-type": "application/json" },
		body: JSON.stringify(body ?? {}),
	});
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as T;
}

export async function fetchUpdateStatus(): Promise<UpdateStatus> {
	const response = await fetch("/api/update/status");
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as UpdateStatus;
}

/** The last twenty releases, newest first. `prerelease` widens the list for one visit. */
export async function fetchReleases(
	includePrerelease: boolean,
	limit = 20,
): Promise<UpdateRelease[]> {
	const query = new URLSearchParams({ limit: String(limit) });
	if (includePrerelease) query.set("prerelease", "1");
	const response = await fetch(`/api/update/releases?${query}`);
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	const body = (await response.json()) as { releases: UpdateRelease[] };
	return body.releases;
}

/** The notes of several releases in one read: the page's "changes in between". */
export async function fetchNotes(tags: string[]): Promise<Record<string, string>> {
	if (tags.length === 0) return {};
	const query = new URLSearchParams({ tags: tags.join(",") });
	const response = await fetch(`/api/update/notes?${query}`);
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	const body = (await response.json()) as { notes: Record<string, string> };
	return body.notes;
}

/** One release by name: a version outside the twenty, and the running row. */
export async function fetchRelease(tag: string): Promise<UpdateRelease> {
	const response = await fetch(`/api/update/releases/${encodeURIComponent(tag)}`);
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as UpdateRelease;
}

/** Force a release check now. */
export function checkUpdate(): Promise<UpdateStatus> {
	return postJson<UpdateStatus>("/api/update/check", {});
}

/** Back up, download, verify, swap. The process stays old until it restarts. */
export function applyUpdate(
	cleanVMs: boolean,
	drain: "wait" | "stop",
	tag = "",
): Promise<UpdateStatus> {
	return postJson<UpdateStatus>("/api/update/apply", { cleanVMs, drain, tag });
}

/** Finish a pending update: the process replaces itself with the binary on disk. */
export function restartUpdate(): Promise<UpdateStatus> {
	return postJson<UpdateStatus>("/api/update/restart", {});
}

/** Restore the last good build. */
export function rollbackUpdate(): Promise<UpdateStatus> {
	return postJson<UpdateStatus>("/api/update/rollback", {});
}

export interface UpdatePrefs {
	channel?: string;
	autoCheck?: boolean;
}

/** The install-wide settings page: channel and auto-check. There is no pin. */
export function setUpdatePrefs(prefs: UpdatePrefs): Promise<UpdateStatus> {
	return postJson<UpdateStatus>("/api/update/settings", prefs);
}

/* ---------- tool secrets ---------- */

export type SecretScope = "global" | "workspace";

export interface SecretSubstitution {
	headers: boolean;
	query: boolean;
	body: boolean;
}

/** A secret as the API returns it: everything but the value. */
export interface SecretMeta {
	name: string;
	description: string;
	scope: SecretScope;
	workspace?: string | undefined;
	hosts: string[];
	substitution: SecretSubstitution;
	allowEnv: boolean;
	rotatedAt?: string | undefined;
	/** Set on a workspace entry whose name also exists globally. */
	overrides?: boolean | undefined;
	/** Set on a global entry that a workspace entry replaces. */
	overridden?: boolean | undefined;
	/** On an effective entry: which scope won. */
	source?: SecretScope | undefined;
}

export interface WorkspaceSecrets {
	workspace: { name: string; path: string };
	global: SecretMeta[];
	own: SecretMeta[];
	effective: SecretMeta[];
}

export interface SecretInput {
	name: string;
	description: string;
	value: string;
	hosts: string[];
	substitution: SecretSubstitution;
	allowEnv: boolean;
}

function secretsPath(scope: SecretScope, workspace: string, tail = ""): string {
	if (scope === "global") return `/api/secrets${tail}`;
	return workspacePath(workspace, `/secrets${tail}`);
}

async function readJSON<T>(path: string, method: string, body?: unknown): Promise<T> {
	const init: RequestInit = { method };
	if (body !== undefined) {
		init.headers = { "content-type": "application/json" };
		init.body = JSON.stringify(body);
	}
	const response = await fetch(path, init);
	if (!response.ok) throw new Error((await response.text()).trim() || response.statusText);
	return (await response.json()) as T;
}

export async function fetchGlobalSecrets(): Promise<SecretMeta[]> {
	const payload = await readJSON<{ secrets: SecretMeta[] }>("/api/secrets", "GET");
	return payload.secrets;
}

export async function fetchWorkspaceSecrets(workspace: string): Promise<WorkspaceSecrets> {
	return readJSON<WorkspaceSecrets>(secretsPath("workspace", workspace), "GET");
}

export async function saveSecret(
	scope: SecretScope,
	workspace: string,
	input: SecretInput,
): Promise<SecretMeta> {
	const payload = await readJSON<{ secret: SecretMeta }>(
		secretsPath(scope, workspace),
		"POST",
		input,
	);
	return payload.secret;
}

export async function rotateSecret(
	scope: SecretScope,
	workspace: string,
	name: string,
	value: string,
): Promise<SecretMeta> {
	const payload = await readJSON<{ secret: SecretMeta }>(
		secretsPath(scope, workspace, `/${encodeURIComponent(name)}/rotate`),
		"POST",
		{ value },
	);
	return payload.secret;
}

export async function deleteSecret(
	scope: SecretScope,
	workspace: string,
	name: string,
): Promise<void> {
	await readJSON<{ deleted: string }>(
		secretsPath(scope, workspace, `/${encodeURIComponent(name)}`),
		"DELETE",
	);
}

export interface SecretAnswerInput {
	value: string;
	description: string;
	hosts: string[];
	scope: "workspace" | "global";
}

export async function answerSecretRequest(
	workspace: string,
	conversation: string,
	requestId: string,
	input: SecretAnswerInput,
): Promise<{ restartRequired?: boolean | undefined }> {
	return readJSON(
		workspacePath(
			workspace,
			`/conversations/${encodeURIComponent(conversation)}/secrets/${encodeURIComponent(requestId)}`,
		),
		"POST",
		input,
	);
}

export async function cancelSecretRequest(
	workspace: string,
	conversation: string,
	requestId: string,
): Promise<void> {
	await readJSON(
		workspacePath(
			workspace,
			`/conversations/${encodeURIComponent(conversation)}/secrets/${encodeURIComponent(requestId)}`,
		),
		"POST",
		{ cancelled: true },
	);
}
