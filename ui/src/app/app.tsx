import { useKeyboardInset } from "@/lib/keyboard";
import { SessionGate, useSessionGate } from "./session-gate";
import { WorkspaceView } from "./workspace-view";
import { usePlatform } from "./use-platform";

/**
 * The app: the keyboard inset, the session gate, and the console behind it.
 *
 * The gate is the first render, and the console is its child: `usePlatform` does not run — no state
 * request, no stream, no workspace — until the session says this browser is somebody (ADR 0036 §3).
 */
export function App() {
	// One hook for the phone's keyboard: it writes `--keyboard-inset`, and the composer reserves
	// it. Nothing else in the app knows how a keyboard works. It is above the gate so the claim
	// form and the sign-in get the same behaviour.
	useKeyboardInset();
	const gate = useSessionGate();
	return (
		<SessionGate gate={gate}>
			<Console />
		</SessionGate>
	);
}

/** The console, mounted only once the gate has a session: this is the one place the platform starts. */
function Console() {
	return <WorkspaceView platform={usePlatform()} />;
}
