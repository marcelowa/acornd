import { afterEach, describe, expect, test } from "bun:test";
import {
	addPerson,
	fetchPeople,
	restorePerson,
	revokePerson,
	setPersonCanCreate,
	setPersonModels,
} from "../src/app/client";

/**
 * The book's routes, at the client boundary. The gateway tests the handlers; what a page can get wrong is the
 * shape it sends and the address it encodes — a `+` in a URL is a space to a proxy, and an address with one in
 * it would quietly name somebody else.
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

describe("the book of people", () => {
	test("reading the book is one GET of the document", async () => {
		const calls = stub({
			people: [{ email: "me@example.com", role: "admin", state: "active" }],
		});
		const people = await fetchPeople();
		expect(calls[0]?.url).toBe("/api/people");
		expect(calls[0]?.init?.method).toBe("GET");
		expect(people).toHaveLength(1);
	});

	test("naming somebody posts the address and the name, and answers the whole book", async () => {
		const calls = stub({ people: [] });
		await addPerson({ email: "ana@example.com", name: "Ana Silva" });
		expect(calls[0]?.url).toBe("/api/people");
		expect(calls[0]?.init?.method).toBe("POST");
		expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({
			email: "ana@example.com",
			name: "Ana Silva",
		});
	});

	test("revoking and restoring are two posts to the record's own routes", async () => {
		const calls = stub({ people: [] });
		await revokePerson("ana+acornd@example.com");
		await restorePerson("ana+acornd@example.com");
		expect(calls.map((call) => call.url)).toEqual([
			"/api/people/ana%2Bacornd%40example.com/revoke",
			"/api/people/ana%2Bacornd%40example.com/restore",
		]);
		expect(calls.every((call) => call.init?.method === "POST")).toBe(true);
	});

	test("a person's models are a list, and an empty list is every model", async () => {
		const calls = stub({ people: [] });
		await setPersonModels("ana@example.com", ["opencode-go/deepseek-v4.1-flash"]);
		await setPersonModels("ana@example.com", []);
		expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({
			models: ["opencode-go/deepseek-v4.1-flash"],
		});
		expect(JSON.parse(String(calls[1]?.init?.body))).toEqual({ models: [] });
	});

	test("leading a room is the book's own field, spelled as the record spells it", async () => {
		const calls = stub({ people: [] });
		await setPersonCanCreate("ana@example.com", false);
		expect(calls[0]?.url).toBe("/api/people/ana%40example.com/can_create");
		expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({ can_create: false });
	});
});
