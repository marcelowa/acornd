/**
 * One worker: one Harness over one storage, and the table that answers its commands.
 *
 * `createWorker` builds what the process holds — credentials, sandbox, resources, harness, the
 * conversations it has open, the views it sends — and `main.ts` only parses arguments and wires
 * it to stdin and stdout. What a test needs to replace comes in as `WorkerDeps`.
 */
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import type { Models } from "@earendil-works/pi-ai";
import { getSupportedThinkingLevels } from "@earendil-works/pi-ai/models";
import { builtinModels } from "@earendil-works/pi-ai/providers/all";
import {
	createRegistry,
	defineExtension,
	Harness,
	section,
	type HarnessSettings,
	type Storage,
} from "@earendil-works/pi-durable";
import { CodingTools } from "@earendil-works/pi-durable/tools";
import { attachmentCommands } from "./attachments.ts";
import { authCommands, cancelAuth, resolveAuthAnswer } from "./auth.ts";
import { authorsExtension } from "./authors.ts";
import { commandCommands } from "./commands.ts";
import { compactionCommands } from "./compaction.ts";
import { layerRoots } from "./config.ts";
import { conversationCommands, conversationRegistry, type Conversations } from "./conversations.ts";
import { FileCredentialStore } from "./credentials.ts";
import { delegationCommands, delegationExtension } from "./delegation.ts";
import { devProvider, scriptedDevProvider } from "./devprovider.ts";
import { routingFiles, tolerantFiles, type Files } from "./files.ts";
import { guardsExtension } from "./hooks.ts";
import { ConversationMetaDoc } from "./meta.ts";
import { PREAMBLE } from "./preamble.ts";
import {
	CommandQueue,
	type Command,
	type DescribeData,
	type Frame,
	log,
	parseCommand,
} from "./protocol.ts";
import { loadResources, resourceCommands, type Resources } from "./resources.ts";
import { runCommands } from "./runs.ts";
import { SandboxExecutionEnv } from "./sandbox-env.ts";
import type { SandboxClient } from "./sandbox.ts";
import { resolveSecretAnswer, resolveSecretList, secretExtension } from "./secret-tool.ts";
import { secretSection, type SecretInfo } from "./secrets.ts";
import { skillsExtension } from "./skills.ts";
import { createViews, viewCommands, type Views } from "./views.ts";

const CONTEXT = BACKGROUND_CONTEXT;
/** How long a tool call waits for control to start a stopped sandbox. */
const SANDBOX_WAKE_TIMEOUT_MS = 2 * 60_000;

export type Registry = ReturnType<typeof createRegistry>;

export interface WorkerOptions {
	workspace: string;
	/** The installation folder: global resources and shared state. */
	home: string;
	credentials: string;
	/** The workspace's delivered secrets, names and descriptions only. */
	secrets: SecretInfo[];
	/** A scripted model to talk to instead of a provider (ACORND_DEV_SCRIPT). */
	devScript?: string | undefined;
	/** The fixed dev model (ACORND_DEV_PROVIDER=1), when there is no script. */
	devProvider?: boolean | undefined;
}

/** The edges of the process: what a test replaces with a fake. */
export interface WorkerDeps {
	openStorage(): Promise<Storage>;
	/** `wake` asks control to start a stopped sandbox; a client that can be woken keeps it. */
	openSandbox(wake: () => Promise<void>): Promise<SandboxClient>;
	/**
	 * One frame out. Not the only way out yet: `auth.ts` and `secret-tool.ts` still write their
	 * frames to stdout themselves.
	 */
	write(frame: Frame): void;
	now(): number;
	/** Called once, after `shutdown` or the end of input has closed the harness. */
	exit(): void;
}

/** What the command handlers share. Built once by `createWorker`. */
export interface WorkerContext {
	readonly workspace: string;
	readonly home: string;
	readonly harness: Harness;
	readonly storage: Storage;
	readonly models: Models;
	readonly registry: Registry;
	readonly sandbox: SandboxClient;
	readonly resources: Resources;
	/** Skills are filtered per conversation, because two conversations can run different agents at once. */
	readonly skillFilter: Map<string, readonly string[] | null>;
	readonly conversations: Conversations;
	readonly views: Views;
	write(frame: Frame): void;
	now(): number;
}

