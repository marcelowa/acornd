import { useState } from "react";
import { Stack } from "@/components/layout/stack";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Text } from "@/components/typography/text";
import { Code } from "@/components/typography/code";
import { SessionFrame, SessionRefusal, SessionSource } from "./session-frame";

export interface SignInScreenProps {
	/** The address the book records as the admin, when there is one. */
	owner?: string | undefined;
	/** The gateway's refusal, in its own words. */
	failure?: string | undefined;
	/** A sign-in is in flight: the one button says so. */
	busy?: boolean | undefined;
	onSignIn: (secret: string) => void;
}

/**
 * The form a browser that lost its cookie comes back through: the install token always works, and
 * the setup code works while nobody owns the install, so a rotated or removed code is never a
 * lockout (ADR 0036 §3–§4).
 */
export function SignInScreen({ owner = "", failure, busy = false, onSignIn }: SignInScreenProps) {
	const [secret, setSecret] = useState("");
	const ready = secret.trim() !== "";
	return (
		<SessionFrame
			title="Sign in"
			intro={
				owner === ""
					? "This browser is not signed in yet."
					: `This install belongs to ${owner}. This browser is not signed in yet.`
			}
		>
			<Stack
				as="form"
				gap="sm"
				onSubmit={(event) => {
					event.preventDefault();
					if (busy) return;
					onSignIn(secret.trim());
				}}
			>
				<Stack as="label" gap="2xs">
					<Text size="2xs" tone="muted">
						Install token or setup code
					</Text>
					<Input
						name="secret"
						type="password"
						value={secret}
						autoComplete="current-password"
						onChange={(event) => setSecret(event.target.value)}
					/>
				</Stack>
				<SessionSource>
					<Code>acornd token</Code> prints the install token;{" "}
					<Code>acornd setup-code</Code> prints the setup code.
				</SessionSource>
				<Button type="submit" disabled={busy || !ready}>
					{busy ? "Signing in…" : "Sign in"}
				</Button>
				{failure === undefined ? null : <SessionRefusal>{failure}</SessionRefusal>}
			</Stack>
		</SessionFrame>
	);
}
