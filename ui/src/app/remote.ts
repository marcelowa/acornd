/**
 * The hostname the door will answer on, shown before the apply so a label is never a guess.
 *
 * This is the display, not the rule: the gateway completes and validates the name for real, and its sentence is
 * what a person reads when the name is wrong (ADR 0036 §6). Keeping the two in step is a test on this side and a
 * table on the other, which is cheaper than making the interface the authority.
 */
export function completeHostname(input: string, zone: string): string {
	const name = input
		.trim()
		.toLowerCase()
		.replace(/^[a-z][a-z0-9+.-]*:\/\//, "")
		.replace(/\/+$/, "");
	if (name === "") return "";
	const suffix = zone.trim().toLowerCase().replace(/\.$/, "");
	if (suffix === "") return name;
	if (!name.includes(".")) return `${name}.${suffix}`;
	return name;
}
