import type { Meta, StoryObj } from "@storybook/react-vite";
import type { ProviderSummary } from "@/app/view-models";
import { Stack } from "@/components/layout/stack";
import { Picker } from "@/components/composer/picker";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { PeopleSection } from "./people-section";
import { ProvidersSection } from "./providers-section";
import { RemoteSection } from "./remote-section";
import { SessionSectionView } from "./session-section";
import { SettingsScreen } from "./settings-screen";
import { Text } from "@/components/typography/text";
import { UpdatesSection } from "@/components/release/updates-section";

const meta = {
	title: "Screens/Settings",
	component: SettingsScreen,
	parameters: { layout: "fullscreen" },
} satisfies Meta<typeof SettingsScreen>;

export default meta;
type Story = StoryObj<typeof meta>;

const noop = (): void => undefined;

/**
 * The app builds the defaults section from the pickers it already has, so the story does the
 * same with literals: the screen does not know what a model is.
 */
const defaults = (
	<Stack gap="sm">
		<ToggleGroup
			type="single"
			defaultValue="workspace"
			variant="outline"
			size="sm"
			spacing={0}
			aria-label="Default scope"
		>
			<ToggleGroupItem value="workspace">This workspace</ToggleGroupItem>
			<ToggleGroupItem value="install">Every workspace</ToggleGroupItem>
		</ToggleGroup>
		<Picker
			label="Model"
			value="deepseek-v4.1-flash"
			groups={[
				{
					label: "opencode-go",
					options: [
						{ value: "deepseek-v4.1-flash", label: "deepseek-v4.1-flash" },
						{ value: "minimax-m3", label: "minimax-m3" },
					],
				},
			]}
			onSelect={noop}
		/>
		<Picker
			label="Thinking"
			value="max"
			groups={[
				{
					label: "levels",
					options: [
						{ value: "off", label: "off" },
						{ value: "medium", label: "medium" },
						{ value: "max", label: "max" },
					],
				},
			]}
			onSelect={noop}
		/>
		<Picker
			label="Agent"
			value="general"
			groups={[
				{
					label: "agents",
					options: [
						{ value: "general", label: "general" },
						{ value: "researcher", label: "researcher" },
					],
				},
			]}
			onSelect={noop}
		/>
	</Stack>
);

const providers: ProviderSummary[] = [
	{
		id: "opencode-go",
		name: "OpenCode Go",
		authenticated: true,
		source: "OPENCODE_API_KEY",
		authType: "api_key",
		authTypes: ["api_key", "oauth"],
		modelCount: 29,
	},
	{
		id: "commandcode",
		name: "CommandCode",
		authenticated: false,
		authTypes: ["api_key"],
		modelCount: 0,
	},
	{
		id: "aws",
		name: "AWS Bedrock",
		authenticated: false,
		authTypes: [],
		modelCount: 0,
	},
];

const install = (
	<UpdatesSection
		status={{
			current: "v0.9.33",
			latest: "v0.9.33",
			notes: "",
			channel: "releases",
			autoCheck: true,
			checking: false,
			available: false,
			backups: [],
			downloadSize: 13_600_000,
			workerSize: 81_000,
			runsActive: false,
			runs: [],
		}}
		onSavePrefs={noop}
		onOpenReleases={noop}
	/>
);

export const Default: Story = {
	args: {
		tab: "general",
		onTabChange: noop,
		theme: "system",
		onThemeChange: noop,
		defaults,
		providers: <ProvidersSection providers={providers} />,
		secrets: (
			<Text size="sm" tone="muted">
				The workspace's secrets and the globals.
			</Text>
		),
		install,
		remote: <RemoteSection onApply={noop} />,
		session: (
			<SessionSectionView
				session={{
					setup: false,
					owner: "marcelo@example.com",
					email: "marcelo@example.com",
					signedIn: true,
				}}
				onSignOut={noop}
			/>
		),
		people: (
			<PeopleSection
				people={[]}
				onAdd={noop}
				onRevoke={noop}
				onRestore={noop}
				onSetCanCreate={noop}
				onSetModels={noop}
			/>
		),
		admin: true,
		version: "v0.9.2",
		onClose: noop,
	},
};

/** Dark is a token swap, not a second screen: the same settings with the other theme chosen. */
export const Dark: Story = {
	args: { ...Default.args, theme: "dark" },
};

/** Each page has its own URL: the remote-access page, which is where a family install makes its door. */
export const Remote: Story = {
	args: { ...Default.args, tab: "remote" },
};

/** Each page has its own URL: the providers page. */
export const Providers: Story = {
	args: { ...Default.args, tab: "providers" },
};

/**
 * The People page: the admin's, and it is in the bar only for the admin. Who may use this install, what they
 * may run, and whether they may lead a room of their own (ADR 0034 §1, §3).
 */
export const People: Story = {
	args: {
		...Default.args,
		tab: "people",
		people: (
			<PeopleSection
				people={[
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
						canCreate: false,
						created: "2026-10-28",
					},
				]}
				onAdd={noop}
				onRevoke={noop}
				onRestore={noop}
				onSetCanCreate={noop}
				onSetModels={noop}
			/>
		),
	},
};

/**
 * A member's settings: the People tab is not in the bar at all, and a link to its address lands on the
 * install page rather than on a page with no tab selected.
 */
export const PeopleForAMember: Story = {
	args: { ...Default.args, tab: "people", admin: false },
};

/** The secrets page lists names and descriptions; a value is never shown. */
export const Secrets: Story = {
	args: { ...Default.args, tab: "secrets" },
};

/** A phone: the tab bar, the fields and the controls at 390px. */
export const Phone: Story = {
	args: Default.args,
	globals: { viewport: { value: "phone" } },
};

/** The updates section before the gateway answers: the sentence, not a blank. */
export const UpdatesUnanswered: Story = {
	args: {
		...Default.args,
		install: <UpdatesSection status={undefined} onSavePrefs={noop} onOpenReleases={noop} />,
		remote: <RemoteSection onApply={noop} />,
	},
};
