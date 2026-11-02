import type { Meta, StoryObj } from "@storybook/react-vite";
import { SessionSectionView } from "./session-section";

const meta = {
	title: "Settings/Session",
	component: SessionSectionView,
	args: { onSignOut: () => undefined },
} satisfies Meta<typeof SessionSectionView>;

export default meta;
type Story = StoryObj<typeof meta>;

/** Signed in with a cookie minted by the claim: the address is the one the book records. */
export const SignedInAsTheOwner: Story = {
	args: {
		session: {
			setup: false,
			owner: "marcelo@example.com",
			email: "marcelo@example.com",
			signedIn: true,
		},
	},
};

/** Signed in with the install token: the cookie names no address, so the line says the token did it. */
export const SignedInWithTheToken: Story = {
	args: {
		session: {
			setup: false,
			owner: "marcelo@example.com",
			email: "",
			signedIn: true,
		},
	},
};

/** A sign-out in flight: the one button says so. */
export const SigningOut: Story = {
	args: {
		session: {
			setup: false,
			owner: "marcelo@example.com",
			email: "marcelo@example.com",
			signedIn: true,
		},
		busy: true,
	},
};

/** A sign-out the gateway refused: its sentence, where the button was. */
export const Refused: Story = {
	args: {
		session: {
			setup: false,
			owner: "marcelo@example.com",
			email: "marcelo@example.com",
			signedIn: true,
		},
		failure: "the session could not be cleared: read the install's log",
	},
};

/** The page before the answer: a sentence rather than a blank. */
export const Unanswered: Story = {};

/** The read itself failed: the sentence says so, and the line above does not claim to still be reading. */
export const Unreachable: Story = { args: { failure: "Failed to fetch" } };
