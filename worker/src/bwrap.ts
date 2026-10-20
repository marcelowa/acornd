import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { concatChunks, type ExecOptions, type ExecResult, type SandboxClient } from "./sandbox.ts";
import { WorkspaceFiles } from "./workspace-files.ts";

/**
 * The bubblewrap engine: namespaces built from an argv, with no runtime and no sandbox object.
 *
 * Control writes one config per workspace to `ACORNFALL_BWRAP_STATE` — the same file a person
 * can read — and this turns it into the argv of every command. That is the whole engine: there
 * is nothing to boot, nothing to attach to, and nothing that survives a command but what it
 * wrote. See docs/adr/0023-bwrap-engine.md.
 *
 * File operations are the host's, unchanged: bubblewrap binds a folder at the path it already
 * has, so a path means the same thing inside and outside.
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

/** What control resolved for one workspace: paths, network, and the private home. */
export interface BwrapConfig {
	engine: string;
	workspace: string;
	image: string;
	network: string;
	/** The workspace's own unpacked image, bound as `/`. Absent means the host's root. */
	rootfs?: string;
	/** The private home language toolchains install into. */
	home: string;
	/** The install folder, hidden by a tmpfs: credentials and state live in there. */
	hidden: string;
	/** Global resource folders, bound back read-only after the tmpfs. */
	readOnly?: string[];
}

const systemPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin";

/**
 * What a command's PATH starts with. Tools a workspace installs itself land in its own home,
 * so the two folders every unprivileged installer uses come first — otherwise a toolchain that
 * survived one command would still not be found by the next one.
 */
function searchPath(home: string): string {
	return `${home}/.local/bin:${home}/.cargo/bin:${home}/.bun/bin:${systemPath}`;
}

/**
 * The argv that puts one command inside this workspace's sandbox: the mounts, the namespaces,
 * the environment, and the command. It is a function of the config alone, which is what makes
 * it testable without a user namespace.
 */
export function bwrapArguments(
	config: BwrapConfig,
	argv: readonly string[],
	cwd: string,
	env: Record<string, string>,
): string[] {
	const image = config.rootfs !== undefined;
	return [
		// The workspace's own image when it has one: its `/` is then a tree that is disposable
		// with the workspace, and an install in it outlives the command. Without one the host's
		// root is the root, read-only, which is the older shape.
		...(image
			? [
					"--bind",
					config.rootfs as string,
					"/",
					// A root of its own: a single mapping makes root inside the user who owns the
					// sandbox outside, so files still land owned by them.
					"--uid",
					"0",
					"--gid",
					"0",
					// The resolver is the one thing a root genuinely needs from its machine.
					"--ro-bind",
					"/etc/resolv.conf",
					"/etc/resolv.conf",
				]
			: ["--ro-bind", "/", "/"]),
		"--dev",
		"/dev",
		"--proc",
		"/proc",
		"--tmpfs",
		"/tmp",
		// The install folder is hidden only when the host's root is the root: credentials and
		// state live in there, and inside an image they are not on the filesystem at all.
		...(image ? [] : ["--tmpfs", config.hidden]),
		// The global resources, read-only at their own paths: a skill's instructions name their
		// own directory, and a script beside a skill has to be a path that works.
		...[...(config.readOnly ?? [])].flatMap((path) => ["--ro-bind", path, path]),
		"--bind",
		config.home,
		guestHome(config),
		"--bind",
		config.workspace,
		config.workspace,
		"--unshare-all",
		// Egress is the workspace's setting: undoing the netns is how it is granted.
		...(config.network === "off" ? [] : ["--share-net"]),
		// A command that outlives the worker is a command nobody can stop; the worker's kill
		// then reaches bubblewrap and bubblewrap takes its child with it.
		"--die-with-parent",
		"--clearenv",
		...Object.entries(env).flatMap(([key, value]) => ["--setenv", key, value]),
		"--chdir",
		cwd,
		"--",
		...argv,
	];
}

