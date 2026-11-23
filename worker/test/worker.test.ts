/**
 * The command table, driven in-process: `createWorker` with memory storage, a refused sandbox,
 * a fixed clock, and a writer that keeps every frame. `protocol.test.ts` drives the same table
 * through a real process; this is where a handler can be tested without one.
 */
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { join } from "node:path";
import { MemoryStorage } from "@earendil-works/pi-durable";
import type { Frame } from "../src/protocol.ts";
import { refusedSandbox } from "../src/sandbox.ts";
import { createWorker } from "../src/worker.ts";
import { cleanupScratch, tempDir, write } from "./scratch.ts";

const NOW = 1_800_000_000_000;

/** A worker driven by lines: a call answers with its response, and `wait` finds any other frame. */
interface Driven {
	exited: Promise<void>;
	call<T = unknown>(command: Record<string, unknown>): Promise<T>;
	wait(predicate: (frame: Frame) => boolean, timeout?: number): Promise<Frame>;
}

async function start(): Promise<Driven> {
	const home = await tempDir("acornd-worker-");
	const workspace = join(home, "workspace");
	const script = join(home, "script.json");
	await write(
		script,
		JSON.stringify({
			// Slow enough that a turn is still running while a test acts on it.
			tokensPerSecond: 20,
			rules: [
				{
					match: "keep going",
					reply: "a slow answer that goes on and on and on and on and finally says FINISHED",
				},
				{ match: "then this", reply: "FOLLOWED UP" },
				{
					match: "work first",
					tools: [{ name: "bash", args: { command: "true" } }],
					reply: "WORK DONE",
				},
				{ match: "hello", reply: "HI THERE" },
			],
		}),
	);
	const frames: Frame[] = [];
	const waiters: { predicate: (frame: Frame) => boolean; resolve: (frame: Frame) => void }[] = [];
	let exit = (): void => undefined;
	const exited = new Promise<void>((resolve) => {
		exit = resolve;
	});
	const created = await createWorker(
		{
			workspace,
			home,
			credentials: join(home, "credentials.json"),
			secrets: [],
			devScript: script,
		},
		{
			openStorage: async () => new MemoryStorage(),
			openSandbox: async () => refusedSandbox("no sandbox in this test"),
			write: (frame) => {
				const index = waiters.findIndex((waiter) => waiter.predicate(frame));
				if (index >= 0) waiters.splice(index, 1)[0]?.resolve(frame);
				else frames.push(frame);
			},
			now: () => NOW,
			exit,
		},
	);
	await created.start();

	const wait = (predicate: (frame: Frame) => boolean, timeout = 15_000): Promise<Frame> => {
		const existing = frames.findIndex(predicate);
		if (existing >= 0) return Promise.resolve(frames.splice(existing, 1)[0]!);
		return new Promise((resolve, reject) => {
			const timer = setTimeout(
				() => reject(new Error("timed out waiting for a frame")),
				timeout,
			);
			waiters.push({
				predicate,
				resolve: (frame) => {
					clearTimeout(timer);
					resolve(frame);
				},
			});
		});
	};
	let next = 0;
	const call = async <T>(command: Record<string, unknown>): Promise<T> => {
		const id = `t${++next}`;
		const answered = wait((frame) => frame.type === "response" && frame.id === id);
		created.receive(JSON.stringify({ id, ...command }));
		const frame = await answered;
		if (frame.type === "response" && !frame.ok) throw new Error(frame.error);
		return (frame as { data?: unknown }).data as T;
	};
	return { exited, call, wait };
}

/** A conversation opened on the scripted model, with its view attached. */
async function opened(worker: Driven): Promise<string> {
	const { conversationId } = await worker.call<{ conversationId: string }>({ type: "create" });
	await worker.call({
		type: "open",
		conversationId,
		agent: { provider: "faux", modelId: "faux-1" },
	});
	await worker.call({ type: "attach", conversationId });
	return conversationId;
}

function submission(worker: Driven, conversationId: string, submissionId: string): Promise<Frame> {
	return worker.wait(
		(frame) =>
			frame.type === "submission" &&
			frame.conversationId === conversationId &&
			frame.submissionId === submissionId,
	);
}

