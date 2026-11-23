/**
 * The Go gateway's wire, read by the worker's types.
 *
 * `fixtures/wire` is written by a Go test from the structs the gateway really sends (`go test
 * ./internal/control`, with UPDATE_FIXTURES=1 to rewrite). Here every fixture is held against a table that
 * the compiler holds against `src/protocol.ts`: add, rename or drop a field in a type and the table stops
 * compiling; rename or drop it in Go and a fixture stops matching the table. Either way the two sides cannot
 * drift without a failing test (S6).
 *
 * A table says, per field, whether the type requires it. A fixture is full: it carries every field the type
 * names, so a field Go stopped sending is as visible as one it renamed.
 */
import { describe, expect, test } from "bun:test";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type {
	AuthEvent,
	Author,
	Command,
	Frame,
	ModelBinding,
	SecretInfo,
} from "../src/protocol.ts";

type Presence = "required" | "optional";
type Keys<T> = {
	[P in keyof T]-?: Partial<Pick<T, P>> extends Pick<T, P> ? "optional" : "required";
};

const fixtures = join(dirname(fileURLToPath(import.meta.url)), "../../fixtures/wire");

async function read(name: string): Promise<Record<string, Record<string, unknown>>> {
	// A trust boundary: our own checked-in file, and every value in it is checked below.
	return (await Bun.file(join(fixtures, name)).json()) as Record<string, Record<string, unknown>>;
}

/**
 * Fields Go writes that the worker has no use for, and so does not name. The gateway's Command has no
 * `omitempty` on them (a `false` has to survive for `hide`, and the secret list for `secret_list_answer`),
 * which puts them on every command. The worker reads fields by the handler that owns them, so they are
 * ignored elsewhere.
 */
const everyCommandCarries = ["hidden", "secrets"];

/** Go omits a false `ok` (`omitempty`) because only the worker writes that frame, and Go only reads it. */
const goOmitsFalse = ["ok"];

function compare(
	label: string,
	value: unknown,
	shape: Record<string, Presence>,
	{ extra = [], absent = [] }: { extra?: string[]; absent?: string[] } = {},
): void {
	expect(
		typeof value === "object" && value !== null && !Array.isArray(value),
		`${label} is an object`,
	).toBe(true);
	const present = Object.keys(value as object);
	const unknown = present.filter((key) => !(key in shape) && !extra.includes(key));
	expect(unknown, `${label}: fields Go sends that the worker's type does not name`).toEqual([]);
	const missing = Object.keys(shape).filter(
		(key) => !present.includes(key) && !absent.includes(key),
	);
	expect(missing, `${label}: fields the worker's type names that Go does not send`).toEqual([]);
}

const author: Keys<Author> = { email: "required", name: "optional", speaker: "optional" };
const binding: Keys<ModelBinding> = {
	provider: "required",
	modelId: "required",
	thinkingLevel: "optional",
	instructions: "optional",
	definition: "optional",
};
const secret: Keys<SecretInfo> = {
	name: "required",
	description: "optional",
	scope: "optional",
	hosts: "optional",
	delivery: "optional",
	status: "optional",
	placeholder: "optional",
};

type CommandOf<K extends Command["type"]> = Extract<Command, { type: K }>;
const commandShapes: { [K in Command["type"]]: Keys<CommandOf<K>> } = {
	describe: { id: "required", type: "required" },
	conversations: { id: "required", type: "required" },
	create: { id: "required", type: "required" },
	rename: { id: "required", type: "required", conversationId: "required", title: "required" },
	hide: { id: "required", type: "required", conversationId: "required", hidden: "required" },
	fork: { id: "required", type: "required", conversationId: "required", at: "required" },
	compact: { id: "required", type: "required", conversationId: "required" },
	compact_preview: { id: "required", type: "required", conversationId: "required" },
	reset: { id: "required", type: "required", conversationId: "required" },
	action: {
		id: "required",
		type: "required",
		conversationId: "required",
		text: "required",
		action: "required",
		author: "optional",
	},
	open: { id: "required", type: "required", conversationId: "required", agent: "required" },
	attach: { id: "required", type: "required", conversationId: "required" },
	detach: { id: "required", type: "required", conversationId: "required" },
	prompt: {
		id: "required",
		type: "required",
		conversationId: "required",
		text: "required",
		whenBusy: "optional",
		attachments: "optional",
		author: "optional",
	},
	attachment: {
		id: "required",
		type: "required",
		name: "required",
		mime: "required",
		size: "required",
		tempPath: "required",
	},
	abort: { id: "required", type: "required", conversationId: "required", author: "optional" },
	agents: { id: "required", type: "required" },
	commands: { id: "required", type: "required" },
	expand: { id: "required", type: "required", text: "required" },
	policy: { id: "required", type: "required", conversationId: "required" },
	messages: { id: "required", type: "required", conversationId: "required" },
	reload: { id: "required", type: "required" },
	login: {
		id: "required",
		type: "required",
		authId: "required",
		providerId: "required",
		authType: "required",
	},
	logout: { id: "required", type: "required", providerId: "required" },
	auth_answer: {
		id: "required",
		type: "required",
		authId: "required",
		promptId: "required",
		value: "optional",
		cancelled: "optional",
	},
	auth_cancel: { id: "required", type: "required", authId: "required" },
	secret_answer: {
		id: "required",
		type: "required",
		secretRequestId: "required",
		cancelled: "optional",
		secretScope: "optional",
		restartRequired: "optional",
		reason: "optional",
	},
	secret_list_answer: {
		id: "required",
		type: "required",
		secretRequestId: "required",
		secrets: "required",
		reason: "optional",
	},
	sandbox_wake: { id: "required", type: "required", replyTo: "required", error: "optional" },
	usage: { id: "required", type: "required" },
	shutdown: { id: "required", type: "required" },
};