/**
 * Commands that bypass the queue. Each one answers something that is blocked waiting for it — a
 * login, a tool asking for a secret, a tool waiting for its sandbox — so queueing it behind what it
 * answers would deadlock.
 */
type Bypassing = Extract<
	Command,
	{
		type:
			| "auth_answer"
			| "auth_cancel"
			| "secret_answer"
			| "secret_list_answer"
			| "sandbox_wake";
	}
>;

/** The commands the table answers. */
export type HandledCommand = Exclude<Command, Bypassing>;

/**
 * One handler per command. The type names every handled command, so a new command in the protocol
 * is either given a handler or added to `Bypassing`, and the compiler asks.
 */
export type CommandTable = {
	[T in HandledCommand["type"]]: (
		command: Extract<HandledCommand, { type: T }>,
	) => Promise<unknown>;
};

type BypassTable = {
	[T in Bypassing["type"]]: (command: Extract<Bypassing, { type: T }>) => void;
};

export interface Worker {
	/** Resume the harness and attach the root conversation. Commands are answered after this. */
	start(): Promise<void>;
	/** One line of input. */
	receive(line: string): void;
	/** The input ended: shut down after what is queued. */
	end(): void;
}

interface SandboxWake {
	resolve: () => void;
	reject: (error: Error) => void;
	timer: ReturnType<typeof setTimeout>;
}

/**
 * Build a worker. The steps run in this order, and each depends on one before it:
 *
 * 1. credentials and models;
 * 2. the sandbox wakes, so a sandbox that is not ready yet can ask for one;
 * 3. the sandbox, the only way to the workspace's files;
 * 4. resources — skills, agents, rules, commands — read through the sandbox;
 * 5. the dev provider, if one is asked for, replacing the providers in the models;
 * 6. the registry, whose prompt reads the rules and the skills;
 * 7. the storage, then the harness over it;
 * 8. the extensions that need the harness: delegation, guards, authors.
 */
