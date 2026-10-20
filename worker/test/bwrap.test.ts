import { afterEach, expect, test } from "bun:test";
import { mkdir, mkdtemp, realpath, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { openSandbox } from "../src/bwrap.ts";
import type { SandboxClient } from "../src/sandbox.ts";

/**
 * The bubblewrap engine through its own client, against the real binary. The engine is a
 * namespace view built per command, so what there is to check is that the view is the one the
 * config asks for: the workspace bound read-write, the install folder hidden, the global
 * resources read-only, the network a setting, and a kill that reaches the command.
 *
 * A machine without a working user namespace skips: the driver's own refusal is the Go
 * suite's business, and this one needs a real sandbox to say anything.
 */
const probe = Bun.spawnSync(["bwrap", "--ro-bind", "/", "/", "--unshare-all", "--", "/bin/true"]);
const usable = probe.exitCode === 0;
const whenUsable = usable ? test : test.skip;

const opened: string[] = [];

afterEach(async () => {
	for (const dir of opened.splice(0)) await rm(dir, { recursive: true, force: true });
	delete process.env.ACORNFALL_BWRAP_STATE;
});

interface Opened {
	dir: string;
	hidden: string;
	global: string;
	readOnly: string;
	client: SandboxClient;
}

/** A workspace and the config control would have written for it. */
async function open(network: "on" | "off" = "off"): Promise<Opened> {
	const root = await realpath(await mkdtemp(join(tmpdir(), "acornfall-bwrap-")));
	opened.push(root);
	const dir = join(root, "work");
	const state = join(root, "state");
	const hidden = join(root, "home");
	const global = join(root, "agents");
	const readOnly = join(hidden, "skills");
	for (const path of [dir, state, hidden, global, readOnly])
		await mkdir(path, { recursive: true });
	await writeFile(join(hidden, "credentials.json"), '{"secret":"not for the sandbox"}\n');
	await writeFile(join(readOnly, "SKILL.md"), "read me\n");
	await writeFile(join(dir, "note.txt"), "in the workspace\n");

	const name = "acornfall-test";
	await writeFile(
		join(state, `${name}.json`),
		JSON.stringify({
			engine: "bwrap",
			workspace: dir,
			image: "debian:stable-slim",
			network,
			home: join(state, "home"),
			hidden,
			readOnly: [global, readOnly],
		}),
	);
	await mkdir(join(state, "home"), { recursive: true });
	process.env.ACORNFALL_BWRAP_STATE = state;
	return { dir, hidden, global, readOnly, client: await openSandbox(name, dir) };
}

whenUsable("a command streams as it runs and reports its exit code", async () => {
	const { client } = await open();
	const chunks: string[] = [];
	const result = await client.exec("printf one; sleep 0.2; printf two", {
		onChunk: (chunk) => chunks.push(new TextDecoder().decode(chunk.data)),
	});
	expect(result.code).toBe(0);
	expect(result.stopped).toBeUndefined();
	expect(new TextDecoder().decode(result.stdout)).toBe("onetwo");
	// Streaming, not buffering: the first chunk arrives before the command ends.
	expect(chunks.length).toBeGreaterThan(1);

	const failed = await client.exec("printf bad >&2; exit 3", {});
	expect(failed.code).toBe(3);
	expect(new TextDecoder().decode(failed.stderr)).toBe("bad");
});

whenUsable("the workspace is bound read-write, and writes are the user's own", async () => {
	const { dir, client } = await open();
	await client.exec("printf inside > written.txt", { cwd: dir });
	expect(await client.read(join(dir, "written.txt"))).toBe("inside");
	expect(new TextDecoder().decode((await client.exec("pwd", {})).stdout).trim()).toBe(dir);
});

whenUsable("the install folder is hidden and the global resources are read-only", async () => {
	const { hidden, readOnly, dir, client } = await open();
	// credentials.json lives inside the hidden folder: a sandbox that could read it would be
	// reading the worker's key, which is the one thing ADR 0012 keeps out.
	const hiddenFile = await client.exec(`cat ${join(hidden, "credentials.json")}`, {
		clearEnv: true,
	});
	expect(hiddenFile.code).not.toBe(0);
	expect(new TextDecoder().decode(hiddenFile.stdout)).toBe("");

	// A skill's own directory is readable at its own path, and writing it is refused.
	const skill = await client.exec(`cat ${join(readOnly, "SKILL.md")}`, { clearEnv: true });
	expect(new TextDecoder().decode(skill.stdout)).toBe("read me\n");
	const tamper = await client.exec(`printf x >> ${join(readOnly, "SKILL.md")}`, {
		clearEnv: true,
	});
	expect(tamper.code).not.toBe(0);
	expect((await client.exec(`printf x > ${join(dir, "ok.txt")}`, { clearEnv: true })).code).toBe(
		0,
	);
});

whenUsable("the host root is read-only, and the workspace is not", async () => {
	const { client } = await open();
	const write = await client.exec("printf x > /usr/local/break-it", { clearEnv: true });
	expect(write.code).not.toBe(0);
	expect(new TextDecoder().decode(write.stderr)).toContain("Read-only file system");
});

whenUsable("the network is the workspace's setting", async () => {
	const off = await open("off");
	const isolates = await off.client.exec("cat /proc/net/dev", { clearEnv: true });
	// A netns of its own: loopback is the only interface, so egress is not there to leak.
	const interfaces = new TextDecoder()
		.decode(isolates.stdout)
		.split("\n")
		.filter((line) => line.includes(":"))
		.map((line) => line.trim().split(":")[0]);
	expect(interfaces).toEqual(["lo"]);

	const on = await open("on");
	const shares = await on.client.exec("cat /proc/net/dev", { clearEnv: true });
	expect(new TextDecoder().decode(shares.stdout)).toContain("eth");
});

whenUsable("secrets arrive in the environment and nothing else does", async () => {
	const { client } = await open();
	const secret = await client.exec('printf %s "$GITHUB_TOKEN"', {
		env: { GITHUB_TOKEN: "from the vault" },
	});
	expect(new TextDecoder().decode(secret.stdout)).toBe("from the vault");
	// clearEnv is the caller saying "nothing but what I pass", and it has to mean it.
	const cleared = await client.exec('printf %s "$GITHUB_TOKEN"', { clearEnv: true });
	expect(new TextDecoder().decode(cleared.stdout)).toBe("");
});

whenUsable("a timeout and an abort stop the command and say which happened", async () => {
	const { client } = await open();
	const started = Date.now();
	const timedOut = await client.exec("sleep 5", { timeoutMs: 200 });
	expect(timedOut.stopped).toBe("timeout");
	expect(timedOut.code).toBe(143);
	expect(Date.now() - started).toBeLessThan(3000);

	const controller = new AbortController();
	const timer = setTimeout(() => controller.abort(), 100);
	const aborted = await client.exec("sleep 5", { signal: controller.signal });
	clearTimeout(timer);
	expect(aborted.stopped).toBe("aborted");
	expect(aborted.code).toBe(143);
});

whenUsable("the workspace's own home is where installs land", async () => {
	const { client } = await open();
	const home = await client.exec('printf %s "$HOME"', { clearEnv: true });
	const printed = new TextDecoder().decode(home.stdout);
	// The config's own home, not the worker's: only what a workspace installs lives here.
	expect(printed).toEndWith("/state/home");
	const written = await client.exec("printf installed > $HOME/.toolchain", { clearEnv: true });
	expect(written.code).toBe(0);
	expect(await client.read(join(printed, ".toolchain"))).toBe("installed");
});

test("a config for another workspace is refused", async () => {
	const { client } = await open();
	expect(client.name).toStartWith("bwrap:");
	// The name control sends has to resolve to the config of the folder the worker was told
	// about, or a command could run in somebody else's workspace.
	expect(openSandbox("acornfall-test", "/somewhere/else")).rejects.toThrow(
		"is not the config for",
	);
});
