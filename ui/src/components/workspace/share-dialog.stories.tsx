import type { Meta, StoryObj } from "@storybook/react-vite";
import type { ShareView } from "@/app/view-models";
import { ShareDialog } from "./share-dialog";

const meta = {
	title: "Workspace/ShareDialog",
	component: ShareDialog,
	parameters: { layout: "centered" },
} satisfies Meta<typeof ShareDialog>;

export default meta;
type Story = StoryObj<typeof meta>;

const noop = (): void => undefined;

/** A room its owner has shared with one other person. */
const owned: ShareView = {
	name: "team",
	title: "Team",
	members: [
		{ email: "me@example.com", role: "owner" },
		{ email: "ana@example.com", role: "guest" },
	],
	administer: true,
};

/**
 * The room's owner: who is in it, a way in, and a way out for a guest. The owner is never offered one — a
 * room is its owner's until it is forgotten, and the API refuses it (ADR 0034 §3).
 */
export const Owner: Story = {
	args: { room: owned, onAdd: noop, onRemove: noop, onClose: noop },
};

/** A room nobody has been added to yet: the owner, and the sentence that says so. */
export const OnlyTheOwner: Story = {
	args: {
		room: { ...owned, members: [{ email: "me@example.com", role: "owner" }] },
		onAdd: noop,
		onRemove: noop,
		onClose: noop,
	},
};

/**
 * A guest's view of the same room: the list, and no controls. Only the owner invites and removes, and a
 * guest may leave from the command line — the console does not offer it (ADR 0034 §3).
 */
export const Guest: Story = {
	args: { room: { ...owned, administer: false }, onAdd: noop, onRemove: noop, onClose: noop },
};

/**
 * An address the book does not hold: the server's 400 in its own words, under the field it was typed in.
 * The sentence says what to do about it, which is why it is shown rather than swallowed (ADR 0034 §1).
 */
export const UnknownAddress: Story = {
	args: {
		room: owned,
		failure: "bo@example.com is not in People: ask the install's admin to add them first",
		onAdd: noop,
		onRemove: noop,
		onClose: noop,
	},
};

/** A phone: the rows wrap and the field prints at 16px, so iOS does not zoom it. */
export const Phone: Story = {
	args: { ...Owner.args },
	globals: { viewport: { value: "phone" } },
};
