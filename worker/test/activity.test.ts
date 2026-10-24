import { describe, expect, test } from "bun:test";
import { activityOf } from "../src/activity.ts";

/**
 * The number control's busy rule reads beside a run's flag.
 *
 * A resumed run writes a `pi.system` entry — the rendered prompt — before its first assistant
 * message, and a system message carries no timestamp. Reading that as "never moved" made a live,
 * resumed generation look idle: the state said `running: false` while the durable task was
 * `running`, and an update could walk over it.
 */

const at = 1793223324000;

interface TestMessage {
	role: string;
	content: string;
	timestamp?: number;
}

const system: { model: TestMessage[] } = { model: [{ role: "system", content: "" }] };
const assistant = { model: [{ role: "assistant", content: [], timestamp: at - 1000 }] };

describe("when a conversation last moved", () => {
	test("a message's own timestamp is the activity", () => {
		expect(activityOf(assistant, false, at)).toBe(at - 1000);
	});

	test("the newest message wins", () => {
		const entry = {
			model: [
				{ role: "user", timestamp: at - 5000 },
				{ role: "assistant", timestamp: at - 1000 },
			],
		};
		expect(activityOf(entry, false, at)).toBe(at - 1000);
	});

	test("a restarted run whose newest entry is a system entry moved now", () => {
		expect(activityOf(system, true, at)).toBe(at);
	});

	test("an idle conversation with no timestamps carries none", () => {
		expect(activityOf(system, false, at)).toBeUndefined();
		expect(activityOf(undefined, false, at)).toBeUndefined();
	});
});
