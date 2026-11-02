import type { Meta, StoryObj } from "@storybook/react-vite";
import type { WireSession } from "@/app/wire";
import { gateStateOf } from "@/app/gate";
import type { Gate } from "@/app/session-gate";
import { SessionGate } from "@/app/session-gate";
import { Page } from "@/components/layout/page";
import { Stack } from "@/components/layout/stack";
import { Text } from "@/components/typography/text";

const noop = (): void => undefined;

/** The gate with a literal document: a story never asks a gateway, and every state is a value. */
function gateOf(session: WireSession | undefined, over: Partial<Gate> = {}): Gate {
	return {
		state: gateStateOf(session),
		session,
		code: "",
		failure: undefined,
		busy: false,
		claim: noop,
		signIn: noop,
		...over,
	};
}

const meta = {
	title: "Screens/Session gate",
	component: SessionGate,
	parameters: { layout: "fullscreen" },
	args: {
		gate: gateOf(undefined),
		children: (
			<Page>
				<Stack fill justify="center" align="center">
					<Text size="sm" tone="muted">
						The console opens here.
					</Text>
				</Stack>
			</Page>
		),
	},
} satisfies Meta<typeof SessionGate>;

export default meta;
type Story = StoryObj<typeof meta>;

const fresh: WireSession = { setup: true, owner: "", email: "", signedIn: false };
const unclaimed: WireSession = { setup: false, owner: "", email: "", signedIn: false };
const owned: WireSession = {
	setup: false,
	owner: "marcelo@example.com",
	email: "",
	signedIn: false,
};
const signedIn: WireSession = {
	setup: false,
	owner: "marcelo@example.com",
	email: "marcelo@example.com",
	signedIn: true,
};

/** The first frame: the question is in flight, and nothing of the console exists yet. */
export const Asking: Story = {};

/** A fresh install, with the code `acornd start` put in the address bar already filled in. */
export const ClaimTheInstall: Story = {
	args: { gate: gateOf(fresh, { code: "K7QM-3XPD" }) },
};

/** A claim in flight: the one button says so rather than the form going quiet. */
export const Claiming: Story = {
	args: { gate: gateOf(fresh, { code: "K7QM-3XPD", busy: true }) },
};

/** The code was wrong or already used: the gateway's sentence, in the form, next to the field. */
export const ClaimRefused: Story = {
	args: {
		gate: gateOf(fresh, {
			code: "K7QM-3XPD",
			failure:
				"that is not this install's setup code: read it again with `acornd setup-code`",
		}),
	},
};

/** The session is gone: the install has an owner, and this browser has no cookie. */
export const SignIn: Story = { args: { gate: gateOf(owned) } };

/** An install whose owner is not recorded yet: the form asks for the token without naming anybody. */
export const SignInWithoutAnOwner: Story = { args: { gate: gateOf(unclaimed) } };

/** A sign-in in flight: the button says which one. */
export const SigningIn: Story = {
	args: { gate: gateOf(owned, { busy: true }) },
};

/** A token the install does not know: the gateway's sentence, where the field was. */
export const SignInRefused: Story = {
	args: {
		gate: gateOf(owned, {
			failure:
				"that is not this install's token or its setup code: read it again with `acornd token`",
		}),
	},
};

/** A session that ended while the console was open: the same form, and the sentence that says why. */
export const SessionEnded: Story = {
	args: { gate: gateOf(unclaimed, { failure: "Your session ended. Sign in again." }) },
};

/** The browser is somebody: the gate is not drawn at all, and the console is. */
export const BehindTheGate: Story = { args: { gate: gateOf(signedIn) } };