type AuthOf<K extends AuthEvent["kind"]> = Extract<AuthEvent, { kind: K }>;
const authShapes: { [K in AuthEvent["kind"]]: Keys<AuthOf<K>> } = {
	prompt: {
		kind: "required",
		promptId: "required",
		promptType: "required",
		message: "required",
		placeholder: "optional",
		options: "optional",
	},
	info: { kind: "required", message: "required", links: "optional" },
	auth_url: { kind: "required", url: "required", instructions: "optional" },
	device_code: {
		kind: "required",
		userCode: "required",
		verificationUri: "required",
		intervalSeconds: "optional",
		expiresInSeconds: "optional",
	},
	progress: { kind: "required", message: "required" },
	prompt_cancelled: { kind: "required", promptId: "required" },
	done: { kind: "required", providerId: "required" },
	error: { kind: "required", message: "required" },
};
const authOption: Keys<NonNullable<AuthOf<"prompt">["options"]>[number]> = {
	id: "required",
	label: "required",
	description: "optional",
};
const authLink: Keys<NonNullable<AuthOf<"info">["links"]>[number]> = {
	url: "required",
	label: "optional",
};

type FrameOf<T extends Frame["type"]> = Extract<Frame, { type: T }>;
type ViewFrame = FrameOf<"view">;
const frameShapes = {
	response_ok: {
		type: "required",
		id: "required",
		ok: "required",
		data: "optional",
	} satisfies Keys<Extract<Frame, { type: "response"; ok: true }>>,
	response_error: {
		type: "required",
		id: "required",
		ok: "required",
		error: "required",
	} satisfies Keys<Extract<Frame, { type: "response"; ok: false }>>,
	view: {
		type: "required",
		conversationId: "required",
		seq: "required",
		value: "required",
		acornd: "optional",
	} satisfies Keys<ViewFrame>,
	submission: {
		type: "required",
		conversationId: "required",
		submissionId: "required",
		status: "required",
	} satisfies Keys<FrameOf<"submission">>,
	secret_request: {
		type: "required",
		id: "required",
		conversationId: "required",
		name: "required",
		description: "optional",
		hosts: "optional",
	} satisfies Keys<FrameOf<"secret_request">>,
	secret_list: { type: "required", id: "required", conversationId: "required" } satisfies Keys<
		FrameOf<"secret_list">
	>,
	sandbox_wake: { type: "required", id: "required" } satisfies Keys<FrameOf<"sandbox_wake">>,
	auth: { type: "required", authId: "required", event: "required" } satisfies Keys<
		FrameOf<"auth">
	>,
} as const satisfies Record<string, Record<string, Presence>>;
const sender: Keys<NonNullable<ViewFrame["acornd"]>["senders"][string]> = {
	conversationId: "required",
	name: "optional",
	kind: "required",
};

const commands = await read("commands.json");
const frames = await read("frames.json");

describe("commands Go sends", () => {
	test("there is one fixture for every command type, and no other", () => {
		expect(Object.keys(commands).sort()).toEqual(Object.keys(commandShapes).sort());
	});

	for (const [kind, shape] of Object.entries(commandShapes)) {
		test(kind, () => {
			const command = commands[kind];
			compare(kind, command, shape, {
				extra: everyCommandCarries.filter((key) => !(key in shape)),
			});
			expect(command?.type).toBe(kind);
			if (command?.author !== undefined) compare(`${kind}.author`, command.author, author);
			if (command?.agent !== undefined) compare(`${kind}.agent`, command.agent, binding);
			for (const item of (command?.secrets as unknown[] | null) ?? [])
				compare(`${kind}.secrets`, item, secret);
		});
	}
});

describe("frames Go reads and forwards", () => {
	for (const [name, shape] of Object.entries(frameShapes)) {
		test(name, () => {
			const frame = frames[name === "auth" ? "auth_prompt" : name];
			compare(name, frame, shape, { absent: name === "response_error" ? goOmitsFalse : [] });
			if (name === "view") {
				const senders = (frame?.acornd as { senders: Record<string, unknown> }).senders;
				for (const one of Object.values(senders))
					compare("view.acornd.senders", one, sender);
			}
		});
	}

	for (const [kind, shape] of Object.entries(authShapes)) {
		test(`auth event: ${kind}`, () => {
			const frame = frames[`auth_${kind}`];
			expect(frame?.type).toBe("auth");
			const event = frame?.event as Record<string, unknown>;
			compare(`auth_${kind}.event`, event, shape);
			expect(event.kind).toBe(kind);
			for (const option of (event.options as unknown[] | undefined) ?? [])
				compare("option", option, authOption);
			for (const link of (event.links as unknown[] | undefined) ?? [])
				compare("link", link, authLink);
		});
	}

	test("every fixture is checked by a test above", () => {
		const covered = new Set([
			...Object.keys(frameShapes).filter((name) => name !== "auth"),
			...Object.keys(authShapes).map((kind) => `auth_${kind}`),
			// The interface's own frame: the worker never sends or reads it (ui/test/wire-fixtures.test.ts).
			"secret_event",
		]);
		expect(Object.keys(frames).filter((name) => !covered.has(name))).toEqual([]);
	});
});