/** Where the private home appears inside the sandbox: the root's own home when there is an
 * image, and its real path when the host's root is the root. */
export function guestHome(config: BwrapConfig): string {
	return config.rootfs === undefined ? config.home : "/root";
}

class BwrapClient extends WorkspaceFiles implements SandboxClient {
	readonly name: string;
	private readonly config: BwrapConfig;

	constructor(config: BwrapConfig) {
		super();
		this.config = config;
		this.name = `bwrap:${config.workspace}`;
	}

	/**
	 * The environment a command sees. An environment engine delivers secrets by putting them
	 * in this process, so a command that inherits inherits them; `clearEnv` is how a caller
	 * asks for nothing but what it passes. Nothing else leaks in either way: the sandbox is
	 * built with `--clearenv` and only these entries are handed over.
	 */
	private environment(options: ExecOptions): Record<string, string> {
		const inherited = options.clearEnv === true ? {} : { ...process.env };
		const env: Record<string, string> = {};
		for (const [key, value] of Object.entries({ ...inherited, ...options.env })) {
			if (typeof value === "string") env[key] = value;
		}
		env["HOME"] ??= guestHome(this.config);
		env["PATH"] = searchPath(env["HOME"] ?? guestHome(this.config));
		return env;
	}

	async exec(command: string | readonly string[], options: ExecOptions): Promise<ExecResult> {
		const argv = typeof command === "string" ? ["/bin/sh", "-c", command] : [...command];
		if (argv.length === 0) throw new Error("empty argv: no program to run");
		const env = this.environment(options);
		const args = bwrapArguments(this.config, argv, options.cwd ?? this.config.workspace, env);
		const proc = Bun.spawn(["bwrap", ...args], {
			stdin: "ignore",
			stdout: "pipe",
			stderr: "pipe",
			// Its own session, so the kill below takes bubblewrap and everything it started.
			detached: true,
		});
		const stdout: Uint8Array[] = [];
		const stderr: Uint8Array[] = [];
		let stopped: "aborted" | "timeout" | undefined;
		const stop = (reason: "aborted" | "timeout"): void => {
			if (stopped !== undefined) return;
			stopped = reason;
			try {
				process.kill(-proc.pid, "SIGKILL");
			} catch {
				proc.kill("SIGKILL");
			}
		};
		const abort = (): void => stop("aborted");
		if (options.signal?.aborted === true) abort();
		else options.signal?.addEventListener("abort", abort, { once: true });
		let timer: ReturnType<typeof setTimeout> | undefined;
		if (options.timeoutMs !== undefined && options.timeoutMs > 0) {
			timer = setTimeout(() => stop("timeout"), options.timeoutMs);
		}
		const drain = async (
			stream: ReadableStream<Uint8Array>,
			chunks: Uint8Array[],
			kind: "stdout" | "stderr",
		): Promise<void> => {
			for await (const data of stream) {
				chunks.push(data);
				options.onChunk?.({ stream: kind, data });
			}
		};
		try {
			await Promise.all([
				drain(proc.stdout, stdout, "stdout"),
				drain(proc.stderr, stderr, "stderr"),
			]);
		} finally {
			if (timer !== undefined) clearTimeout(timer);
			options.signal?.removeEventListener("abort", abort);
		}
		const out = concatChunks(stdout);
		const fail = concatChunks(stderr);
		const code = stopped === undefined ? await proc.exited : 143;
		// bubblewrap could not start the program at all — it is not in this sandbox. The host
		// engine reports that as a thrown error, so this engine does too: an argv that names a
		// program that is not there is a spawn failure, not a command that exited 1.
		const spawnFailure =
			/^bwrap: execvp .*: (No such file or directory|Permission denied)$/m.exec(
				new TextDecoder().decode(fail),
			);
		if (spawnFailure !== null && stopped === undefined) {
			throw new Error(spawnFailure[0]);
		}
		return {
			code,
			stdout: out,
			stderr: fail,
			...(stopped === undefined ? {} : { stopped }),
		};
	}
}
