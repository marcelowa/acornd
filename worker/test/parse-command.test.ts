/**
 * The boundary between a line of stdin and a command.
 *
 * `JSON.parse` answers `unknown`, so this is where a value that is not a command has to stop. The
 * cases below are the ones a gateway would never send and a browser, a broken pipe, or a truncated
 * write still can: a bare `null`, an array, a number, JSON cut in half, and a type that is not a
 * string. A throw here used to leave every handler, and every conversation in the process with it.
 */
import { describe, expect, test } from "bun:test";
import { parseCommand } from "../src/protocol.ts";

/** Deterministic, so a failure is reproducible from the seed alone. */
function random(seed: number): () => number {
	let state = seed >>> 0;
	return () => {
		state = (state + 0x6d2b79f5) >>> 0;
		let t = state;
		t = Math.imul(t ^ (t >>> 15), t | 1);
		t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
}

describe("parseCommand", () => {
	test("a command with an envelope is accepted as it arrived", () => {
		const line = JSON.stringify({ id: "7", type: "prompt", conversationId: "1", text: "hi" });
		const parsed = parseCommand(line);
		expect(parsed.ok).toBe(true);
		if (parsed.ok) expect(parsed.command).toMatchObject({ id: "7", type: "prompt" });
	});

	test("what is not a command is refused, and the reason says what was wrong", () => {
		const refused: [string, string][] = [
			["", "not JSON: "],
			["{", "not JSON: "],
			["null", "not an object"],
			["[]", "not an object"],
			["1", "not an object"],
			["true", "not an object"],
			[`"a string"`, "not an object"],
			[`{ "id": "1" }`, "no type"],
			[`{ "id": "1", "type": 1 }`, "no type"],
			[`{ "id": "1", "type": "" }`, "no type"],
			[`{ "type": "describe" }`, "no id"],
			[`{ "id": 7, "type": "describe" }`, "no id"],
		];
		for (const [line, reason] of refused) {
			const parsed = parseCommand(line);
			expect(parsed.ok).toBe(false);
			if (!parsed.ok) expect(parsed.reason).toStartWith(reason);
		}
	});

	test("no string can make it throw, and every accepted value has an envelope", () => {
		// Not a fuzzer: a fixed seed and a million-to-one alphabet is what a seed corpus is for,
		// and the run below is short enough to sit in the fast tier.
		const next = random(0x5eed);
		const alphabet = [
			"{",
			"}",
			"[",
			"]",
			'"',
			":",
			",",
			"null",
			"1",
			"type",
			"id",
			"\\u0000",
			"\n",
			"💥",
		];
		for (let index = 0; index < 2000; index++) {
			let line = "";
			const length = Math.floor(next() * 24);
			for (let part = 0; part < length; part++) {
				line += alphabet[Math.floor(next() * alphabet.length)] ?? "";
			}
			let parsed: ReturnType<typeof parseCommand>;
			expect(() => {
				parsed = parseCommand(line);
			}).not.toThrow();
			if (parsed! === undefined) throw new Error("parseCommand returned nothing");
			if (parsed!.ok) {
				expect(typeof parsed!.command.type).toBe("string");
				expect(typeof parsed!.command.id).toBe("string");
			} else {
				expect(parsed!.reason.length).toBeGreaterThan(0);
			}
		}
	});
});
