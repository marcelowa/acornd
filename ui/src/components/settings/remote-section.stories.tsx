import type { Meta, StoryObj } from "@storybook/react";
import { RemoteSection } from "./remote-section";

const meta: Meta<typeof RemoteSection> = {
	title: "Settings/Remote access",
	component: RemoteSection,
	args: {
		onApply: () => undefined,
		onPreview: () => undefined,
		onEnable: () => undefined,
		onRefresh: () => undefined,
		onCheck: () => undefined,
	},
};
export default meta;
type Story = StoryObj<typeof meta>;

/** A family install before anybody has made the door: two fields and a token, and nothing to break. */
export const NothingYet: Story = {};

/** After an apply: what a browser reaches, and what the press did, one line per piece. */
export const Made: Story = {
	args: {
		onForgetToken: () => undefined,
		status: {
			configured: true,
			tokenStored: true,
			hostname: "console.example.com",
			teamDomain: "acornd.cloudflareaccess.com",
			audience: "c52dbf9750d9619950bffb6e96106adba22bdd4f8957362c0505a14718873982",
			updated: "2026-10-30T21:47:20Z",
			steps: [
				{ name: "tunnel", created: true, detail: "acornd" },
				{ name: "ingress", created: true, detail: "http://127.0.0.1:7777" },
				{ name: "dns", created: true, detail: "console.example.com" },
				{ name: "access group", created: false, detail: "2 addresses" },
			],
		},
	},
};

/** The door made, but the token was never kept: the group will not follow People until one is pasted. */
export const NotFollowingPeople: Story = {
	args: {
		status: {
			configured: true,
			tokenStored: false,
			hostname: "console.example.com",
			audience: "c52dbf9750d9619950bffb6e96106adba22bdd4f8957362c0505a14718873982",
			updated: "2026-10-30T21:47:20Z",
		},
	},
};

/** A press that Cloudflare refused: the sentence the person can act on, next to the button that retries. */
export const Refused: Story = {
	args: {
		failure:
			"The zone token is missing Zone·DNS·Edit, so the record cannot be made. Add the permission in Cloudflare, then press again.",
	},
};

/** A press in flight: the button says so rather than the screen going quiet. */
export const Making: Story = { args: { busy: true } };

/** The door checked from outside, and guarded: the answer a person wants before inviting anybody. */
export const CheckedAndGuarded: Story = {
	args: {
		status: Made.args?.status,
		onCheck: () => undefined,
		check: {
			guarded: true,
			status: 302,
			login: "https://acornd.cloudflareaccess.com/cdn-cgi/access/login/console.example.com",
			keys: true,
			detail: "the door is up: an unauthenticated browser is sent to the login page, and the keys are reachable",
		},
	},
};

/** The finding that looks like success: the console answered directly, so nothing is in front of it. */
export const CheckedAndOpen: Story = {
	args: {
		status: Made.args?.status,
		onCheck: () => undefined,
		check: {
			guarded: false,
			status: 200,
			keys: false,
			detail: "the console answered directly, so nothing is in front of it: this is worse than a door that does not open",
		},
	},
};

/** The token checked: what it can see, before any of it is stored. One zone is already chosen. */
export const TokenChecked: Story = {
	args: {
		preview: {
			teamDomain: "acornd.cloudflareaccess.com",
			zones: [{ name: "example.com", id: "z1", accountId: "a1" }],
		},
	},
};

/** Two zones the token can see: the picker is the choice, and nothing has been made yet. */
export const TwoZones: Story = {
	args: {
		preview: {
			teamDomain: "acornd.cloudflareaccess.com",
			zones: [
				{ name: "example.com", id: "z1", accountId: "a1" },
				{ name: "acornd.app", id: "z2", accountId: "a1" },
			],
		},
	},
};

/** Cloudflare has not published the application yet: the screen says so and asks again while it waits. */
export const RestartRequired: Story = {
	args: {
		status: {
			configured: true,
			tokenStored: true,
			restartRequired: true,
			hostname: "console.example.com",
			teamDomain: "acornd.cloudflareaccess.com",
			audience: "c52dbf9750d9619950bffb6e96106adba22bdd4f8957362c0505a14718873982",
			updated: "2026-10-30T21:47:20Z",
			steps: [{ name: "tunnel", created: true }],
		},
		check: {
			guarded: true,
			status: 302,
			login: "https://acornd.cloudflareaccess.com/cdn-cgi/access/login/console.example.com?kid=another-application",
			keys: true,
			detail: "the door is up: an unauthenticated browser is sent to the login page",
		},
	},
};

/** Published: the screen opens the door itself, and says what will happen to this address. */
export const Opening: Story = {
	args: {
		...RestartRequired.args,
		check: {
			guarded: true,
			status: 302,
			login: "https://acornd.cloudflareaccess.com/cdn-cgi/access/login/console.example.com?kid=c52dbf9750d9619950bffb6e96106adba22bdd4f8957362c0505a14718873982",
			keys: true,
			detail: "the door is up: an unauthenticated browser is sent to the login page",
		},
	},
};

/** The restart is in flight: the process is replacing itself, and the address underneath is about to go. */
export const Restarting: Story = {
	args: { ...Opening.args, restarting: true },
};

/** It came back: this page will not answer again, so the link is the way on. */
export const Restarted: Story = {
	args: { ...Opening.args, restarted: true },
};

/** The door is open and the connector says so; the check from outside is the last confirmation. */
export const Live: Story = {
	args: {
		status: {
			configured: true,
			tokenStored: true,
			restartRequired: false,
			tunnel: "connected",
			hostname: "console.example.com",
			teamDomain: "acornd.cloudflareaccess.com",
			audience: "c52dbf9750d9619950bffb6e96106adba22bdd4f8957362c0505a14718873982",
			updated: "2026-10-30T21:47:20Z",
		},
	},
};

/** A connector that is not running: the line says why, which the public hostname cannot. */
export const TunnelDown: Story = {
	args: {
		status: {
			configured: true,
			tokenStored: true,
			restartRequired: false,
			tunnel: "not started",
			tunnelDetail:
				"this build has no pinned cloudflared for this platform: install it yourself and pass --cloudflared <path>",
			hostname: "console.example.com",
			teamDomain: "acornd.cloudflareaccess.com",
			audience: "c52dbf9750d9619950bffb6e96106adba22bdd4f8957362c0505a14718873982",
			updated: "2026-10-30T21:47:20Z",
		},
	},
};

/** A press the gateway refused, in its own words. */
export const EnableRefused: Story = {
	args: {
		...RestartRequired.args,
		failure:
			"this console was started with --host 0.0.0.0, and a restart keeps the arguments it already has: stop it and start it again without --host",
	},
};
