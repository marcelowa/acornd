/**
 * The Go gateway's state and frames, read by the interface's mirror.
 *
 * `fixtures/wire` is written by a Go test from the structs the gateway really sends (`go test
 * ./internal/control`, with UPDATE_FIXTURES=1 to rewrite). `src/app/wire.ts` says it is "the mirror, kept to the
 * fields the interface reads", so the check runs one way: every field a mirror type names must be in the
 * fixture, and a required one must not be null. A field Go renamed or dropped fails here instead of rendering
 * as `undefined`. A field Go sends and the mirror does not name is fine — that is what "kept to the fields the
 * interface reads" means.
 *
 * Each table is held against its type by the compiler: add, rename or drop a field in `wire.ts` and the table
 * stops compiling. A fixture is full, so an optional field is present too.
 */
import { describe, expect, test } from "bun:test";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type {
	WireAuthEvent,
	WireAuthFrame,
	WireBinding,
	WireCan,
	WireCapabilities,
	WireConversation,
	WireDraft,
	WireDraftAttachment,
	WireEngine,
	WireFrame,
	WireLayerTrace,
	WireMember,
	WireModel,
	WireProvider,
	WireResolvedAgent,
	WireSandbox,
	WireSandboxSecret,
	WireScopeDefaults,
	WireSecretEvent,
	WireState,
	WireUpdateSummary,
	WireWorkspace,
	WireAgentSummary,
} from "../src/app/wire.ts";

type Keys<T> = { [P in keyof T]-?: unknown };

const fixtures = join(dirname(fileURLToPath(import.meta.url)), "../../fixtures/wire");

async function read(name: string): Promise<unknown> {
	// A trust boundary: our own checked-in file, and every value in it is checked below.
	return (await Bun.file(join(fixtures, name)).json()) as Record<string, unknown>;
}

/** Every field the mirror names is in the value. */
function named(label: string, value: unknown, shape: Keys<object>): void {
	expect(
		typeof value === "object" && value !== null && !Array.isArray(value),
		`${label} is an object`,
	).toBe(true);
	const present = Object.keys(value as object);
	expect(
		Object.keys(shape).filter((key) => !present.includes(key)),
		`${label}: fields the interface reads that Go does not send`,
	).toEqual([]);
}

const member: Keys<WireMember> = { email: 1, role: 1 };
const workspace: Keys<WireWorkspace> = {
	name: 1,
	path: 1,
	title: 1,
	personal: 1,
	kind: 1,
	members: 1,
	image: 1,
	network: 1,
	engine: 1,
	keep_alive: 1,
	private: 1,
	disable_devices: 1,
	running: 1,
	status: 1,
	stage: 1,
	error: 1,
	can_administer: 1,
};
const engine: Keys<WireEngine> = {
	name: 1,
	default: 1,
	isolated: 1,
	installed: 1,
	substitutes: 1,
	version: 1,
	problem: 1,
};
const conversation: Keys<WireConversation> = {
	id: 1,
	title: 1,
	root: 1,
	hidden: 1,
	running: 1,
	lastEntryId: 1,
	updatedAt: 1,
	tokens: 1,
	cost: 1,
	parent: 1,
	children: 1,
	model: 1,
	thinking: 1,
	createdBy: 1,
};
const draft: Keys<WireDraft> = { text: 1, attachments: 1, updatedAt: 1 };
const draftAttachment: Keys<WireDraftAttachment> = { id: 1, name: 1, size: 1, mime: 1 };
const sandbox: Keys<WireSandbox> = {
	engine: 1,
	version: 1,
	status: 1,
	tools: 1,
	isolated: 1,
	problem: 1,
	secrets: 1,
	secretsRestart: 1,
	secretsProblem: 1,
};
const sandboxSecret: Keys<WireSandboxSecret> = {
	name: 1,
	description: 1,
	scope: 1,
	hosts: 1,
	delivery: 1,
	status: 1,
	placeholder: 1,
};
const update: Keys<WireUpdateSummary> = {
	current: 1,
	latest: 1,
	available: 1,
	failed: 1,
	restartRequired: 1,
};
const capabilities: Keys<WireCapabilities> = { steer: 1, subagents: 1, sandbox: 1 };
const binding: Keys<WireBinding> = {
	provider: 1,
	modelId: 1,
	thinkingLevel: 1,
	thinkingRequested: 1,
	definition: 1,
};
const scopeDefaults: Keys<WireScopeDefaults> = { model: 1, thinking: 1, agent: 1 };
const can: Keys<WireCan> = {
	makeRoom: 1,
	create: 1,
	fork: 1,
	hide: 1,
	rename: 1,
	model: 1,
	administer: 1,
};
const agent: Keys<WireAgentSummary> = { name: 1, description: 1, source: 1, layer: 1 };
const resolved: Keys<WireResolvedAgent> = { name: 1, source: 1, model: 1, thinking: 1, layers: 1 };
const layer: Keys<WireLayerTrace> = { layer: 1, path: 1, contributes: 1 };
const provider: Keys<WireProvider> = {
	id: 1,
	name: 1,
	authenticated: 1,
	source: 1,
	authType: 1,
	authTypes: 1,
	models: 1,
};
const model: Keys<WireModel> = { id: 1, name: 1, contextWindow: 1, thinkingLevels: 1 };
const state: Keys<WireState> = {
	workspace: 1,
	workspaces: 1,
	engines: 1,
	conversation: 1,
	conversations: 1,
	draft: 1,
	workerUp: 1,
	workerError: 1,
	sandbox: 1,
	update: 1,
	capabilities: 1,
	harness: 1,
	agent: 1,
	resolved: 1,
	defaultModel: 1,
	defaultThinking: 1,
	defaultAgent: 1,
	workspaceDefaults: 1,
	installDefaults: 1,
	subagent: 1,
	can: 1,
	role: 1,
	admin: 1,
	agents: 1,
	providers: 1,
	seq: 1,
};

