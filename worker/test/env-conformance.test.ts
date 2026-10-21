import { describe, expect, it } from "bun:test";
import { mkdir, mkdtemp, realpath, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { registerEnvConformance } from "@earendil-works/pi-durable/testing";
import { fakeSupervisor } from "./fake-supervisor.ts";
import { openSandbox as openBwrap } from "../src/bwrap.ts";
import { openSandbox } from "../src/host.ts";
import { SandboxExecutionEnv } from "../src/sandbox-env.ts";

/**
 * The environment over the host engine, checked against the harness's own conformance suite.
 *
 * `ExecutionEnv` is the seam every tool reads and writes through, and it grows: 1.0.3 added
 * readers, pagers, watchers, and argv exec. The suite is the contract's own idea of what
 * those mean; this is what says the worker's implementation kept up. The host engine needs
 * no hypervisor, so the check runs without one — a fresh empty directory per case, as the
 * provider contract asks.
 */
registerEnvConformance(
	{ describe, expect, it },
	"SandboxExecutionEnv (host engine) conformance",
	async (use) => {
		const dir = await realpath(await mkdtemp(join(tmpdir(), "acornfall-envconf-")));
		const sandbox = await openSandbox("conformance", dir);
		try {
			await use(new SandboxExecutionEnv(sandbox, dir));
		} finally {
			await rm(dir, { recursive: true, force: true });
		}
	},
	{ shell: ["sh", "-c"] },
);

/**
 * The same contract over the bubblewrap engine: the third client, and the one that has to pass
 * without a hypervisor. A machine that will not make a user namespace skips, because there is
 * no sandbox to conform to; the driver's refusal is covered in the Go suite.
 */
// `Bun.spawnSync` throws when the program is missing instead of returning a status, so the
// presence check comes first: a machine without bubblewrap skips this suite rather than failing the
// run — which is what CI, having no bubblewrap, found the hard way.
const bwrapUsable =
	Bun.which("bwrap") !== null &&
	Bun.spawnSync(["bwrap", "--ro-bind", "/", "/", "--unshare-all", "--", "/bin/true"]).exitCode ===
		0;

// `make test-bwrap` is the run that says this tier ran, so a skip there would be a green run that
// tested nothing on the one machine meant to test it: with ACORNFALL_BWRAP_ONLY it fails instead.
if (!bwrapUsable && process.env.ACORNFALL_BWRAP_ONLY === "1") {
	throw new Error(
		"no usable bubblewrap on this machine (apt install bubblewrap): the bwrap tier needs it",
	);
}

if (bwrapUsable) {
	registerEnvConformance(
		{ describe, expect, it },
		"SandboxExecutionEnv (bwrap engine) conformance",
		async (use) => {
			const workspace = await realpath(
				await mkdtemp(join(tmpdir(), "acornfall-envconf-bwrap-")),
			);
			const state = await mkdtemp(join(tmpdir(), "acornfall-envconf-state-"));
			const name = "acornfall-envconf";
			// The conformance suite is about the harness contract, not about isolation: a
			// supervisor that answers the protocol is enough, and the real one is the end-to-end
			// tests' business.
			const fake = await fakeSupervisor(state);
			const socket = fake.socket;
			await writeFile(
				join(state, `${name}.json`),
				JSON.stringify({
					engine: "bwrap",
					workspace,
					image: "debian:stable-slim",
					network: { mode: "off" },
					socket,
					home: join(state, "home"),
					hidden: join(state, "hidden"),
				}),
			);
			await mkdir(join(state, "home"), { recursive: true });
			await mkdir(join(state, "hidden"), { recursive: true });
			process.env.ACORNFALL_BWRAP_STATE = state;
			const sandbox = await openBwrap(name, workspace);
			try {
				await use(new SandboxExecutionEnv(sandbox, workspace));
			} finally {
				await fake.close();
				delete process.env.ACORNFALL_BWRAP_STATE;
				await rm(workspace, { recursive: true, force: true });
				await rm(state, { recursive: true, force: true });
			}
		},
		{ shell: ["sh", "-c"] },
	);
}
