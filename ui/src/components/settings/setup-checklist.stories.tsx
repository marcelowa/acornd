import type { Meta, StoryObj } from "@storybook/react";
import { SetupChecklist } from "./setup-checklist";

const meta: Meta<typeof SetupChecklist> = {
	title: "Settings/Getting started",
	component: SetupChecklist,
	args: { onOpen: () => undefined, memberCount: 1 },
};
export default meta;
type Story = StoryObj<typeof meta>;

/** A fresh install: nothing done, and the four questions in the order they are worth asking. */
export const Fresh: Story = {
	args: {
		hasProvider: false,
		hasModel: false,
		remoteConfigured: false,
		remoteRestartRequired: false,
		memberCount: 1,
	},
};

/** Halfway: a key and a model, Cloudflare applied and a restart away, three people already invited. */
export const Halfway: Story = {
	args: {
		hasProvider: true,
		hasModel: true,
		remoteConfigured: true,
		remoteRestartRequired: true,
		memberCount: 3,
	},
};

/** Everything answered: the group draws nothing, which is the point of a checklist that can finish. */
export const Finished: Story = {
	args: {
		hasProvider: true,
		hasModel: true,
		remoteConfigured: true,
		remoteRestartRequired: false,
		memberCount: 4,
	},
};
