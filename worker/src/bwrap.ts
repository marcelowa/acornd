import { connect, type Socket } from "node:net";
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { concatChunks, type ExecOptions, type ExecResult, type SandboxClient } from "./sandbox.ts";
import { WorkspaceFiles } from "./workspace-files.ts";

/**
 * The bubblewrap engine: a sandbox that runs as one process per workspace, with this worker's
 * binary as its first process inside.
 *
 * Control writes one config per workspace to `ACORNFALL_BWRAP_STATE` — the same file a person can
 * read — starts the sandbox, and the sandbox's supervisor answers on a Unix socket. A command
 * here is a request over that socket: the supervisor runs it inside the namespaces it already
 * holds, streams it back, and reaps what it leaves behind. That is why a daemon outlives the
 * command that started it, and why a command costs a fork instead of a sandbox.
 *
 * File operations are the host's, unchanged: bubblewrap binds a folder at the path it already
 * has, so a path means the same thing inside and outside.
 *
 * See docs/adr/0023-bwrap-engine.md.
 */
export function engineName(): string {
	return "bwrap";
}

export async function openSandbox(name: string, workspace: string): Promise<SandboxClient> {
	const state = process.env.ACORNFALL_BWRAP_STATE;
	if (state === undefined || state === "") {
		throw new Error(
			"the bwrap engine's state folder is not set: control starts a workspace with ACORNFALL_BWRAP_STATE",
		);
	}
	const path = join(state, `${name}.json`);
	const config = JSON.parse(await readFile(path, "utf8")) as BwrapConfig;
	if (config.engine !== "bwrap" || config.workspace !== workspace) {
		// A name that resolves to another workspace's config would run commands in the wrong
		// folder, which is the one mistake worth refusing loudly.
		throw new Error(`${path} is not the config for ${workspace}`);
	}
	return new BwrapClient(config);
}

/** What control resolved for one workspace: paths, the sandbox's socket, and its private home. */
export interface BwrapConfig {
	engine: string;
	workspace: string;
	image: string;
	/** How a command reaches the network, recorded for the person reading the file. */
	network: { mode: "off" | "pasta"; address?: string; gateway?: string };
	/** The workspace's own unpacked image, bound as `/`. */
	rootfs?: string;
	/** The private home, where a toolchain installed by one command is found by the next. */
	home: string;
	/** The install folder, hidden when the machine's root is the root. */
	hidden: string;
	readOnly?: string[];
	/** The sandbox's supervisor: where it listens, for this worker. */
	socket: string;
}

const systemPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";

/**
 * What a command's PATH starts with. Tools a workspace installs itself land in its own home, so
 * the two folders every unprivileged installer uses come first — otherwise a toolchain that
 * survived one command would still not be found by the next one.
 */
function searchPath(home: string): string {
	return `${home}/.local/bin:${home}/.cargo/bin:${home}/.bun/bin:${systemPath}`;
}

/** Where the private home appears inside the sandbox. */
export function guestHome(config: BwrapConfig): string {
	return config.rootfs === undefined ? config.home : "/root";
}

/** One request's frames, as the supervisor speaks them. */
interface Event {
	type: "ready" | "started" | "chunk" | "exit" | "error";
	id?: string;
	stream?: "stdout" | "stderr";
	data?: string;
	code?: number;
	stopped?: string;
	error?: string;
}

class BwrapClient extends WorkspaceFiles implements SandboxClient {
	readonly name: string;
	private readonly config: BwrapConfig;
	private nextID = 0;

	constructor(config: BwrapConfig) {
		super();
		this.config = config;
		this.name = `bwrap:${config.workspace}`;
	}

	/**
	 * The environment a command sees. An environment engine delivers secrets by putting them in
	 * this process, so a command that inherits inherits them; `clearEnv` is how a caller asks for
	 * nothing but what it passes. Nothing else reaches the sandbox: the supervisor starts a
	 * command with exactly this map and nothing of its own.
	 */
	private environment(options: ExecOptions): Record<string, string> {
		const inherited = options.clearEnv === true ? {} : { ...process.env };
		const env: Record<string, string> = {};
		for (const [key, value] of Object.entries({ ...inherited, ...options.env })) {
			if (typeof value === "string") env[key] = value;
		}
		// The sandbox's own home, always: the machine's is not in the image, so an install there
		// would land nowhere and a `~` would mean a path the command cannot see.
		env["HOME"] = guestHome(this.config);
		env["PATH"] = searchPath(this.config.home);
		return env;
	}

