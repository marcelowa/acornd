import type { WireSession } from "./wire";

/**
 * What the console shows first, decided once from the one document every session route answers.
 *
 * The order is the install's before the browser's: `setup` is a state of the install — nobody owns
 * it — and it outlives a browser that signed in with the install token, because the open window
 * closes when the code is claimed and not before (ADR 0036 §3). A browser nobody owns the install
 * for, and that holds no cookie, gets the sign-in; anything else is the console.
 */
export type GateState = "busy" | "setup" | "signin" | "app";

/** The decision, with no React and no fetch in it: `undefined` is the frame before the answer. */
export function gateStateOf(session: WireSession | undefined): GateState {
	if (session === undefined) return "busy";
	if (session.setup) return "setup";
	if (!session.signedIn) return "signin";
	return "app";
}

/**
 * The setup code `acornd start` put in the address, so the claim form arrives filled in and the
 * person types only their name and address. It stays in the address bar until the claim succeeds;
 * the success replaces the page with `/`, which is what takes it out again.
 */
export function setupCodeFrom(search: string): string {
	return new URLSearchParams(search).get("code") ?? "";
}
