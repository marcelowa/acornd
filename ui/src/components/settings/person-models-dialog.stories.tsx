import type { Meta, StoryObj } from "@storybook/react-vite";
import type { ProviderCatalogEntry } from "@/app/client";
import type { PersonSummary } from "@/app/view-models";
import { PersonModelsDialog } from "./person-models-dialog";

const meta = {
	title: "Settings/PersonModels",
	component: PersonModelsDialog,
	parameters: { layout: "centered" },
} satisfies Meta<typeof PersonModelsDialog>;

export default meta;
type Story = StoryObj<typeof meta>;

const noop = (): void => undefined;

const catalog: ProviderCatalogEntry[] = [
	{
		id: "opencode-go",
		name: "OpenCode Go",
		models: [
			{ id: "deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash" },
			{ id: "minimax-m3", name: "MiniMax M3" },
			{ id: "opus-5", name: "Opus 5" },
		],
	},
	{
		id: "commandcode",
		name: "CommandCode",
		models: [{ id: "cc-large", name: "CommandCode Large" }],
	},
];

const person: PersonSummary = {
	email: "ana@example.com",
	name: "Ana Silva",
	role: "member",
	state: "active",
	models: [],
	canCreate: true,
	created: "2026-10-02",
	lastSeen: "2026-10-30",
};

/**
 * A person with no list: every model this install has, which is what the book means by no list. Saving with
 * everything ticked writes the record back to no list at all.
 */
export const EveryModel: Story = {
	args: { person, catalog, onSave: noop, onClose: noop },
};

/** A narrowed list: three of the four. What is not ticked is what they may not pin in a conversation. */
export const Narrowed: Story = {
	args: {
		person: {
			...person,
			models: ["opencode-go/deepseek-v4.1-flash", "commandcode/cc-large"],
		},
		catalog,
		onSave: noop,
		onClose: noop,
	},
};

/** A record with no name: the dialog reads the address, which is the identity the book keeps. */
export const NoName: Story = {
	args: { person: { ...person, name: undefined }, catalog, onSave: noop, onClose: noop },
};

/** An install with no provider connected: there is nothing to narrow, and the dialog says so. */
export const NoModels: Story = {
	args: { person, catalog: [], onSave: noop, onClose: noop },
};

/** A phone: the list scrolls inside the dialog and the buttons take the thumb ladder. */
export const Phone: Story = {
	args: { person, catalog, onSave: noop, onClose: noop },
	globals: { viewport: { value: "phone" } },
};
