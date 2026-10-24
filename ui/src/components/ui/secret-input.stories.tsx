import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import { SecretInput } from "./secret-input";

const meta = {
	title: "UI/SecretInput",
	component: SecretInput,
	parameters: { layout: "centered" },
} satisfies Meta<typeof SecretInput>;

export default meta;
type Story = StoryObj<typeof meta>;

/**
 * The field is controlled, which is what lets the mask know how long the value is: one masked
 * character per character typed. It is a text field rather than a password field, so no password
 * manager offers to save a tool secret as a login.
 */
function Controlled({ initial }: { initial: string }) {
	const [value, setValue] = useState(initial);
	return (
		<SecretInput
			inputSize="sm"
			value={value}
			onChange={(event) => setValue(event.target.value)}
			placeholder="the value is never shown again"
			aria-label="Secret value"
		/>
	);
}

/** The value is masked by default; the eye reveals it without changing it. */
export const Hidden: Story = {
	args: {
		inputSize: "sm",
		value: "a value the user pasted",
		placeholder: "the value is never shown again",
		"aria-label": "Secret value",
	},
	render: () => <Controlled initial="a value the user pasted" />,
};

/** An empty field, which is where the mask has nothing to draw. */
export const Empty: Story = {
	args: { ...Hidden.args, value: "" },
	render: () => <Controlled initial="" />,
};

/** The same field after the reveal button was used. */
export const Revealed: Story = {
	args: { ...Hidden.args },
	render: () => <Controlled initial="a value the user pasted" />,
	play: async ({ canvasElement }) => {
		const button = canvasElement.querySelector<HTMLButtonElement>(
			"button[aria-label='Show secret']",
		);
		button?.click();
	},
};
