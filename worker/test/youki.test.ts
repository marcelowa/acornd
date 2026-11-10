import { afterEach, expect, test } from "bun:test";
import { mkdir, mkdtemp, realpath, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fakeSupervisor, type Fake } from "./fake-supervisor.ts";
import { engineName, openSandbox } from "../src/youki.ts";
import type { SandboxClient } from "../src/sandbox.ts";

/**
 * The youki client against the protocol its sandbox speaks.
 *
 * The client is the supervisor socket client bwrap.ts implements, so the streaming and the
 * kill path are bwrap's tests; what is youki's is the name, the record it reads, and the
 * refusal of a record that belongs to another engine or workspace.
 */

const opened: string[] = [];

afterEach(async () => {
	for (const dir of opened.splice(0)) await rm(dir, { recursive: true, force: true });
	delete process.env.ACORND_YOUKI_STATE;
});

interface Opened {
	dir: string;
	state: string;
	client: SandboxClient;
	fake: Fake;
}

/** A workspace, its youki record, and a supervisor to talk to. */
async function open(): Promise<Opened> {
	const root = await realpath(await mkdtemp(join(tmpdir(), "acornd-youki-")));
	opened.push(root);
	const dir = join(root, "work");
	const state = join(root, "state");
	for (const path of [dir, state, join(state, "home")]) await mkdir(path, { recursive: true });
	const fake = await fakeSupervisor(root);
	const name = "acornd-test";
	await writeFile(
		join(state, `${name}.json`),
		JSON.stringify({
			engine: "youki",
			workspace: dir,
			image: "debian:stable-slim",
			network: { mode: "off" },
			rootfs: join(root, "rootfs"),
			home: join(state, "home"),
			hidden: join(root, "hidden"),
			readOnly: [],
			socket: fake.socket,
		}),
	);
	process.env.ACORND_YOUKI_STATE = state;
	return { dir, state, client: await openSandbox(name, dir), fake };
}

test("the youki engine is named youki", () => {
	expect(engineName()).toBe("youki");
});

test("a command goes through the supervisor socket and the client names the engine", async () => {
	const { dir, client, fake } = await open();
	try {
		const result = await client.exec("printf youki-ok", {});
		expect(result.code).toBe(0);
		expect(new TextDecoder().decode(result.stdout)).toBe("youki-ok");
		expect(client.name).toBe(`youki:${dir}`);
		expect(fake.captured[0]?.argv).toEqual(["/bin/sh", "-c", "printf youki-ok"]);
	} finally {
		await fake.close();
	}
});

test("a record for another engine is refused rather than run in the wrong folder", async () => {
	const { dir, state, fake } = await open();
	try {
		const name = "acornd-test";
		await writeFile(
			join(state, `${name}.json`),
			JSON.stringify({
				engine: "bwrap",
				workspace: dir,
				image: "debian:stable-slim",
				network: { mode: "off" },
				home: join(state, "home"),
				hidden: join(state, "hidden"),
				readOnly: [],
				socket: fake.socket,
			}),
		);
		expect(openSandbox(name, dir)).rejects.toThrow(`is not the config for ${dir}`);
	} finally {
		await fake.close();
	}
});
