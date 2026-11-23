/**
 * Acornd harness worker.
 *
 * One worker owns one Harness over one storage: a workspace's conversations and
 * all the subagent conversations they spawn. It speaks the JSONL protocol in
 * src/protocol.ts and never talks to the UI or to our database.
 *
 * This file reads the arguments, opens what they name, and wires the worker in
 * src/worker.ts to stdin and stdout.
 */
import { mkdir } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { createInterface } from "node:readline";
import { openNodeSqliteStorage } from "@earendil-works/pi-durable/storage/sqlite/node";
import { emit } from "./protocol.ts";
import {
	deferredSandbox,
	refusedSandbox,
	type SandboxClient,
	type SandboxEngine,
} from "./sandbox.ts";
import { parseSecrets, type SecretInfo } from "./secrets.ts";
import { createWorker } from "./worker.ts";

interface Args {
	workspace: string;
	/** The installation folder: global resources and shared state. */
	home: string;
	storage: string;
	credentials: string;
	/** The sandbox this workspace runs in, as control named it. Empty means none: tools refuse. */
	sandbox: string;
	/** Which engine that sandbox belongs to. The name is control's, and the two must agree. */
	sandboxEngine: string;
	/**
	 * Why control could not prepare the sandbox, when it could not. A worker started with this
	 * set asks control to try again on the first tool call instead of staying dead: the failure
	 * may have been a moment's, and the worker outlives the moment.
	 */
	sandboxProblem: string;
	/** The workspace's delivered secrets, names and descriptions only. */
	secrets: SecretInfo[];
}

function parseArgs(argv: readonly string[]): Args {
	const values = new Map<string, string>();
	for (let index = 0; index < argv.length; index += 1) {
		const arg = argv[index];
		if (arg === undefined || !arg.startsWith("--")) continue;
		const key = arg.slice(2);
		const next = argv[index + 1];
		if (next === undefined || next.startsWith("--")) {
			values.set(key, "true");
			continue;
		}
		values.set(key, next);
		index += 1;
	}
	const workspace = resolve(values.get("workspace") ?? process.cwd());
	const defaults = (name: string) => join(workspace, ".acornd", name);
	const credentials = resolve(values.get("credentials") ?? defaults("credentials.json"));
	const home = resolve(values.get("home") ?? dirname(credentials));
	return {
		workspace,
		home,
		storage: resolve(values.get("storage") ?? defaults("session.sqlite")),
		credentials,
		sandbox: values.get("sandbox") ?? "",
		sandboxEngine: values.get("sandbox-engine") ?? "microsandbox",
		sandboxProblem: values.get("sandbox-problem") ?? "",
		secrets: parseSecrets(values.get("secrets")),
	};
}

/**
 * The sandbox this workspace runs in — the only way this process touches the workspace.
 *
 * Control created it and named it; this attaches to it with the engine's SDK. There is no
 * fallback to the host: without a sandbox the tools refuse, and a workspace whose resources
 * cannot be read simply has fewer of them. Running unconfined must be a decision somebody
 * made, never a state the system drifted into.
 */
async function openSandboxForWorkspace(
	options: Args,
	wakeSandbox: () => Promise<void>,
): Promise<SandboxClient> {
	if (options.sandbox === "") {
		return refusedSandbox(
			options.sandboxProblem === ""
				? "this workspace has no sandbox: tool execution is disabled"
				: options.sandboxProblem,
		);
	}
	const engine = await sandboxEngine(options.sandboxEngine);
	if (engine === undefined) {
		return refusedSandbox(
			`this workspace's sandbox belongs to engine ${options.sandboxEngine}, which this build does not support`,
		);
	}
	if (options.sandboxProblem !== "") {
		// Control could not prepare the sandbox before starting this worker. The engine is still
		// named, so the first tool call can ask control to try again through the wake path
		// (ADR 0037) instead of the workspace staying dead until a gateway restart.
		return deferredSandbox(
			engine,
			options.sandbox,
			options.workspace,
			wakeSandbox,
			options.sandboxProblem,
		);
	}
	return await engine
		.openSandbox(options.sandbox, options.workspace, wakeSandbox)
		.catch((error: unknown) => {
			return refusedSandbox(
				`cannot attach to the sandbox ${options.sandbox}: ${error instanceof Error ? error.message : String(error)}`,
			);
		});
}

/** The engine modules this build implements, by the name control sends. */
async function sandboxEngine(name: string): Promise<SandboxEngine | undefined> {
	switch (name) {
		case "microsandbox":
			return await import("./microsandbox.ts");
		case "bwrap":
			return await import("./bwrap.ts");
		case "youki":
			return await import("./youki.ts");
		case "host":
			return await import("./host.ts");
		default:
			return undefined;
	}
}

const args = parseArgs(process.argv.slice(2));
await mkdir(dirname(args.storage), { recursive: true });

const worker = await createWorker(
	{
		workspace: args.workspace,
		home: args.home,
		credentials: args.credentials,
		secrets: args.secrets,
		devScript: process.env.ACORND_DEV_SCRIPT,
		devProvider: process.env.ACORND_DEV_PROVIDER === "1",
	},
	{
		openStorage: () => openNodeSqliteStorage(args.storage),
		openSandbox: (wake) => openSandboxForWorkspace(args, wake),
		write: emit,
		now: Date.now,
		exit: () => process.exit(0),
	},
);
await worker.start();

const lines = createInterface({ input: process.stdin, terminal: false });
lines.on("line", (line) => worker.receive(line));
lines.on("close", () => worker.end());
