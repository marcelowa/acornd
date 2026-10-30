import type { Meta, StoryObj } from "@storybook/react-vite";
import type { ProviderCatalogEntry } from "@/app/client";
import type { PersonSummary } from "@/app/view-models";
import { PeopleSection } from "./people-section";

const meta = {
	title: "Settings/People",
	component: PeopleSection,
	parameters: { layout: "centered" },
	decorators: [
		(Story) => (
			<div className="w-full max-w-4xl rounded-md border border-border bg-background p-4">
				<Story />
			</div>
		),
	],
} satisfies Meta<typeof PeopleSection>;

export default meta;
type Story = StoryObj<typeof meta>;

const noop = (): void => undefined;

/** Every provider and model this install has: what one person's list is narrowed from. */
const catalog: ProviderCatalogEntry[] = [
	{
		id: "opencode-go",
		name: "OpenCode Go",
		models: [
			{ id: "deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash" },
			{ id: "minimax-m3", name: "MiniMax M3" },
		],
	},
	{
		id: "commandcode",
		name: "CommandCode",
		models: [{ id: "cc-large", name: "CommandCode Large" }],
	},
];

/** A book with one of everything: the admin, somebody who signed in, somebody who has not, and a revoked record. */
const people: PersonSummary[] = [
	{
		email: "me@example.com",
		name: "Marcelo",
		role: "admin",
		state: "active",
		models: [],
		canCreate: true,
		created: "2026-09-01",
		lastSeen: "2026-10-31",
	},
	{
		email: "ana@example.com",
		name: "Ana Silva",
		role: "member",
		state: "active",
		models: ["opencode-go/deepseek-v4.1-flash"],
		canCreate: true,
		created: "2026-10-02",
		lastSeen: "2026-10-30",
	},
	{
		email: "bo@example.com",
		role: "member",
		state: "invited",
		models: [],
		canCreate: true,
		created: "2026-10-28",
	},
	{
		email: "cy@example.com",
		name: "Cy",
		role: "member",
		state: "revoked",
		models: [],
		canCreate: false,
		created: "2026-09-14",
		lastSeen: "2026-10-01",
	},
];

/**
 * The book: who may use the install, what they may run, and whether they may lead a room of their own. The
 * admin's own row has no controls — they run every model by owning the install, and the book refuses to
 * revoke the one address that could add anybody back.
 */
export const Default: Story = {
	args: {
		people,
		catalog,
		onAdd: noop,
		onRevoke: noop,
		onRestore: noop,
		onSetCanCreate: noop,
		onSetModels: noop,
	},
};

/**
 * An install with no book, and one whose book names nobody: both have no admin yet, and the command line is
 * where one is claimed. The command is the page's, in the gateway's own words.
 */
export const Empty: Story = {
	args: { ...Default.args, people: [] },
};

/** The first read in flight: the page says so rather than showing an empty book. */
export const Loading: Story = {
	args: { ...Default.args, people: undefined, loading: true },
};

/** A write the book refused, in its own words: the address is already there, so the row it names is above. */
export const Refused: Story = {
	args: { ...Default.args, failure: "that address is already in the list: ana@example.com" },
};

/** A book with one record: the admin alone, before anybody was named. */
export const OnlyTheAdmin: Story = {
	args: { ...Default.args, people: people.slice(0, 1) },
};

/** A phone: the rows wrap and the two fields print at 16px, so iOS does not zoom them. */
export const Phone: Story = {
	args: { ...Default.args },
	globals: { viewport: { value: "phone" } },
};