export async function createWorker(options: WorkerOptions, deps: WorkerDeps): Promise<Worker> {
	const credentials = new FileCredentialStore(options.credentials);
	const models = builtinModels({ credentials });

	/**
	 * Wakes waiting for control's answer, by the frame id that asked. A stopped sandbox is
	 * started by control, not here: a substitute secret's stored config names a host variable
	 * whose value lives in the gateway's vault, and this process never has it (issue #13).
	 *
	 * It is declared before the sandbox is opened: a worker whose sandbox could not be prepared
	 * gets a deferred client, and the first thing a tool does with it is ask for a wake.
	 */
	const sandboxWakes = new Map<string, SandboxWake>();

	/** Ask control to start the stopped sandbox and wait for it to say the start landed. */
	function wakeSandbox(): Promise<void> {
		const id = crypto.randomUUID();
		return new Promise((resolve, reject) => {
			const timer = setTimeout(() => {
				sandboxWakes.delete(id);
				reject(new Error("the gateway did not answer the sandbox wake"));
			}, SANDBOX_WAKE_TIMEOUT_MS);
			timer.unref?.();
			sandboxWakes.set(id, { resolve, reject, timer });
			deps.write({ type: "sandbox_wake", id });
		});
	}

	/** Settle the wake control answered. A reason is a start that did not land, so the call fails closed. */
	function answerSandboxWake(replyTo: string, reason: string | undefined): void {
		const pending = sandboxWakes.get(replyTo);
		if (pending === undefined) return;
		sandboxWakes.delete(replyTo);
		clearTimeout(pending.timer);
		if (reason !== undefined && reason !== "") pending.reject(new Error(reason));
		else pending.resolve();
	}

	const sandbox = await deps.openSandbox(wakeSandbox);

	// Every resource folder this install reads: the user's, then the workspace's, each with the
	// ecosystem's `.agents` before our own `.acornd`.
	//
	// The workspace's own files live inside the sandbox and the user's live on the host, so reads
	// are routed per path: there is one door to the workspace, and this is it.
	const roots = layerRoots({ home: options.home, workspace: options.workspace });
	// A sandbox that is only waiting for control's next start answers resource reads with nothing
	// rather than waking: the first line of a worker must not wait two minutes for a sandbox, and a
	// workspace with fewer skills at this start is a smaller workspace, not a dead one.
	const emptyFiles: Files = {
		read: async () => null,
		list: async () => [],
		stat: async () => null,
	};
	const resourceFiles = sandbox.pending === true ? emptyFiles : tolerantFiles(sandbox);
	const files = routingFiles(options.workspace, resourceFiles);
	const resources = await loadResources(roots, files, options.workspace);

	const skillFilter = new Map<string, readonly string[] | null>();

	if (options.devScript !== undefined && options.devScript !== "") {
		models.setProvider(scriptedDevProvider(options.devScript).provider);
	} else if (options.devProvider === true) {
		models.setProvider(devProvider().provider);
	}

	const registry = createRegistry();
	// Order matters: the prompt reads preamble, where it is, the project's rules, then what
	// skills are available, and finally the agent's own instructions, which durable renders.
	registry.install(
		defineExtension({
			name: "acornd-prompt",
			sections: [
				section("preamble", () => PREAMBLE, { tag: false }),
				section("cwd", (input) => input.env?.cwd),
				section("workspace-rules", () => resources.rules),
				section("secrets", () => secretSection(options.secrets)),
			],
		}),
	);
	registry.install(
		skillsExtension(
			resources.skills,
			(conversationId) => skillFilter.get(conversationId) ?? null,
			files,
		),
	);
	registry.install(CodingTools);
	registry.install(secretExtension());

	const settings: HarnessSettings = { toolExecution: "parallel" };
	// One environment per tool call, all of them over the same sandbox: the id is the sandbox's,
	// which is how the harness knows two calls see the same files.
	const env = ({ cwd }: { readonly cwd?: string }) =>
		new SandboxExecutionEnv(sandbox, cwd ?? options.workspace, options.secrets);

	const storage = await deps.openStorage();
	const harness = await Harness.open(
		storage,
		{
			models,
			registry,
			settings,
			env,
			// A task phase that throws is otherwise swallowed: the run simply never
			// continues and nothing is logged. This is the only window into that.
			onReport: (error) =>
				log("extension failure:", error instanceof Error ? error.message : String(error)),
			// Runs in every commit that creates or forks a conversation, so metadata
			// exists from the first entry and a fork inherits its parent's naming rules.
			conversationCreated: async (tx, conversation) => {
				await tx.doc(ConversationMetaDoc, conversation.id);
			},
		},
		CONTEXT,
	);

	// Registered after the harness opens: the delegation tools need it to read a
	// child's answer, and the registry accepts installs while running.
	registry.install(
		delegationExtension({
			harness,
			storage,
			catalog: () => resources.catalog,
			// What a definition allows, so a child obeys its own list rather than being offered every
			// skill the install has.
			setSkills: (conversationId, skills) => skillFilter.set(conversationId, skills),
		}),
	);
	registry.install(
		guardsExtension({
			harness,
			storage,
			catalog: () => resources.catalog,
			workspaceRoot: options.workspace,
		}),
	);
	registry.install(authorsExtension({ harness, storage, catalog: () => resources.catalog }));

	const conversations = conversationRegistry(harness);
	const context: WorkerContext = {
		workspace: options.workspace,
		home: options.home,
		harness,
		storage,
		models,
		registry,
		sandbox,
		resources,
		skillFilter,
		conversations,
		views: createViews({ harness, conversations, write: deps.write }),
		write: deps.write,
		now: deps.now,
	};

	const table: CommandTable = {
		describe: async () => await describe(context),
		usage: async () => await harness.usage(CONTEXT),
		shutdown: async () => undefined,
		...conversationCommands(context),
		...compactionCommands(context),
		...viewCommands(context),
		...attachmentCommands(context),
		...runCommands(context),
		...delegationCommands(context),
		...commandCommands(context),
		...resourceCommands(context),
		...authCommands(context),
	};

	const bypass: BypassTable = {
		auth_answer: (command) =>
			resolveAuthAnswer(command.promptId, command.value, command.cancelled === true),
		auth_cancel: (command) => cancelAuth(command.authId),
		secret_answer: (command) =>
			resolveSecretAnswer(command.secretRequestId, {
				cancelled: command.cancelled === true,
				scope: command.secretScope,
				restartRequired: command.restartRequired === true,
				reason: command.reason,
			}),
		secret_list_answer: (command) =>
			resolveSecretList(command.secretRequestId, {
				secrets: command.secrets ?? [],
				reason: command.reason,
			}),
		sandbox_wake: (command) => answerSandboxWake(command.replyTo, command.error),
	};

	async function handle(command: HandledCommand): Promise<unknown> {
		// Own keys only: a type such as "constructor" must not find Object's.
		if (!Object.hasOwn(table, command.type)) {
			throw new Error(`unsupported command: ${JSON.stringify(command)}`);
		}
		const handler = table[command.type] as (command: HandledCommand) => Promise<unknown>;
		return await handler(command);
	}

	/** One try around the run and the ok write, so a failed write of the answer is still answered. */
	async function respond(id: string, run: Promise<unknown>): Promise<void> {
		try {
			const data = await run;
			deps.write({ type: "response", id, ok: true, ...(data === undefined ? {} : { data }) });
		} catch (error) {
			deps.write({ type: "response", id, ok: false, error: (error as Error).message });
		}
	}

	const queue = new CommandQueue();
	let closing = false;

	const shutdown = async (): Promise<void> => {
		if (closing) return;
		closing = true;
		await harness.close(CONTEXT).catch(() => undefined);
		deps.exit();
	};

	return {
		async start() {
			harness.resume();
			// The root conversation exists in every install and is adopted as the first
			// conversation, so an upgrade keeps its history.
			const root = await harness.root(CONTEXT);
			conversations.add(root);
			await context.views.attach(String(root.id));
			log(`worker ready workspace=${options.workspace} conversation=${root.id}`);
		},

		receive(line) {
			const trimmed = line.trim();
			if (trimmed.length === 0) return;
			const parsed = parseCommand(trimmed);
			if (!parsed.ok) {
				// Dropped with the reason: a line the worker cannot read is a bug in whoever wrote it,
				// and the one thing it must not do is take the live conversations down with it (T8).
				log("dropped a line that is not a command", parsed.reason);
				return;
			}
			const command = parsed.command;
			if (Object.hasOwn(bypass, command.type)) {
				const answer = bypass[command.type as Bypassing["type"]] as (
					command: Bypassing,
				) => void;
				answer(command as Bypassing);
				deps.write({ type: "response", id: command.id, ok: true });
				return;
			}
			const handled = command as HandledCommand;
			// A login runs beside the queue as well. It is a conversation with the provider that can
			// wait minutes for a code, and one sign-in nobody answers must not hold up every command
			// behind it — the state, the conversations, the next message. Its response still comes
			// when the flow ends, which is what the caller is waiting for.
			if (handled.type === "login") {
				void respond(handled.id, handle(handled));
				return;
			}
			queue.push(async () => {
				await respond(handled.id, handle(handled));
				if (handled.type === "shutdown") await shutdown();
			});
		},

		end() {
			queue.push(shutdown);
		},
	};
}

