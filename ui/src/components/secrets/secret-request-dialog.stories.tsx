import type { Meta, StoryObj } from "@storybook/react-vite";
import type { WireSecretEvent } from "@/app/wire";
import { SecretRequestDialog } from "./secret-request-dialog";

const meta = {
	title: "Conversation/SecretRequestDialog",
	component: SecretRequestDialog,
	parameters: { layout: "centered" },
} satisfies Meta<typeof SecretRequestDialog>;

export default meta;
type Story = StoryObj<typeof meta>;

const request: WireSecretEvent = {
	kind: "request",
	requestId: "s1",
	conversationId: "1",
	name: "GITHUB_TOKEN",
	description: "A read-only token for the release script",
	hosts: ["api.github.com", "github.com"],
};

const noop = async (): Promise<void> => undefined;

/**
 * The agent asked for a secret. The value is typed here, in a password field, and posts
 * straight to the gateway: it is not a message and does not enter the transcript.
 */
export const Open: Story = {
	args: {
		request,
		substitutes: true,
		onAnswer: noop,
		onCancel: noop,
	},
};

/** The host or a container engine holds the value in the sandbox; the copy says so. */
export const InTheSandbox: Story = {
	args: {
		...Open.args,
		substitutes: false,
	},
};

/** With nothing open the dialog renders nothing, which is the state between requests. */
export const NothingWaiting: Story = {
	args: {
		request: undefined,
		onAnswer: noop,
		onCancel: noop,
	},
};
