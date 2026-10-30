import { afterEach, describe, expect, test } from "bun:test";
import { addMember, removeMember } from "../src/app/client";

/**
 * A room's members, at the client boundary. Two things are the contract and neither is visible in the
 * gateway's own tests: the address rides the path encoded, because an `@` and a `+` in a URL are not the
 * same characters to every proxy; and a refusal is the server's sentence, not a status code, because the
 * 400 for an address the book does not hold says to ask the admin (ADR 0034 §1).
 */

interface Call {
	url: string;
	init: RequestInit | undefined;
}

const originalFetch = globalThis.fetch;

function stub(answer: () => Response): Call[] {
	const calls: Call[] = [];
	globalThis.fetch = (async (url: string, init?: RequestInit) => {
		calls.push({ url, init });
		return answer();
	}) as unknown as typeof fetch;
	return calls;
}

/** What a request refused with, as the dialog would show it. */
async function refusalOf(request: Promise<unknown>): Promise<string> {
	return request.then(
		() => "it was accepted",
		(cause: unknown) => (cause instanceof Error ? cause.message : String(cause)),
	);
}

afterEach(() => {
	globalThis.fetch = originalFetch;
});

describe("a room's members", () => {
	test("adding somebody posts the address to the room's member route", async () => {
		const calls = stub(() => new Response(JSON.stringify({ workspaces: [] })));
		await addMember("team", "ana@example.com");
		expect(calls[0]?.url).toBe("/api/workspaces/team/members");
		expect(calls[0]?.init?.method).toBe("POST");
		expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({ email: "ana@example.com" });
	});

	test("removing somebody encodes the address in the path", async () => {
		const calls = stub(() => new Response(JSON.stringify({ workspaces: [] })));
		await removeMember("team", "ana+acornfall@example.com");
		expect(calls[0]?.url).toBe("/api/workspaces/team/members/ana%2Bacornfall%40example.com");
		expect(calls[0]?.init?.method).toBe("DELETE");
	});

	// The sentence is the whole value of the refusal: "400 Bad Request" tells nobody to ask the admin.
	test("a refusal arrives as the server's own sentence", async () => {
		stub(
			() =>
				new Response(
					"bo@example.com is not in People: ask the install's admin to add them first",
					{
						status: 400,
					},
				),
		);
		expect(await refusalOf(addMember("team", "bo@example.com"))).toContain(
			"ask the install's admin to add them first",
		);
	});

	test("the owner cannot be removed, and the API's refusal is what the person reads", async () => {
		stub(
			() =>
				new Response("a room's owner cannot be removed: forget the room instead", {
					status: 409,
				}),
		);
		expect(await refusalOf(removeMember("team", "me@example.com"))).toContain(
			"forget the room instead",
		);
	});
});
