import { afterEach, describe, expect, test } from "bun:test";
import {
	claimInstall,
	fetchSession,
	fetchState,
	onSessionLost,
	signIn,
	signOut,
} from "../src/app/client";

/**
 * The session routes, and the one seam a lost session is noticed in. The gateway tests the
 * handlers; what this side can get wrong is the shape it sends, and a 401 that nobody hears — a
 * cookie that expired would otherwise leave a console whose every press says "unauthorized".
 */

interface Call {
	url: string;
	init: RequestInit | undefined;
}

const originalFetch = globalThis.fetch;

/** Answer every call with a fresh response: a Response's body can only be read once. */
function stub(answer: () => Response): Call[] {
	const calls: Call[] = [];
	globalThis.fetch = (async (url: string, init?: RequestInit) => {
		calls.push({ url, init });
		return answer();
	}) as unknown as typeof fetch;
	return calls;
}

function json(body: unknown): () => Response {
	return () => new Response(JSON.stringify(body));
}

/** The sentence a request failed with, so a test can read what a person would. */
async function refused(request: Promise<unknown>): Promise<string> {
	try {
		await request;
	} catch (cause) {
		return cause instanceof Error ? cause.message : String(cause);
	}
	throw new Error("the request was accepted, and this test expected a refusal");
}

afterEach(() => {
	globalThis.fetch = originalFetch;
});

describe("the claim form", () => {
	test("sends the code, the name and the address, and answers the person", async () => {
		const calls = stub(json({ email: "marcelo@example.com", role: "admin", state: "active" }));
		const person = await claimInstall({
			code: "K7QM-3XPD",
			name: "Marcelo",
			email: "marcelo@example.com",
		});
		expect(calls[0]?.url).toBe("/api/setup/claim");
		expect(calls[0]?.init?.method).toBe("POST");
		expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({
			code: "K7QM-3XPD",
			name: "Marcelo",
			email: "marcelo@example.com",
		});
		expect(person.role).toBe("admin");
	});

	test("a refused code keeps the gateway's sentence", async () => {
		stub(
			() =>
				new Response(
					"that is not this install's setup code: read it again with `acornd setup-code`\n",
					{ status: 401 },
				),
		);
		const message = await refused(
			claimInstall({ code: "AAAA-AAAA", name: "Ana", email: "ana@example.com" }),
		);
		expect(message).toBe(
			"that is not this install's setup code: read it again with `acornd setup-code`",
		);
	});
});

describe("the sign-in form", () => {
	test("sends the secret as the one field the route reads", async () => {
		const calls = stub(json({ setup: false, owner: "", email: "", signedIn: true }));
		await signIn("a-token");
		expect(calls[0]?.url).toBe("/api/session");
		expect(calls[0]?.init?.method).toBe("POST");
		expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({ secret: "a-token" });
	});

	test("a wrong secret is the form's own refusal, and its sentence is the gateway's", async () => {
		stub(
			() =>
				new Response(
					"that is not this install's token or its setup code: read it again with `acornd token`\n",
					{ status: 401 },
				),
		);
		const message = await refused(signIn("nope"));
		expect(message).toBe(
			"that is not this install's token or its setup code: read it again with `acornd token`",
		);
	});
});

describe("the session document", () => {
	test("reading it is one GET, and the answer is the document", async () => {
		const calls = stub(json({ setup: true, owner: "", email: "", signedIn: false }));
		const session = await fetchSession();
		expect(calls[0]?.url).toBe("/api/session");
		expect(calls[0]?.init?.method).toBe("GET");
		expect(session.setup).toBe(true);
		expect(session.signedIn).toBe(false);
	});

	test("signing out is a DELETE of this browser's cookie", async () => {
		const calls = stub(json({ setup: false, owner: "", email: "", signedIn: false }));
		await signOut();
		expect(calls[0]?.url).toBe("/api/session");
		expect(calls[0]?.init?.method).toBe("DELETE");
	});
});

describe("a session that ended", () => {
	test("any other API 401 wakes the gate once, and its sentence still reaches the screen", async () => {
		stub(() => new Response("the cookie expired: sign in again", { status: 401 }));
		let lost = 0;
		const stop = onSessionLost(() => {
			lost += 1;
		});
		try {
			const message = await refused(fetchState("acornd", "1"));
			expect(message).toBe("the cookie expired: sign in again");
			expect(lost).toBe(1);
		} finally {
			stop();
		}
	});

	test("a 403 is a refusal about this person, not a session that ended", async () => {
		stub(() => new Response("this is the install's own business", { status: 403 }));
		let lost = 0;
		const stop = onSessionLost(() => {
			lost += 1;
		});
		try {
			await refused(fetchState("acornd", "1"));
			expect(lost).toBe(0);
		} finally {
			stop();
		}
	});

	test("a wrong secret on the sign-in form is not a lost session", async () => {
		stub(() => new Response("that is not this install's token", { status: 401 }));
		let lost = 0;
		const stop = onSessionLost(() => {
			lost += 1;
		});
		try {
			await refused(signIn("nope"));
			expect(lost).toBe(0);
		} finally {
			stop();
		}
	});

	test("a wrong setup code is refused where it was typed", async () => {
		stub(() => new Response("that is not this install's setup code", { status: 401 }));
		let lost = 0;
		const stop = onSessionLost(() => {
			lost += 1;
		});
		try {
			await refused(
				claimInstall({ code: "AAAA-AAAA", name: "Ana", email: "ana@example.com" }),
			);
			expect(lost).toBe(0);
		} finally {
			stop();
		}
	});

	test("stopping the watch leaves nobody to wake", async () => {
		stub(() => new Response("the cookie expired", { status: 401 }));
		let lost = 0;
		const stop = onSessionLost(() => {
			lost += 1;
		});
		stop();
		await refused(fetchState("acornd", "1"));
		expect(lost).toBe(0);
	});
});
