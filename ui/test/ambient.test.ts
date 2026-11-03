import { describe, expect, test } from "bun:test";
import { ambientState } from "../src/app/ambient";

/**
 * What the composer announces about the connection, as one decision.
 *
 * The case this exists for is the first one: the page has not heard back yet, `workerUp` is
 * still false because nothing has said otherwise, and a pill drawn from that says the harness
 * is down on every load. It is a race the browser suite caught — a green run on a fast machine
 * and a red one on a busy runner — so it is a rule here rather than a hope there.
 */

const platform = (
	over: Partial<{ ready: boolean; workerUp: boolean; connection: "live" | "reconnecting" }> = {},
) => ({ ready: true, workerUp: true, connection: "live" as const, ...over });

describe("what the composer announces", () => {
	test("nothing before the page has heard back, even though workerUp is false", () => {
		expect(ambientState(platform({ ready: false, workerUp: false }))).toBeUndefined();
	});

	test("nothing while everything is well", () => {
		expect(ambientState(platform())).toBeUndefined();
	});

	test("the harness, once the state says so", () => {
		expect(ambientState(platform({ workerUp: false }))).toBe("harness-down");
	});

	test("a dropped stream, while the state still has a live harness", () => {
		expect(ambientState(platform({ connection: "reconnecting" }))).toBe("reconnecting");
	});

	test("a dead harness outranks a dropped stream: it is the cause, not the symptom", () => {
		expect(ambientState(platform({ workerUp: false, connection: "reconnecting" }))).toBe(
			"harness-down",
		);
	});
});
