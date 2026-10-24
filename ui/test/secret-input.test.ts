/**
 * How the secret field hides what it holds.
 *
 * The field is a text field rather than a password field, because password managers classify by
 * `type="password"` and no combination of opt-out attributes stopped one offering to save a tool
 * secret as a login. That means the masking is ours, and these are the two ways it happens: the
 * browser's own glyph masking where it has one, and a transparent field under a drawn bullet where
 * it does not.
 */
import { describe, expect, test } from "bun:test";
import { maskDecision, maskStyle } from "../src/components/ui/secret-input";

describe("maskStyle", () => {
	test("shows the value when it is revealed", () => {
		expect(maskStyle(false, true)).toBeUndefined();
		expect(maskStyle(false, false)).toBeUndefined();
	});

	test("asks the browser to mask the glyphs where it can", () => {
		// The caret stays where the person put it, so editing works.
		expect(maskDecision(true, true)).toBe("native");
	});

	test("draws the mask itself where the browser cannot", () => {
		expect(maskDecision(true, false)).toBe("drawn");
		expect(maskDecision(false, false)).toBe("shown");
	});

	test("hides the text and keeps the caret visible where it cannot", () => {
		const style = maskStyle(true, false);
		expect(style?.color).toBe("transparent");
		// A transparent caret would be a field nobody can see themselves typing in.
		expect(style?.caretColor).toBe("var(--foreground)");
	});
});
