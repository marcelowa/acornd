/**
 * When a conversation last moved.
 *
 * The rail groups by it, and control's busy rule needs it: a live run with no fresh activity is
 * treated as a run whose record outlived it. Most entries carry the message's own timestamp, but
 * a `pi.system` entry does not — and a resumed run writes one before its first assistant message.
 * A run that is live has activity now, even when the newest entry cannot date it.
 */
export function activityOf(
	entry: { readonly model?: readonly { readonly timestamp?: number }[] } | undefined,
	running: boolean,
	now: number = Date.now(),
): number | undefined {
	let newest: number | undefined;
	for (const message of entry?.model ?? []) {
		if (typeof message.timestamp !== "number") continue;
		if (newest === undefined || message.timestamp > newest) newest = message.timestamp;
	}
	if (newest !== undefined) return newest;
	// A live run with no dated entry: the system prompt a resumed run writes, before its first
	// assistant message. It is moving now, and saying otherwise drops the busy sign under it.
	return running ? now : undefined;
}
