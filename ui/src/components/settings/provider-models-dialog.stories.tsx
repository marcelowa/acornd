import type { Meta, StoryObj } from "@storybook/react";
import { ProviderModelsDialog } from "./provider-models-dialog";

const provider = {
	id: "openai",
	name: "OpenAI",
	models: [
		{ id: "gpt-5", name: "GPT-5" },
		{ id: "gpt-5-mini", name: "GPT-5 mini" },
		{ id: "gpt-5-pro", name: "GPT-5 pro" },
	],
};

const meta: Meta<typeof ProviderModelsDialog> = {
	title: "Settings/Provider models",
	component: ProviderModelsDialog,
	args: { provider, allowed: [], onSave: () => undefined, onClose: () => undefined },
};
export default meta;
type Story = StoryObj<typeof meta>;

/** A new install: nothing allowed until the admin ticks it. */
export const NothingYet: Story = {};

/** Narrowed to two: the state an owner leaves a key in when the third model is the expensive one. */
export const Narrowed: Story = { args: { allowed: ["gpt-5", "gpt-5-mini"] } };

/** Everything selected: the same as Select all, and now written as the three models by name. */
export const AllAllowed: Story = {
	args: { allowed: ["gpt-5", "gpt-5-mini", "gpt-5-pro"] },
};