/** The transcript of the newest view that says `until`. */
async function transcript(worker: Driven, conversationId: string, until: string): Promise<string> {
	const frame = await worker.wait(
		(candidate) =>
			candidate.type === "view" &&
			candidate.conversationId === conversationId &&
			JSON.stringify(candidate.value).includes(until),
	);
	return JSON.stringify((frame as { value: unknown }).value);
}

/** The message a call failed with; a call that succeeded fails the test. */
async function failure(call: Promise<unknown>): Promise<string> {
	return await call.then(
		() => {
			throw new Error("the call succeeded");
		},
		(error: Error) => error.message,
	);
}

let worker: Driven;

beforeAll(async () => {
	worker = await start();
});

afterAll(cleanupScratch);

describe("createWorker", () => {
	test("describe names the workspace and the scripted provider", async () => {
		const described = await worker.call<{
			workdir: string;
			providers: { id: string; authenticated: boolean }[];
		}>({ type: "describe" });
		expect(described.workdir.endsWith("workspace")).toBe(true);
		expect(described.providers.some((p) => p.id === "faux" && p.authenticated)).toBe(true);
	});

	test("open applies the agent and answers what it resolved to", async () => {
		const { conversationId } = await worker.call<{ conversationId: string }>({
			type: "create",
		});
		const answer = await worker.call<{
			conversationId: string;
			model: { provider: string; modelId: string };
		}>({
			type: "open",
			conversationId,
			agent: { provider: "faux", modelId: "faux-1" },
		});
		expect(answer.conversationId).toBe(conversationId);
		expect(answer.model).toEqual({ provider: "faux", modelId: "faux-1" });
	});

	test("an unknown command is an error response", async () => {
		for (const type of ["not-a-command", "constructor"]) {
			expect(await failure(worker.call({ type }))).toContain("unsupported command");
		}
	});

	test("a prompt while busy steers into the running turn", async () => {
		const conversationId = await opened(worker);
		const first = await worker.call<{ submissionId: string }>({
			type: "prompt",
			conversationId,
			text: "work first",
		});
		const steered = await worker.call<{ submissionId: string }>({
			type: "prompt",
			conversationId,
			text: "hello",
			whenBusy: "steer",
		});
		await submission(worker, conversationId, first.submissionId);
		await submission(worker, conversationId, steered.submissionId);
		const text = await transcript(worker, conversationId, "HI THERE");
		// The steer lands at the tool boundary, so the run answers it instead of its own reply.
		expect(text.indexOf("toolResult")).toBeLessThan(text.indexOf('"hello"'));
		expect(text).not.toContain("WORK DONE");
	});

	test("a prompt while busy follows up after the running turn", async () => {
		const conversationId = await opened(worker);
		const first = await worker.call<{ submissionId: string }>({
			type: "prompt",
			conversationId,
			text: "work first",
		});
		const followed = await worker.call<{ submissionId: string }>({
			type: "prompt",
			conversationId,
			text: "then this",
			whenBusy: "followUp",
		});
		await submission(worker, conversationId, first.submissionId);
		await submission(worker, conversationId, followed.submissionId);
		const text = await transcript(worker, conversationId, "FOLLOWED UP");
		// A follow-up waits for the run to finish its own reply.
		expect(text.indexOf("WORK DONE")).toBeLessThan(text.indexOf('"then this"'));
	});

	test("a prompt while busy can be rejected", async () => {
		const conversationId = await opened(worker);
		const first = await worker.call<{ submissionId: string }>({
			type: "prompt",
			conversationId,
			text: "keep going",
		});
		const refused = worker.call({
			type: "prompt",
			conversationId,
			text: "hello",
			whenBusy: "reject",
		});
		expect(await failure(refused)).toContain("busy");
		await submission(worker, conversationId, first.submissionId);
	});

	test("abort stops the run and names who stopped it", async () => {
		const conversationId = await opened(worker);
		await worker.call({ type: "prompt", conversationId, text: "keep going" });
		await transcript(worker, conversationId, "slow answer");
		await worker.call({
			type: "abort",
			conversationId,
			author: { email: "ana@example.com", name: "Ana Silva" },
		});
		const text = await transcript(worker, conversationId, "ana@example.com");
		expect(text).not.toContain("FINISHED");
	});

	test("shutdown answers, closes the harness, and exits", async () => {
		// Its own worker: the rest of this file shares one.
		const own = await start();
		const answered = await own.call({ type: "shutdown" });
		expect(answered).toBeUndefined();
		await own.exited;
	});
});
