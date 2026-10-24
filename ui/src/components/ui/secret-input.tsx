import * as React from "react";
import { useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Eye, EyeOff } from "lucide-react";
import { cn } from "cn";
import { Input } from "./input";

type InputSize = "sm" | "default" | "lg";

export interface SecretInputProps extends Omit<React.ComponentProps<"input">, "type"> {
	/** The field's own class, merged after the room reserved for the toggle. */
	className?: string | undefined;
	inputSize?: InputSize | undefined;
}

/**
 * Whether the browser can mask a plain text field's glyphs. The property is non-standard and
 * prefixed, so it is asked for rather than assumed — Firefox, Chromium and WebKit all answer yes
 * today, and the field carries a mask of its own for anything that answers no.
 */
export function browserMasksText(): boolean {
	if (typeof CSS === "undefined" || typeof CSS.supports !== "function") return false;
	return CSS.supports("-webkit-text-security", "disc");
}

/**
 * How the glyphs are hidden. Where the browser can mask a text field natively it does, and the caret
 * stays where the person put it; where it cannot, the text is transparent and the caret is painted
 * in the foreground colour, so the field is still usable while a drawn bullet stands in for each
 * character.
 */
export function maskStyle(masked: boolean, nativeMask: boolean): React.CSSProperties | undefined {
	if (!masked) return undefined;
	if (nativeMask) return { WebkitTextSecurity: "disc" } as React.CSSProperties;
	return { color: "transparent", caretColor: "var(--foreground)" };
}

/** The same decision, as a value a test can compare without React in the way. */
export function maskDecision(masked: boolean, nativeMask: boolean): "shown" | "native" | "drawn" {
	if (!masked) return "shown";
	return nativeMask ? "native" : "drawn";
}

/**
 * A secret value field. It is masked, and it is **not a password field**: managers classify by
 * `type="password"`, and no combination of `data-bwignore`, `autocomplete="new-password"` and an
 * absent `<form>` stopped Bitwarden offering to save a tool secret as a login. A field that is not
 * a password field is not a login to save.
 *
 * The masking is therefore ours. Where the browser can hide a text field's glyphs
 * (`-webkit-text-security`) it does; where it cannot, the text is transparent and a bullet for each
 * character is drawn over it, which keeps the caret, the selection and pasting working because the
 * input is a real one.
 *
 * The value is never read, trimmed, or reformatted here. It is controlled by its caller, which is
 * what lets the mask know how long it is.
 */
export function SecretInput({ className, inputSize, value, ...props }: SecretInputProps) {
	const [revealed, setRevealed] = useState(false);
	const input = useRef<HTMLInputElement>(null);
	const selection = useRef<{ start: number | null; end: number | null } | undefined>(undefined);
	const id = useId();
	// Asked once per mount: the answer depends on the browser, not on anything that changes.
	const nativeMask = useMemo(browserMasksText, []);
	const length = String(value ?? "").length;

	const toggle = (): void => {
		const field = input.current;
		if (field !== null) {
			selection.current = { start: field.selectionStart, end: field.selectionEnd };
		}
		setRevealed((current) => !current);
	};

	useLayoutEffect(() => {
		const field = input.current;
		const saved = selection.current;
		if (field === null || saved === undefined) return;
		field.focus();
		field.setSelectionRange(saved.start, saved.end);
	}, [revealed]);

	const masked = !revealed;

	return (
		<div className="relative" data-slot="secret-input">
			<Input
				{...props}
				ref={input}
				id={id}
				value={value}
				inputSize={inputSize}
				type="text"
				autoComplete="off"
				autoCapitalize="off"
				autoCorrect="off"
				spellCheck={false}
				data-1p-ignore="true"
				data-bwignore="true"
				data-lpignore="true"
				data-form-type="other"
				aria-label={props["aria-label"] ?? "Secret value"}
				// Inline, not a class: the mask is a property the design system has no token for, and
				// a property name with a vendor prefix is not something a class list can carry
				// reliably. Neither of these changes the value.
				style={maskStyle(masked, nativeMask)}
				className={cn("pr-10 max-sm:pr-12", className)}
			/>
			{masked && !nativeMask && length > 0 ? (
				<span
					aria-hidden="true"
					data-slot="secret-mask"
					className="pointer-events-none absolute inset-y-0 left-3 flex items-center overflow-hidden pr-10 text-ellipsis whitespace-nowrap select-none"
				>
					{"•".repeat(length)}
				</span>
			) : null}
			<button
				type="button"
				aria-pressed={revealed}
				aria-label={revealed ? "Hide secret" : "Show secret"}
				aria-controls={id}
				onClick={toggle}
				className="absolute top-1/2 right-0 flex h-9 w-9 -translate-y-1/2 items-center justify-center rounded-r-md text-muted-foreground transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none max-sm:h-11 max-sm:w-11"
			>
				{revealed ? (
					<EyeOff aria-hidden="true" className="size-4" />
				) : (
					<Eye aria-hidden="true" className="size-4" />
				)}
			</button>
		</div>
	);
}
