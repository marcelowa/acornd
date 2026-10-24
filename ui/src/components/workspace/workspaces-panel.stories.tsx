import type { Meta, StoryObj } from "@storybook/react-vite";
import type { WorkspaceSummary } from "@/app/view-models";
import { WorkspacesPanel } from "./workspaces-panel";

const meta = {
	title: "Layout/WorkspacesPanel",
	component: WorkspacesPanel,
	parameters: { layout: "centered" },
	// The rail's shape: the panel is a level of it, not a page, so it is reviewed at the width
	// and on the surface it ships in.
	decorators: [
		(Story) => (
			<div className="flex h-96 w-80 flex-col overflow-hidden border border-border bg-rail p-2">
				<Story />
			</div>
		),
	],
} satisfies Meta<typeof WorkspacesPanel>;

export default meta;
type Story = StoryObj<typeof meta>;

const noop = (): void => undefined;

const workspaces: WorkspaceSummary[] = [
	{
		name: "general",
		title: "acornfall",
		path: "/home/marcelo/workspace/acornfall",
		general: true,
		running: true,
		active: true,
		engineLabel: "microVM",
	},
	{
		name: "demo",
		title: "Demo",
		path: "/home/marcelo/workspaces/demo",
		general: false,
		running: false,
		active: false,
		engine: "bwrap",
		engineLabel: "bwrap",
	},
	{
		name: "something",
		title: "Something long enough to need the whole row and then some more",
		path: "/home/marcelo/workspaces/something",
		general: false,
		running: false,
		active: false,
	},
];

/** The engines the create form offers: the default first, one that downloads on first use, and
 * one this machine cannot run. */
const engines = [
	{ name: "microsandbox", isDefault: true, usable: true, downloads: true, label: "microVM" },
	{ name: "bwrap", isDefault: false, usable: true, downloads: false, label: "bwrap" },
	{
		name: "host",
		isDefault: false,
		usable: false,
		downloads: false,
		label: "host",
		problem: "not isolated: commands run on the machine itself",
	},
];

export const Default: Story = {
	args: {
		workspaces,
		engines,
		onSelect: noop,
		onCreate: noop,
		onRename: noop,
		onForget: noop,
		onUpdateSandbox: noop,
	},
};

/** Nothing but the general workspace: the only row that cannot be forgotten. */
export const GeneralOnly: Story = {
	args: { ...Default.args, workspaces: workspaces.slice(0, 1) },
};

/**
 * A phone: the drawer is the width of the screen, so the rows get their room back and the create
 * form stops wrapping. It is the same panel at another width, not a second one.
 */
export const Phone: Story = {
	args: Default.args,
	globals: { viewport: { value: "phone" } },
	decorators: [
		(Story) => (
			<div className="flex h-96 w-full flex-col overflow-hidden bg-rail p-2">
				<Story />
			</div>
		),
	],
};
