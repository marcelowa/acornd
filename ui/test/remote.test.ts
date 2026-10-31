import { afterEach, describe, expect, test } from "bun:test";
import { forgetRemoteToken } from "../src/app/client";

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
