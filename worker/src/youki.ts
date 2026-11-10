import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { type BwrapConfig, SupervisorClient } from "./bwrap.ts";
import type { SandboxClient } from "./sandbox.ts";

/**
 * The youki engine's client: the same supervisor socket bwrap's client speaks, over the record
 * control wrote with engine "youki". The sandbox's first process is the supervisor, so a command
 * is a request on the socket and nothing here needs the runtime's own `exec` — which is broken
 * rootless (ADR 0022). The runtime, the mounts and the policy are Go's; this file is the engine's
 * name and the record reader, and the client itself is the one bwrap.ts already implements.
 */
export function engineName(): string {
	return "youki";
}

export async function openSandbox(
	name: string,
	workspace: string,
	wake?: () => Promise<void>,
): Promise<SandboxClient> {
	const state = process.env.ACORND_YOUKI_STATE;
	if (state === undefined || state === "") {
		throw new Error(
			"the youki engine's state folder is not set: control starts a workspace with ACORND_YOUKI_STATE",
		);
	}
	const path = join(state, `${name}.json`);
	const config = JSON.parse(await readFile(path, "utf8")) as BwrapConfig;
	if (config.engine !== "youki" || config.workspace !== workspace) {
		// A name that resolves to another workspace's config would run commands in the wrong
		// folder, which is the one mistake worth refusing loudly.
		throw new Error(`${path} is not the config for ${workspace}`);
	}
	return new SupervisorClient(config, "youki", wake);
}
