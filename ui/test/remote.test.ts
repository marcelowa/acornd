import { afterEach, describe, expect, test } from "bun:test";
import { enableRemote, forgetRemoteToken, previewRemote } from "../src/app/client";
import { completeHostname } from "../src/app/remote";

/**
 * The one call that removes the Cloudflare token: what a page can get wrong is the verb and the route, and a
 * GET here would leave the token on the machine while looking like it worked.
 */

interface Call {
	url: string;
	init: RequestInit | undefined;
}

const originalFetch = globalThis.fetch;

function stub(body: unknown): Call[] {
	const calls: Call[] = [];
	globalThis.fetch = (async (url: string, init?: RequestInit) => {
		calls.push({ url, init });
		return new Response(JSON.stringify(body));
	}) as unknown as typeof fetch;
	return calls;
}

afterEach(() => {
	globalThis.fetch = originalFetch;
});

describe("the door's token", () => {
	test("forgetting it is one POST to its own route, and the answer is the state", async () => {
		const calls = stub({ configured: true, tokenStored: false });
		const status = await forgetRemoteToken();
		expect(calls[0]?.url).toBe("/api/remote/forget-token");
		expect(calls[0]?.init?.method).toBe("POST");
		expect(status.tokenStored).toBe(false);
	});
});

describe("the hostname the door will answer on", () => {
	const table: [string, string, string][] = [
		["console", "acornd.app", "console.acornd.app"],
		["console.acornd.app", "acornd.app", "console.acornd.app"],
		["  Console  ", "acornd.app", "console.acornd.app"],
		["https://home.acornd.app/", "acornd.app", "home.acornd.app"],
		["console", "", "console"],
		["", "acornd.app", ""],
	];
	for (const [input, zone, want] of table) {
		test(`${JSON.stringify(input)} in ${zone} is ${want}`, () => {
			expect(completeHostname(input, zone)).toBe(want);
		});
	}
});

describe("a token, checked before it is stored", () => {
	test("one POST to the preview route, and the answer is what it can see", async () => {
		const calls = stub({
			zones: [{ name: "acornd.app", id: "z1", accountId: "a1" }],
			teamDomain: "acornd.cloudflareaccess.com",
		});
		const preview = await previewRemote("cf-token");
		expect(calls[0]?.url).toBe("/api/remote/preview");
		expect(calls[0]?.init?.method).toBe("POST");
		expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({ token: "cf-token" });
		expect(preview.zones[0]?.name).toBe("acornd.app");
		expect(preview.teamDomain).toBe("acornd.cloudflareaccess.com");
	});
});

describe("turning the door on", () => {
	test("one POST to the enable route, and the answer says whether it restarted", async () => {
		const calls = stub({ restarting: true });
		const answer = await enableRemote();
		expect(calls[0]?.url).toBe("/api/remote/enable");
		expect(calls[0]?.init?.method).toBe("POST");
		expect(answer.restarting).toBe(true);
	});
});
