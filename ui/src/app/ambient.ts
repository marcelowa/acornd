import type { StatusPillState } from "@/components/ui/status-pill";

/**
 * What the composer's ambient row has to announce, or nothing.
 *
 * A dropped stream or a dead harness is the thing to know before choosing a model, and it stays
 * in the folded row — a fold that hid it would make a dead run look alive (step 06, J11).
 *
 * Nothing while everything is well, and nothing before the page has heard back: `workerUp` is
 * false until the first state arrives, and a pill drawn from that is a guess announced as a
 * fact — "the harness is down" on every load, for as long as the state request takes. It is a
 * decision about state rather than pixels, which is why it lives here rather than in the view:
 * a test can name it, and the browser suite stops racing the first frame.
 */
export interface AmbientInput {
	/** The first state request has been answered: before that, nothing here is known. */
	ready: boolean;
	workerUp: boolean;
	connection: "live" | "reconnecting";
}

export function ambientState(platform: AmbientInput): StatusPillState | undefined {
	if (!platform.ready) return undefined;
	if (!platform.workerUp) return "harness-down";
	return platform.connection === "reconnecting" ? "reconnecting" : undefined;
}