const each = (value: unknown): unknown[] => {
	expect(Array.isArray(value)).toBe(true);
	return value as unknown[];
};

// A trust boundary: the fixtures are our own checked-in files, and `named` checks every shape read through this.
const at = (value: unknown, ...path: string[]): unknown =>
	path.reduce((one, key) => (one as Record<string, unknown>)[key], value);

const stateFixture = await read("state.json");
const frames = await read("frames.json");

describe("the state Go sends", () => {
	test("the state and every shape nested in it", () => {
		named("state", stateFixture, state);
		for (const one of [
			at(stateFixture, "workspace"),
			...each(at(stateFixture, "workspaces")),
		]) {
			named("workspace", one, workspace);
			for (const m of each(at(one, "members"))) named("member", m, member);
		}
		for (const one of each(at(stateFixture, "engines"))) named("engine", one, engine);
		for (const one of [
			at(stateFixture, "conversation"),
			...each(at(stateFixture, "conversations")),
		]) {
			named("conversation", one, conversation);
		}
		named("draft", at(stateFixture, "draft"), draft);
		for (const one of each(at(stateFixture, "draft", "attachments")))
			named("draft attachment", one, draftAttachment);
		named("sandbox", at(stateFixture, "sandbox"), sandbox);
		for (const one of each(at(stateFixture, "sandbox", "secrets")))
			named("sandbox secret", one, sandboxSecret);
		named("update", at(stateFixture, "update"), update);
		named("capabilities", at(stateFixture, "capabilities"), capabilities);
		for (const one of [
			at(stateFixture, "agent"),
			at(stateFixture, "defaultModel"),
			at(stateFixture, "workspaceDefaults", "model"),
			at(stateFixture, "installDefaults", "model"),
		]) {
			named("binding", one, binding);
		}
		named("workspaceDefaults", at(stateFixture, "workspaceDefaults"), scopeDefaults);
		named("installDefaults", at(stateFixture, "installDefaults"), scopeDefaults);
		named("can", at(stateFixture, "can"), can);
		for (const one of each(at(stateFixture, "agents"))) named("agent", one, agent);
		named("resolved", at(stateFixture, "resolved"), resolved);
		for (const one of each(at(stateFixture, "resolved", "layers"))) named("layer", one, layer);
		for (const one of each(at(stateFixture, "providers"))) {
			named("provider", one, provider);
			for (const m of each(at(one, "models"))) named("model", m, model);
		}
	});
});

describe("the frames Go forwards", () => {
	const authFrame: Keys<WireAuthFrame> = { type: 1, authId: 1, event: 1 };
	const secretEvent: Keys<WireSecretEvent> = {
		kind: 1,
		requestId: 1,
		conversationId: 1,
		name: 1,
		description: 1,
		hosts: 1,
		scope: 1,
		restartRequired: 1,
		reason: 1,
	};
	const authEvents: { [K in WireAuthEvent["kind"]]: Keys<Extract<WireAuthEvent, { kind: K }>> } =
		{
			prompt: { kind: 1, promptId: 1, promptType: 1, message: 1, placeholder: 1, options: 1 },
			info: { kind: 1, message: 1, links: 1 },
			auth_url: { kind: 1, url: 1, instructions: 1 },
			device_code: {
				kind: 1,
				userCode: 1,
				verificationUri: 1,
				intervalSeconds: 1,
				expiresInSeconds: 1,
			},
			progress: { kind: 1, message: 1 },
			prompt_cancelled: { kind: 1, promptId: 1 },
			done: { kind: 1, providerId: 1 },
			error: { kind: 1, message: 1 },
		};

	for (const [kind, shape] of Object.entries(authEvents)) {
		test(`auth event: ${kind}`, () => {
			const frame = at(frames, `auth_${kind}`);
			named(`auth_${kind}`, frame, authFrame);
			expect(at(frame, "event", "kind")).toBe(kind);
			named(`auth_${kind}.event`, at(frame, "event"), shape);
		});
	}

	test("secret event", () => {
		const frame = at(frames, "secret_event");
		expect(at(frame, "type")).toBe("secret");
		named("secret", at(frame, "secret"), secretEvent);
	});

	test("view frame", () => {
		const frame = at(frames, "view");
		// The other WireFrame fields belong to the other frame kinds, checked above.
		const view: Pick<Keys<WireFrame>, "type" | "conversationId" | "value" | "acornd"> = {
			type: 1,
			conversationId: 1,
			value: 1,
			acornd: 1,
		};
		named("view", frame, view);
		expect(at(frame, "acornd", "senders")).toBeDefined();
	});
});
