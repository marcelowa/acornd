import { useEffect, useState } from "react";

/**
 * The current time, re-read on an interval.
 *
 * The rail prints how long ago a conversation moved, and dims a run's dot when it stops moving.
 * Both are decisions about "now", and nothing re-renders a page that is sitting still: without a
 * tick, a still console could hold a pulsing dot beside an age that stopped moving, which is the
 * one thing the row was added to prevent.
 */
export function useNow(intervalMs = 60_000): Date {
	const [now, setNow] = useState(() => new Date());
	useEffect(() => {
		const timer = window.setInterval(() => setNow(new Date()), intervalMs);
		return () => window.clearInterval(timer);
	}, [intervalMs]);
	return now;
}

/**
 * How long a run may go quiet before the interface stops pulsing its dot. The gateway reports the
 * worker's record as it is: a quiet run is still a run, and Stop must stay offered while it is
 * live. This window is only about the dot — a shorter one would dim a genuinely slow tool call,
 * and a lit dot on a run nobody has heard from promises life the run cannot show.
 */
export const STALE_AFTER_MINUTES = 30;

export type RunState = "running" | "stale" | "idle";

/**
 * What a run flag and its last activity add up to, in one place: the rail row and the top bar
 * both show a dot for the same flag, and an age rule that lived in one of them would let the
 * other keep promising a run nobody has heard from.
 */
export function runState(running: boolean, updatedAt: Date | undefined, now: Date): RunState {
	if (!running) return "idle";
	if (updatedAt === undefined) return "running";
	return now.getTime() - updatedAt.getTime() > STALE_AFTER_MINUTES * 60_000 ? "stale" : "running";
}
