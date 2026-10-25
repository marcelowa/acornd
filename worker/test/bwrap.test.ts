import { afterEach, expect, test } from "bun:test";
import { mkdir, mkdtemp, realpath, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fakeSupervisor, type Fake } from "./fake-supervisor.ts";
import { openSandbox } from "../src/bwrap.ts";
import type { SandboxClient } from "../src/sandbox.ts";

/**
 * The bubblewrap client against the protocol its sandbox speaks.
 *
 * The sandbox itself — bubblewrap, its mounts, the supervisor — is Go's, and the end-to-end tests
 * cover it against a real one. What is only the client's is here: that a request carries the argv
 * it was given and exactly the environment it built, that output streams, that an abort becomes a
 * kill, and that a command the sandbox could not start is a spawn failure rather than an exit code.
 *
 * The fake answers the protocol and runs the real commands, so the streaming path is real even
 * though the sandbox is not.
 */

const opened: string[] = [];

afterEach(async () => {
	for (const dir of opened.splice(0)) await rm(dir, { recursive: true, force: true });
	delete process.env.ACORNFALL_BWRAP_STATE;
});

interface Opened {
	dir: string;
	state: string;
	client: SandboxClient;
	fake: Fake;
}

/** A workspace, its config, and a supervisor to talk to. */
async function open(
	network: "pasta" | "off" = "off",
	options: { ignoreKill?: boolean; secretProxy?: { url: string; bundle: string } } = {},
): Promise<Opened> {
	const root = await realpath(await mkdtemp(join(tmpdir(), "acornfall-bwrap-")));
	opened.push(root);
	const dir = join(root, "work");
	const state = join(root, "state");
	for (const path of [dir, state, join(state, "home")]) await mkdir(path, { recursive: true });
	const fake = await fakeSupervisor(root, options);
	const name = "acornfall-test";
	await writeFile(
		join(state, `${name}.json`),
		JSON.stringify({
			engine: "bwrap",
			workspace: dir,
			image: "debian:stable-slim",
			network:
				network === "pasta"
					? { mode: "pasta", address: "10.0.2.15/24", gateway: "10.0.2.2" }
					: { mode: "off" },
			rootfs: join(root, "rootfs"),
			home: join(state, "home"),
			hidden: join(root, "hidden"),
			readOnly: [],
			socket: fake.socket,
			...(options.secretProxy === undefined ? {} : { secretProxy: options.secretProxy }),
		}),
	);
	process.env.ACORNFALL_BWRAP_STATE = state;
	return { dir, state, client: await openSandbox(name, dir), fake };
}

test("a command streams as it runs and reports its exit code", async () => {
	const { client, fake } = await open();
	try {
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
	} finally {
		await fake.close();
	}
});

test("the request carries the argv it was given and the directory the command runs in", async () => {
	const { dir, client, fake } = await open();
	try {
		await client.exec(["printf", "%s", "argv"], {});
		await client.mkdir(join(dir, "sub"));
		await client.exec("pwd", { cwd: join(dir, "sub") });
		const [first, second] = fake.captured;
		expect(first?.argv).toEqual(["printf", "%s", "argv"]);
		expect(first?.dir).toBe(dir);
		expect(second?.argv).toEqual(["/bin/sh", "-c", "pwd"]);
		expect(second?.dir).toBe(join(dir, "sub"));
	} finally {
		await fake.close();
	}
});

test("the environment is exactly what the caller gets: secrets in, nothing else", async () => {
	const { state, client, fake } = await open();
	try {
		await client.exec("true", { env: { GITHUB_TOKEN: "from the vault" } });
		const withSecret = fake.captured.at(-1);
		expect(withSecret?.env["GITHUB_TOKEN"]).toBe("from the vault");
		// The private home and a search path that finds a toolchain installed there.
		// The sandbox's home, not the machine's: the machine's home is not in the image.
		expect(withSecret?.env["HOME"]).toBe("/root");
		expect(withSecret?.env["PATH"]).toStartWith(`${join(state, "home")}/.local/bin`);

		// clearEnv is the caller saying "nothing but what I pass", and it has to mean it.
		process.env.ACORNFALL_SHOULD_NOT_LEAK = "yes";
		await client.exec("true", { clearEnv: true });
		const cleared = fake.captured.at(-1);
		expect(cleared?.env["ACORNFALL_SHOULD_NOT_LEAK"]).toBeUndefined();
		delete process.env.ACORNFALL_SHOULD_NOT_LEAK;
	} finally {
		await fake.close();
	}
});

test("a timeout and an abort stop the command and say which happened", async () => {
	const { client, fake } = await open();
	try {
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
	} finally {
		await fake.close();
	}
});

test("a program the sandbox cannot start is a spawn failure, not an exit code", async () => {
	const { client, fake } = await open();
	try {
		expect(client.exec(["not-a-program"], {})).rejects.toThrow("no such file or directory");
	} finally {
		await fake.close();
	}
});

