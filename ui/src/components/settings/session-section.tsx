import { useCallback, useEffect, useState } from "react";
import { fetchSession, signOut } from "@/app/client";
import type { WireSession } from "@/app/wire";
import { Stack } from "@/components/layout/stack";
import { Button } from "@/components/ui/button";
import { Text } from "@/components/typography/text";
import { SettingsGroup } from "./settings-group";

export interface SessionSectionViewProps {
	/** The session as the install answers it. Absent while the page is reading it. */
	session?: WireSession | undefined;
	/** The gateway's refusal, in its own words. */
	failure?: string | undefined;
	/** A sign-out is in flight: the button says so. */
	busy?: boolean | undefined;
	onSignOut: () => void;
}

/** Who this browser is, in one line: the address the cookie names, or the token it was made from. */
function whoLine(session: WireSession): string {
	if (!session.signedIn) return "This browser is not signed in.";
	if (session.email !== "") return `Signed in as ${session.email}.`;
	if (session.owner !== "")
		return `Signed in with the install token. This install belongs to ${session.owner}.`;
	return "Signed in with the install token.";
}

/**
 * The browser's own session, at the bottom of the settings: who it is, and the one press that ends
 * it. Signing out clears the cookie and nothing else — the install token and the setup code are
 * untouched, and the same form signs the browser back in (ADR 0036 §4).
 */
export function SessionSectionView({
	session,
	failure,
	busy = false,
	onSignOut,
}: SessionSectionViewProps) {
	// A read that failed says so below the button; the line above does not keep claiming it is
	// still checking, and a read still in flight says what it is doing.
	const line =
		session === undefined
			? failure === undefined
				? "Checking who this browser is…"
				: undefined
			: whoLine(session);
	return (
		<SettingsGroup
			slot="settings-session"
			title="This browser"
			intro={
				line === undefined ? undefined : (
					<Text size="2xs" tone="muted">
						{line}
					</Text>
				)
			}
		>
			<Stack gap="sm">
				<Stack direction="horizontal" gap="sm" align="center">
					<Button
						type="button"
						size="sm"
						variant="outline"
						disabled={busy || session?.signedIn !== true}
						onClick={onSignOut}
					>
						{busy ? "Signing out…" : "Sign out"}
					</Button>
				</Stack>
				{failure === undefined ? null : (
					<Text size="2xs" className="text-destructive">
						{failure}
					</Text>
				)}
			</Stack>
		</SettingsGroup>
	);
}

/**
 * The section as the settings page uses it: it asks who this browser is when the page is opened,
 * and signing out replaces the page with `/`, where the gate draws the sign-in.
 */
export function SessionSection() {
	const [session, setSession] = useState<WireSession | undefined>(undefined);
	const [failure, setFailure] = useState<string | undefined>(undefined);
	const [busy, setBusy] = useState(false);

	useEffect(() => {
		let cancelled = false;
		void fetchSession().then(
			(next) => {
				if (!cancelled) setSession(next);
			},
			(cause: unknown) => {
				if (!cancelled) setFailure(messageOf(cause));
			},
		);
		return () => {
			cancelled = true;
		};
	}, []);

	const signOutNow = useCallback((): void => {
		setBusy(true);
		setFailure(undefined);
		void signOut().then(
			() => window.location.replace("/"),
			(cause: unknown) => {
				setBusy(false);
				setFailure(messageOf(cause));
			},
		);
	}, []);

	return (
		<SessionSectionView
			session={session}
			failure={failure}
			busy={busy}
			onSignOut={signOutNow}
		/>
	);
}

/** What a refusal said, as a page shows it: the gateway writes sentences, and a status code is not one. */
function messageOf(error: unknown): string {
	return error instanceof Error ? error.message : String(error);
}
