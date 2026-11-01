import { useState } from "react";
import { Stack } from "@/components/layout/stack";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Text } from "@/components/typography/text";
import { Code } from "@/components/typography/code";
import { SessionFrame, SessionRefusal, SessionSource } from "./session-frame";

export interface ClaimScreenProps {
	/** The code `acornd start` put in the address bar, if the person followed the printed URL. */
	code?: string | undefined;
	/** The gateway's refusal, in its own words. */
	failure?: string | undefined;
	/** A claim is in flight: the one button says so. */
	busy?: boolean | undefined;
	onClaim: (input: { code: string; name: string; email: string }) => void;
}

/**
 * A fresh install, and the one form that closes it: the code proves the person holds the machine,
 * and the name and address become the book's admin (ADR 0036 §3). The code arrives prefilled from
 * the printed URL; the field stays editable, because a code read out of the log is typed by hand.
 */
export function ClaimScreen({ code = "", failure, busy = false, onClaim }: ClaimScreenProps) {
	const [name, setName] = useState("");
	const [email, setEmail] = useState("");
	const [setupCode, setSetupCode] = useState(code);
	const ready = name.trim() !== "" && email.trim() !== "" && setupCode.trim() !== "";
	return (
		<SessionFrame
			title="Claim this install"
			intro="Nobody owns this install yet. Add your name and email to become the owner."
		>
			<Stack
				as="form"
				gap="sm"
				onSubmit={(event) => {
					event.preventDefault();
					if (busy) return;
					onClaim({ name: name.trim(), email: email.trim(), code: setupCode.trim() });
				}}
			>
				<Stack as="label" gap="2xs">
					<Text size="2xs" tone="muted">
						Your name
					</Text>
					<Input
						name="name"
						value={name}
						autoComplete="name"
						onChange={(event) => setName(event.target.value)}
					/>
				</Stack>
				<Stack as="label" gap="2xs">
					<Text size="2xs" tone="muted">
						Your email
					</Text>
					<Input
						name="email"
						value={email}
						placeholder="you@example.com"
						autoComplete="email"
						onChange={(event) => setEmail(event.target.value)}
					/>
				</Stack>
				<Stack as="label" gap="2xs">
					<Text size="2xs" tone="muted">
						Setup code
					</Text>
					<Input
						name="code"
						value={setupCode}
						placeholder="ABCD-EFGH"
						autoComplete="off"
						onChange={(event) => setSetupCode(event.target.value)}
					/>
				</Stack>
				<SessionSource>
					<Code>acornd start</Code> prints the setup code; <Code>acornd setup-code</Code>{" "}
					prints it again.
				</SessionSource>
				<Button type="submit" disabled={busy || !ready}>
					{busy ? "Claiming…" : "Claim it"}
				</Button>
				{failure === undefined ? null : <SessionRefusal>{failure}</SessionRefusal>}
			</Stack>
		</SessionFrame>
	);
}
