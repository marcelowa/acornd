/**
 * What a prompt becomes before it reaches the model.
 *
 * The gateway's command carries `text` with omitempty, so a message that is only files arrives
 * without one at all — a screenshot on its own is still a message. Reading it as undefined used to
 * fail the whole send with a TypeError, which is what a person saw as "undefined is not an object"
 * after dropping a file in the composer and typing nothing.
 */
import { describe, expect, test } from "bun:test";
import { messageContent } from "../src/attachments.ts";
import type { AttachmentMeta } from "../src/attachments.ts";

function attachment(name: string, mime: string): { meta: AttachmentMeta; bytes: Uint8Array } {
	return {
		meta: { id: "a1", name, mime, size: 12 } as AttachmentMeta,
		bytes: new Uint8Array([1, 2, 3]),
	};
}

describe("messageContent", () => {
	test("words alone are the content, unchanged", () => {
		expect(messageContent("hello", [])).toBe("hello");
	});

	test("no words and no files is empty, not undefined", () => {
		expect(messageContent(undefined, [])).toBe("");
	});

	test("files with no words still make a message", () => {
		const content = messageContent(undefined, [attachment("shot.png", "image/png")]);
		expect(Array.isArray(content)).toBe(true);
		if (!Array.isArray(content)) return;
		const text = content.find((block) => block.type === "text");
		expect(text).toBeDefined();
		if (text?.type !== "text") return;
		// The header is empty, so the block starts with the file's own line rather than a blank one.
		expect(text.text.startsWith("Attached: shot.png")).toBe(true);
		expect(text.text).not.toContain("undefined");
	});

	test("words and files keep the words first", () => {
		const content = messageContent("look at this", [attachment("shot.png", "image/png")]);
		if (!Array.isArray(content)) throw new Error("expected blocks");
		const text = content.find((block) => block.type === "text");
		if (text?.type !== "text") throw new Error("expected a text block");
		expect(text.text.startsWith("look at this")).toBe(true);
	});

	test("an image rides as an image, text inline", () => {
		const blocks = messageContent(undefined, [
			attachment("shot.png", "image/png"),
			attachment("notes.txt", "text/plain"),
		]);
		if (!Array.isArray(blocks)) throw new Error("expected blocks");
		// A raster the model can see rides as an image; text that fits rides inline, marked with its
		// name so the transcript says what came from a file.
		expect(blocks.some((block) => block.type === "image")).toBe(true);
		expect(
			blocks.some(
				(block) => block.type === "text" && block.text.startsWith("--- notes.txt ---"),
			),
		).toBe(true);
	});
});
