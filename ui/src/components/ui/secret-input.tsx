import * as React from "react";
import { useId, useLayoutEffect, useRef, useState } from "react";
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
 * A secret value field. It is a password field, but not a login: the password-manager
 * opt-outs stop the "save this login?" prompt, and the reveal button lets the person check
 * what they pasted. It is never inside a submitting form.
 *
 * The type switch to `text` drops the caret and selection in every browser, so they are
 * captured before the switch and put back in a layout effect after it. The value is never
 * read, trimmed, or reformatted here.
 */
export function SecretInput({ className, inputSize, ...props }: SecretInputProps) {
	const [revealed, setRevealed] = useState(false);
	const input = useRef<HTMLInputElement>(null);
	const selection = useRef<{ start: number | null; end: number | null } | undefined>(undefined);
	const id = useId();

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

	return (
		<div className="relative" data-slot="secret-input">
			<Input
				{...props}
				ref={input}
				id={id}
				inputSize={inputSize}
				type={revealed ? "text" : "password"}
				autoComplete="new-password"
				autoCapitalize="off"
				autoCorrect="off"
				spellCheck={false}
				data-1p-ignore="true"
				data-bwignore="true"
				data-lpignore="true"
				data-form-type="other"
				className={cn("pr-10 max-sm:pr-12", className)}
			/>
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
