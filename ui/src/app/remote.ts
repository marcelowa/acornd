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

/**
 * Whether Cloudflare has published the Access application yet: the login redirect carries the application's
 * audience as its `kid`, so the hostname can be asked before the tunnel is up and answer honestly. A door that
 * exists but is not published yet sends a browser to an error page, which is the wait this asks about.
 */
export function doorPublished(login: string | undefined, audience: string | undefined): boolean {
	if (login === undefined || audience === undefined || audience === "") return false;
	const query = login.indexOf("?");
	if (query < 0) return false;
	const kid = new URLSearchParams(login.slice(query + 1)).get("kid");
	return kid === audience;
}

/** The connector's state as one line a person can read, or "" when this install has no door. */
export function tunnelLine(tunnel: string | undefined, detail: string | undefined): string {
	switch (tunnel) {
		case undefined:
			return "";
		case "connected":
			return "the tunnel is connected";
		case "connecting":
		case "starting":
			return "the tunnel is connecting…";
		case "restarting":
			return "the tunnel is restarting";
		case "stopped":
			return "the tunnel is stopped";
		case "not started":
			return detail === undefined || detail === ""
				? "the tunnel is not running"
				: `the tunnel is not running: ${detail}`;
		default:
			return "";
	}
}
