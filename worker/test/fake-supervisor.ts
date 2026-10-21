/**
 * A supervisor that speaks the protocol and runs what it is told, on this machine.
 *
 * It stands in for the sandbox in the tests that are about the client or the harness contract:
 * the real supervisor is Go and the end-to-end tests run it, but a test of what a request carries
 * should not need a sandbox to carry it.
 */
import { createServer, type Server, type Socket } from "node:net";
import { join } from "node:path";

export interface Captured {
	id: string;
	argv: string[];
	env: Record<string, string>;
	dir: string;
}

export interface Fake {
	socket: string;
	captured: Captured[];
	children: Map<string, ReturnType<typeof Bun.spawn>>;
	close: () => Promise<void>;
}

/** A supervisor that speaks the protocol and runs what it is told, on this machine. */
export function fakeSupervisor(dir: string): Promise<Fake> {
	const socket = join(dir, "supervisor.sock");
	const captured: Captured[] = [];
	const children = new Map<string, ReturnType<typeof Bun.spawn>>();
	const server: Server = createServer((connection: Socket) => {
		let buffered = "";
		const send = (event: unknown): void => {
			connection.write(`${JSON.stringify(event)}\n`);
		};
		const run = async (request: Captured): Promise<void> => {
			captured.push(request);
			if (request.argv[0] === "not-a-program") {
				send({
					type: "error",
					id: request.id,
					error: "exec: not-a-program: no such file or directory",
				});
				return;
			}
			try {
				const child = Bun.spawn(request.argv, {
					cwd: request.dir,
					env: request.env,
					stdout: "pipe",
					stderr: "pipe",
					detached: true,
				});
				children.set(request.id, child);
				send({ type: "started", id: request.id, pid: child.pid });
				const drain = async (
					stream: ReadableStream<Uint8Array>,
					name: "stdout" | "stderr",
				) => {
					for await (const chunk of stream) {
						send({
							type: "chunk",
							id: request.id,
							stream: name,
							data: Buffer.from(chunk).toString("base64"),
						});
					}
				};
				await Promise.all([drain(child.stdout, "stdout"), drain(child.stderr, "stderr")]);
				const code = await child.exited;
				children.delete(request.id);
				send({ type: "exit", id: request.id, code });
			} catch (error) {
				send({ type: "error", id: request.id, error: (error as Error).message });
			}
		};
		connection.on("data", (data: Buffer) => {
			buffered += data.toString("utf8");
			let newline = buffered.indexOf("\n");
			while (newline >= 0) {
				const line = buffered.slice(0, newline);
				buffered = buffered.slice(newline + 1);
				newline = buffered.indexOf("\n");
				if (line.trim() === "") continue;
				const request = JSON.parse(line) as { type: string; id?: string };
				if (request.type === "run") void run(request as unknown as Captured);
				if (request.type === "kill" && request.id !== undefined) {
					const child = children.get(request.id);
					if (child !== undefined) {
						try {
							process.kill(-child.pid, "SIGKILL");
						} catch {
							child.kill("SIGKILL");
						}
					}
				}
			}
		});
	});
	return new Promise((resolve) => {
		server.listen(socket, () => {
			resolve({
				socket,
				captured,
				children,
				close: () =>
					new Promise<void>((done) => {
						for (const child of children.values()) child.kill("SIGKILL");
						server.close(() => done());
					}),
			});
		});
	});
}
