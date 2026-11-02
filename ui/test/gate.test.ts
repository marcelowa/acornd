import { describe, expect, test } from "bun:test";
import { gateStateOf, setupCodeFrom } from "../src/app/gate";
import type { WireSession } from "../src/app/wire";

/**
 * The first render, as one decision: which screen a browser gets from the session document. It is
 * pure on purpose — the gate's four states are reviewable without a gateway, and a test can name
 * the case a running install would need a lost cookie and a fresh machine to reach.
 */

const session = (over: Partial<WireSession> = {}): WireSession => ({
	setup: false,
	owner: "",
	email: "",
	signedIn: false,
	...over,
});

describe("the gate's decision", () => {
	test("nothing asked yet is the busy frame", () => {
		expect(gateStateOf(undefined)).toBe("busy");
	});

	test("an install nobody owns asks to be claimed", () => {
		expect(gateStateOf(session({ setup: true }))).toBe("setup");
	});

	test("a browser with no cookie gets the sign-in", () => {
		expect(gateStateOf(session())).toBe("signin");
	});

	test("a browser with a cookie gets the app", () => {
		expect(
			gateStateOf(
				session({
					owner: "marcelo@example.com",
					email: "marcelo@example.com",
					signedIn: true,
				}),
			),
		).toBe("app");
	});

	test("a cookie minted from the install token names no address and is still signed in", () => {
		expect(
			gateStateOf(session({ owner: "marcelo@example.com", email: "", signedIn: true })),
		).toBe("app");
	});

	// The open window closes when the code is claimed, not when a browser signs in: a token holder
	// during setup still has to name the install's owner (ADR 0036 §3).
	test("setup outlives a browser that signed in with the token", () => {
		expect(gateStateOf(session({ setup: true, signedIn: true }))).toBe("setup");
	});
});

describe("the setup code in the address", () => {
	test("?code= fills the claim form", () => {
		expect(setupCodeFrom("?code=K7QM-3XPD")).toBe("K7QM-3XPD");
	});

	test("no code is an empty field, whatever else the query holds", () => {
		expect(setupCodeFrom("")).toBe("");
		expect(setupCodeFrom("?workspace=spikes")).toBe("");
	});

	test("the code is read next to the parameters a printed URL may carry", () => {
		expect(setupCodeFrom("?from=terminal&code=K7QM-3XPD")).toBe("K7QM-3XPD");
	});
});
