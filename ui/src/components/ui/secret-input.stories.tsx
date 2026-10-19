import type { Meta, StoryObj } from "@storybook/react-vite";
import { SecretInput } from "./secret-input";

const meta = {
	title: "UI/SecretInput",
	component: SecretInput,
	parameters: { layout: "centered" },
} satisfies Meta<typeof SecretInput>;

export default meta;
type Story = StoryObj<typeof meta>;

/** The value is hidden by default; the eye reveals it without changing it. */
export const Hidden: Story = {
	args: {
		inputSize: "sm",
		defaultValue: "a value the user pasted",
		placeholder: "the value is never shown again",
		"aria-label": "Secret value",
	},
};

/** The same field after the reveal button was used. */
export const Revealed: Story = {
	args: { ...Hidden.args },
	play: async ({ canvasElement }) => {
		const button = canvasElement.querySelector<HTMLButtonElement>(
			"button[aria-label='Show secret']",
		);
		button?.click();
	},
};