async function describe(context: WorkerContext): Promise<DescribeData> {
	const { models } = context;
	const providers: DescribeData["providers"] = [];
	for (const provider of models.getProviders()) {
		// One provider's check failing must not fail the description: the description is how the
		// worker announces itself, so a workspace would be unusable over a provider's bad day.
		// It reports that provider as not authenticated, which is what it also does for no key.
		const check = await models.checkAuth(provider.id).catch(() => undefined);
		const authenticated = check !== undefined;
		const list = authenticated ? await models.getAvailable(provider.id).catch(() => []) : [];
		// What the connect dialog can offer: a key field only when the provider asks for
		// one, sign-in only when it implements it, nothing when the key is ambient.
		const authTypes: DescribeData["providers"][number]["authTypes"] = [];
		if (provider.auth.apiKey?.login !== undefined) authTypes.push("api_key");
		if (provider.auth.oauth !== undefined) authTypes.push("oauth");
		providers.push({
			id: provider.id,
			name: provider.name ?? provider.id,
			authenticated,
			...(check?.source === undefined ? {} : { source: check.source }),
			...(check?.type === undefined ? {} : { authType: check.type }),
			authTypes,
			models: list.map((model) => ({
				id: model.id,
				name: model.name ?? model.id,
				contextWindow: model.contextWindow,
				thinkingLevels: getSupportedThinkingLevels(model) as string[],
			})),
		});
	}
	providers.sort(
		(a, b) => Number(b.authenticated) - Number(a.authenticated) || a.id.localeCompare(b.id),
	);
	return {
		provider: "pi-durable",
		workdir: context.workspace,
		providers,
		// subagents: the delegation tools are installed below, so the interface may offer them.
		capabilities: { steer: true, subagents: true, sandbox: "local-process" },
	};
}