test("files are read, written, and listed in the workspace", async () => {
	const { dir, client, fake } = await open();
	try {
		const hello = join(dir, "hello.txt");
		await client.write(hello, "from the test\n");
		expect(await client.read(hello)).toBe("from the test\n");
		const entries = await client.list(dir);
		expect(entries.map((entry) => entry.name)).toEqual(["hello.txt"]);
		await client.rename(hello, join(dir, "renamed.txt"));
		expect(await client.read(join(dir, "renamed.txt"))).toBe("from the test\n");
		await client.remove(join(dir, "renamed.txt"), {});
		expect(await client.read(join(dir, "renamed.txt"))).toBeNull();
	} finally {
		await fake.close();
	}
});

test("a config for another workspace is refused", async () => {
	const { fake } = await open();
	try {
		expect(openSandbox("acornfall-test", "/somewhere/else")).rejects.toThrow(
			"is not the config for",
		);
	} finally {
		await fake.close();
	}
});

test("a sandbox that is not answering says so, and what to do, without guessing", async () => {
	const { client, fake } = await open();
	await fake.close();
	const failure = client.exec("true", {});
	expect(failure).rejects.toThrow("is not answering");
	expect(failure).rejects.toThrow("restarting acornfall starts it again");
});

test("a command the supervisor cannot end is still reported as stopped", async () => {
	// The fake takes the kill request and does nothing with it: a supervisor wedged on a process
	// that left the run's group holding the output pipe. The client's own grace is what settles
	// the call, so an abort or a timeout cannot leave a turn waiting forever.
	const { client, fake } = await open("off", { ignoreKill: true });
	try {
		const started = Date.now();
		const result = await client.exec("sleep 30", { timeoutMs: 50 });
		expect(result.stopped).toBe("timeout");
		expect(result.code).toBe(143);
		expect(Date.now() - started).toBeLessThan(5000);
	} finally {
		await fake.close();
	}
});

test("an attached worker tells its sandbox it is still here", async () => {
	const { fake } = await open();
	try {
		// The first one goes out with the client, before any command: a sandbox that is already
		// near the end of its window should not wait five minutes to hear that it is not alone.
		for (let attempt = 0; attempt < 20 && fake.keepalives.length === 0; attempt += 1) {
			await Bun.sleep(10);
		}
		expect(fake.keepalives.length).toBeGreaterThan(0);
	} finally {
		await fake.close();
	}
});

test("a workspace with a proxy puts it in the environment its commands get", async () => {
	const proxy = {
		url: "http://acornfall:a-token@127.0.0.1:41234",
		bundle: "/etc/ssl/certs/ca-certificates.crt",
	};
	const { client, fake } = await open("pasta", { secretProxy: proxy });
	try {
		await client.exec("true", {});
		const env = fake.captured.at(-1)?.env;
		expect(env?.["HTTPS_PROXY"]).toBe(proxy.url);
		expect(env?.["HTTP_PROXY"]).toBe(proxy.url);
		// The sandbox's own loopback is not an API call.
		expect(env?.["NO_PROXY"]).toBe("127.0.0.1,localhost,::1");
		// Node reads its own root list from here; the bundle is the one the CA was added to.
		expect(env?.["NODE_EXTRA_CA_CERTS"]).toBe(proxy.bundle);
		// The credential is the proxy's, not a secret's: no value is anywhere in the environment.
		expect(Object.values(env ?? {}).some((value) => value.includes("sk-"))).toBe(false);
	} finally {
		await fake.close();
	}
});

test("a command does not inherit the gateway's environment", async () => {
	const { client, fake } = await open();
	const before = process.env.TERM;
	process.env.ACORNFALL_AMBIENT_TOKEN = "a-token-the-gateway-was-started-with";
	process.env.TERM = "a-term-from-the-shell";
	try {
		await client.exec("true", {});
		const env = fake.captured.at(-1)?.env;
		// The worker was started with acornfall's own environment, and a command that inherited it
		// inherited whatever the gateway was holding. Only the handful a shell needs crosses over.
		expect(env?.["ACORNFALL_AMBIENT_TOKEN"]).toBeUndefined();
		expect(env?.["TERM"]).toBe("a-term-from-the-shell");
		expect(env?.["HOME"]).toBe("/root");
	} finally {
		delete process.env.ACORNFALL_AMBIENT_TOKEN;
		if (before === undefined) delete process.env.TERM;
		else process.env.TERM = before;
		await fake.close();
	}
});

test("a command holds the placeholder for a secret it may use", async () => {
	const { client, fake } = await open("pasta");
	try {
		await client.exec("true", { secrets: { GITHUB_TOKEN: "$MSB_GITHUB_TOKEN" } });
		const env = fake.captured.at(-1)?.env;
		// The value never reaches the guest: the proxy swaps this literal for the host the secret
		// names, and a command that prints its environment prints the placeholder.
		expect(env?.["GITHUB_TOKEN"]).toBe("$MSB_GITHUB_TOKEN");
	} finally {
		await fake.close();
	}
});