	async exec(command: string | readonly string[], options: ExecOptions): Promise<ExecResult> {
		const argv = typeof command === "string" ? ["/bin/sh", "-c", command] : [...command];
		if (argv.length === 0) throw new Error("empty argv: no program to run");
		const request = {
			type: "run" as const,
			id: String(++this.nextID),
			argv,
			env: this.environment(options),
			dir: options.cwd ?? this.config.workspace,
		};
		const socket = await this.connect();
		try {
			return await this.pump(socket, request, options);
		} finally {
			socket.destroy();
		}
	}

	/** One connection per command: connecting to a Unix socket costs less than a millisecond. */
	private connect(): Promise<Socket> {
		return new Promise((resolve, reject) => {
			const socket = connect(this.config.socket);
			const failed = (error: Error): void => {
				reject(
					new Error(
						`the sandbox at ${this.config.socket} is not answering (${error.message}): the workspace's sandbox stops when the workspace is stopped`,
					),
				);
			};
			socket.once("error", failed);
			socket.once("connect", () => {
				socket.removeListener("error", failed);
				socket.on("error", () => {
					// A broken connection ends the command; the exit or the close below reports it.
				});
				resolve(socket);
			});
		});
	}

	/**
	 * Sends the request and reads its frames until it exits. A kill is a request of its own, sent
	 * on the same connection, so a command that outlives the abort is not left running.
	 */
	private pump(
		socket: Socket,
		request: {
			type: "run";
			id: string;
			argv: readonly string[];
			env: Record<string, string>;
			dir: string;
		},
		options: ExecOptions,
	): Promise<ExecResult> {
		const stdout: Uint8Array[] = [];
		const stderr: Uint8Array[] = [];
		let stopped: "aborted" | "timeout" | undefined;
		const kill = (reason: "aborted" | "timeout"): void => {
			if (stopped !== undefined) return;
			stopped = reason;
			socket.write(`${JSON.stringify({ type: "kill", id: request.id })}\n`);
		};
		const abort = (): void => kill("aborted");
		if (options.signal?.aborted === true) abort();
		else options.signal?.addEventListener("abort", abort, { once: true });
		let timer: ReturnType<typeof setTimeout> | undefined;
		if (options.timeoutMs !== undefined && options.timeoutMs > 0) {
			timer = setTimeout(() => kill("timeout"), options.timeoutMs);
		}
		socket.write(`${JSON.stringify(request)}\n`);

		return new Promise<ExecResult>((resolve, reject) => {
			let buffered = "";
			const finish = (result: ExecResult | Error): void => {
				if (timer !== undefined) clearTimeout(timer);
				options.signal?.removeEventListener("abort", abort);
				if (result instanceof Error) reject(result);
				else resolve({ ...result, ...(stopped === undefined ? {} : { stopped }) });
			};
			socket.on("data", (chunk: Buffer) => {
				buffered += chunk.toString("utf8");
				let newline = buffered.indexOf("\n");
				while (newline >= 0) {
					const line = buffered.slice(0, newline);
					buffered = buffered.slice(newline + 1);
					newline = buffered.indexOf("\n");
					if (line.trim() === "") continue;
					let event: Event;
					try {
						event = JSON.parse(line) as Event;
					} catch {
						continue;
					}
					if (event.id !== undefined && event.id !== request.id) continue;
					if (event.type === "chunk") {
						const data = new Uint8Array(Buffer.from(event.data ?? "", "base64"));
						if (event.stream === "stderr") stderr.push(data);
						else stdout.push(data);
						options.onChunk?.({ stream: event.stream ?? "stdout", data });
					} else if (event.type === "error") {
						// The supervisor could not start the program: that is a spawn failure, which
						// is what the host engine reports as a thrown error.
						finish(new Error(event.error ?? "the sandbox could not start the command"));
					} else if (event.type === "exit") {
						finish({
							code: stopped === undefined ? (event.code ?? 0) : 143,
							stdout: concatChunks(stdout),
							stderr: concatChunks(stderr),
						});
					}
				}
			});
			socket.on("close", () => {
				finish(
					new Error(
						"the sandbox closed the connection before the command finished: it may have been stopped",
					),
				);
			});
			socket.on("error", (error: Error) => finish(error));
		});
	}
}
