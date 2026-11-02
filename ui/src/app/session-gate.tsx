import { useCallback, useEffect, useState, type ReactNode } from "react";
import { fetchSession, claimInstall, onSessionLost, signIn } from "./client";
import { gateStateOf, setupCodeFrom, type GateState } from "./gate";
import { ClaimScreen } from "@/components/session/claim-screen";
import { SessionFrame } from "@/components/session/session-frame";
import { SignInScreen } from "@/components/session/sign-in-screen";
import type { WireSession } from "./wire";

/** The three fields the claim form sends. */
export interface ClaimInput {
	code: string;
	name: string;
	email: string;
}

/**
 * The first render, as one value: which screen the browser gets, the document behind the decision,
 * and the two presses that change it. It is `Gate` rather than a component's state so a story can
 * hand the gate a document and render every state without a gateway.
 */
export interface Gate {
	state: GateState;
	/** The document the gateway answered, once it did. */
	session: WireSession | undefined;
	/** The code `acornd start` put in the address bar, when the person followed the printed URL. */
	code: string;
	/** The last refusal, in the gateway's own words. */
	failure: string | undefined;
	/** A press is in flight: the form's one button says so. */
	busy: boolean;
	claim: (input: ClaimInput) => void;
	signIn: (secret: string) => void;
}

/**
 * The session, asked once before anything else is built: what the install is, who owns it, and
 * whether this browser is already somebody (ADR 0036 §3–§4). A claim or a sign-in that lands
 * replaces the page with `/`, which drops the setup code out of the address bar and lets the gate
 * ask again with the new cookie; a 401 from any later call comes back here as a signed-out
 * document, so the console is replaced by the form instead of a screen full of refusals.
 */
export function useSessionGate(): Gate {
	const [session, setSession] = useState<WireSession | undefined>(undefined);
	const [failure, setFailure] = useState<string | undefined>(undefined);
	const [busy, setBusy] = useState(false);
	const [code] = useState(() => setupCodeFrom(window.location.search));

	useEffect(() => {
		let cancelled = false;
		void fetchSession().then(
			(next) => {
				if (!cancelled) setSession(next);
			},
			(cause: unknown) => {
				if (cancelled) return;
				// A gateway that cannot answer has no session to show. The sign-in form is the only
				// screen left, and its sentence says what went wrong.
				setFailure(messageOf(cause));
				setSession({ setup: false, owner: "", email: "", signedIn: false });
			},
		);
		return () => {
			cancelled = true;
		};
	}, []);

	useEffect(
		() =>
			onSessionLost(() => {
				setBusy(false);
				setSession((current) => ({
					setup: false,
					owner: current?.owner ?? "",
					email: "",
					signedIn: false,
				}));
				setFailure("Your session ended. Sign in again.");
			}),
		[],
	);

	const claim = useCallback((input: ClaimInput): void => {
		setBusy(true);
		setFailure(undefined);
		void claimInstall(input).then(
			// The cookie is set, and `/` is the console without the code in the address bar.
			() => window.location.replace("/"),
			(cause: unknown) => {
				setBusy(false);
				setFailure(messageOf(cause));
			},
		);
	}, []);

	const signInNow = useCallback((secret: string): void => {
		setBusy(true);
		setFailure(undefined);
		void signIn(secret).then(
			() => window.location.replace("/"),
			(cause: unknown) => {
				setBusy(false);
				setFailure(messageOf(cause));
			},
		);
	}, []);

	return {
		state: gateStateOf(session),
		session,
		code,
		failure,
		busy,
		claim,
		signIn: signInNow,
	};
}

export interface SessionGateProps {
	gate: Gate;
	/** The console. It is not built until the gate says the browser is somebody. */
	children?: ReactNode | undefined;
}

/**
 * What the browser sees first: the claim form on an install nobody owns, the sign-in when the
 * session is gone, and nothing of the console until one of them is answered. The decision is
 * `gateStateOf`, so every state has a story and a test without a running gateway.
 */
export function SessionGate({ gate, children }: SessionGateProps): ReactNode {
	switch (gate.state) {
		case "busy":
			return <SessionFrame title="One moment" intro="Checking who this browser is." />;
		case "setup":
			return (
				<ClaimScreen
					code={gate.code}
					failure={gate.failure}
					busy={gate.busy}
					onClaim={gate.claim}
				/>
			);
		case "signin":
			return (
				<SignInScreen
					owner={gate.session?.owner ?? ""}
					failure={gate.failure}
					busy={gate.busy}
					onSignIn={gate.signIn}
				/>
			);
		case "app":
			return children;
	}
}

/** What a refusal said, as a page shows it: the gateway writes sentences, and a status code is not one. */
function messageOf(error: unknown): string {
	return error instanceof Error ? error.message : String(error);
}
