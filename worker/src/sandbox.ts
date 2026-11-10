import type { Files } from "./files.ts";

/**
 * A workspace's sandbox, as the worker sees it: the only way it touches the workspace.
 *
 * The worker runs on the host, next to the provider credential, and has no host path to the
 * workspace at all. Everything the model asks for — a command, a file — goes through here
 * and happens inside the sandbox, where the workspace is the only host folder mounted and
 * the machine holds nothing else of ours. See docs/adr/0012-sandbox.md.
 *
 * This file is the contract, and it names no engine. An engine is one module implementing
 * `SandboxClient` plus a way to open one — `microsandbox.ts`, `bwrap.ts`, `youki.ts`, and `host.ts` today, which are
 * also where the engine-specific code lives. Control picks the engine and passes its name to
 * the worker, so both sides of one install use the same one; a second engine is a second file
 * here and a second `Driver` in gateway/internal/sandbox.
 */
export interface SandboxClient extends Files {
	/** The engine's name for this sandbox, which is derived from the workspace. */
	readonly name: string;
	/**
	 * Why there is no sandbox, when there is none at all. The file tools read through this
	 * interface, and a missing sandbox must not look like a missing file; the resource loaders
	 * still get the empty answer they can work with.
	 */
	readonly unavailable?: string;
	/**
	 * Whether opening the sandbox is still waiting for control's next start. Resource loading
	 * at worker startup reads this and does not wait: a sandbox that is not there yet must not
	 * hold the worker's first line for a wake.
	 */
	readonly pending?: boolean;
	/**
	 * Run a command inside the sandbox. A string goes through a shell; an array runs its
	 * program directly with the rest as arguments, without a shell to parse them. Chunks
	 * arrive as they are produced.
	 */
	exec(command: string | readonly string[], options: ExecOptions): Promise<ExecResult>;
	/** File contents, or an error: unlike `read`, a missing file is a failure here. */
	readBytes(path: string): Promise<Uint8Array>;
	write(path: string, content: string | Uint8Array): Promise<void>;
	writeStream(path: string): Promise<WriteSink>;
	mkdir(path: string): Promise<void>;
	remove(path: string, options: { recursive?: boolean; force?: boolean }): Promise<void>;
	rename(from: string, to: string): Promise<void>;
}

/**
 * An engine's module: its name, and how to open a workspace with it. The worker matches the
 * name control sends against this interface, so the two sides of one install cannot disagree
 * about which engine is in use.
 */
export interface SandboxEngine {
	engineName(): string;
	/**
	 * `wake` starts a stopped sandbox through control, which holds the values a substitute
	 * secret's stored source names. An engine that cannot be woken this way ignores it.
	 */
	openSandbox(
		name: string,
		workspace: string,
		wake?: () => Promise<void>,
	): Promise<SandboxClient>;
}

export interface ExecChunk {
	stream: "stdout" | "stderr";
	data: Uint8Array;
}

export interface ExecOptions {
	cwd?: string;
	env?: Record<string, string>;
	/**
	 * The literal a command holds for each secret it may use, by name: on an engine whose substitution
	 * is a proxy, the guest never sees the value, so the environment has to carry the placeholder the
	 * proxy will swap. An engine that substitutes on its own side of the boundary ignores this — it
	 * has already put the literal in the guest.
	 */
	secrets?: Record<string, string>;
	/** Start from an empty environment instead of the guest's own. */
	clearEnv?: boolean;
	/** Milliseconds; the command is killed when it runs longer. */
	timeoutMs?: number;
	/** Aborting kills the command, inside the sandbox, and reports "aborted". */
	signal?: AbortSignal;
	onChunk?: (chunk: ExecChunk) => void;
}

export interface ExecResult {
	code: number;
	stdout: Uint8Array;
	stderr: Uint8Array;
	/** Set when we stopped the command rather than it finishing: exit codes cannot say why. */
	stopped?: "aborted" | "timeout";
}

export interface DirEntry {
	name: string;
	directory: boolean;
	size: number;
	mtimeMs: number;
}

export interface WriteSink {
	write(data: string | Uint8Array): Promise<void>;
	close(): Promise<void>;
}

/**
 * What the worker gets when there is no sandbox: reads find nothing and everything else
 * fails with the reason.
 *
 * This is the refusal. Running tools on the host because the sandbox was unavailable would
 * make the sandbox advisory, and "unconfined" would be a state the system drifted into
 * rather than a decision somebody made.
 */
export function refusedSandbox(reason: string): SandboxClient {
	const fail = (): never => {
		throw new Error(reason);
	};
	return {
		name: "",
		unavailable: reason,
		read: async () => null,
		list: async () => [],
		stat: async () => null,
		readBytes: async () => fail(),
		exec: async () => fail(),
		write: async () => fail(),
		writeStream: async () => fail(),
		mkdir: async () => fail(),
		remove: async () => fail(),
		rename: async () => fail(),
	};
}

/**
 * A sandbox that could not be prepared when the worker started, but may be preparable now.
 *
 * Control owns the start (ADR 0037), so the first thing a tool does is ask it to try again.
 * A workspace that lost a race with an idle eviction, or met a machine probe that was busy for
 * a moment, recovers on the next call instead of staying dead until the gateway restarts. Every
 * method is the opened client's method once the wake lands; a wake that does not land is the
 * tool's error, in the words control gave.
 */
export function deferredSandbox(
	engine: SandboxEngine,
	name: string,
	workspace: string,
	wake: () => Promise<void>,
	reason: string,
): SandboxClient {
	let opening: Promise<SandboxClient> | undefined;
	const client = (): Promise<SandboxClient> => {
		if (opening === undefined) {
			opening = wake().then(
				async () => await engine.openSandbox(name, workspace, wake),
				(error: unknown) => {
					opening = undefined;
					throw new Error(
						`the sandbox could not be prepared: ${error instanceof Error ? error.message : String(error)} (it was: ${reason})`,
					);
				},
			);
			opening.catch(() => {
				opening = undefined;
			});
		}
		return opening;
	};
	return {
		name,
		pending: true,
		read: async (path) => (await client()).read(path),
		list: async (path) => (await client()).list(path),
		stat: async (path) => (await client()).stat(path),
		readBytes: async (path) => (await client()).readBytes(path),
		exec: async (command, options) => (await client()).exec(command, options),
		write: async (path, content) => (await client()).write(path, content),
		writeStream: async (path) => (await client()).writeStream(path),
		mkdir: async (path) => (await client()).mkdir(path),
		remove: async (path, options) => (await client()).remove(path, options),
		rename: async (from, to) => (await client()).rename(from, to),
	};
}

export function concatChunks(chunks: readonly Uint8Array[]): Uint8Array {
	const total = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0);
	const out = new Uint8Array(total);
	let offset = 0;
	for (const chunk of chunks) {
		out.set(chunk, offset);
		offset += chunk.byteLength;
	}
	return out;
}
