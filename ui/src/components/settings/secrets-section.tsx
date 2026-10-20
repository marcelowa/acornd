import { useCallback, useEffect, useState } from "react";
import type { SecretMeta, SecretScope, SecretSubstitution, WorkspaceSecrets } from "@/app/client";
import {
	deleteSecret,
	fetchGlobalSecrets,
	fetchWorkspaceSecrets,
	rotateSecret,
	saveSecret,
} from "@/app/client";
import { Stack } from "@/components/layout/stack";
import { Text } from "@/components/typography/text";
import { Button } from "@/components/ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { SecretInput } from "@/components/ui/secret-input";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { SettingsField, SettingsGroup } from "./settings-group";

export interface SecretsSectionProps {
	/** The workspace in view. Its own list is here; the globals are beside it. */
	workspace?: { name: string; path: string } | undefined;
	/** The engine decides how a value reaches a command, and the copy says so. */
	engine?: string | undefined;
}

type Editor =
	| { kind: "create"; scope: SecretScope }
	| { kind: "override"; secret: SecretMeta }
	| { kind: "rotate"; scope: SecretScope; secret: SecretMeta };

/**
 * The install's secrets: what the agent's commands may use, never their values. Global
 * entries are on every workspace; a workspace entry with the same name replaces one there.
 * The list is metadata only, because the API has no way to return a value.
 */
export function SecretsSection({ workspace, engine }: SecretsSectionProps) {
	const [global, setGlobal] = useState<SecretMeta[]>([]);
	const [view, setView] = useState<WorkspaceSecrets | undefined>(undefined);
	const [loading, setLoading] = useState(true);
	const [failure, setFailure] = useState<string | undefined>(undefined);
	const [editor, setEditor] = useState<Editor | undefined>(undefined);
	const [confirming, setConfirming] = useState<string | undefined>(undefined);
	const workspaceName = workspace?.name ?? "";

	const load = useCallback(async () => {
		if (workspaceName === "") {
			setGlobal([]);
			setView(undefined);
			setLoading(false);
			return;
		}
		setLoading(true);
		setFailure(undefined);
		try {
			const [globals, workspaceView] = await Promise.all([
				fetchGlobalSecrets(),
				fetchWorkspaceSecrets(workspaceName),
			]);
			setGlobal(globals);
			setView(workspaceView);
		} catch (error) {
			setFailure(messageOf(error));
		} finally {
			setLoading(false);
		}
	}, [workspaceName]);

	useEffect(() => {
		void load();
	}, [load]);

	if (workspace === undefined) {
		return (
			<Text size="sm" tone="muted">
				Choose a workspace to see the secrets it may use.
			</Text>
		);
	}

	const remove = async (scope: SecretScope, name: string): Promise<void> => {
		try {
			await deleteSecret(scope, workspaceName, name);
			setConfirming(undefined);
			await load();
		} catch (error) {
			setFailure(messageOf(error));
		}
	};

	const done = async (): Promise<void> => {
		setEditor(undefined);
		await load();
	};

	const effective = view?.effective ?? [];
	const overriddenNames = new Set(
		(view?.own ?? [])
			.filter((secret) => secret.overrides === true)
			.map((secret) => secret.name),
	);

	return (
		<Stack gap="lg" data-slot="secrets-section">
			{/* This workspace first: it is the question someone opened settings to answer. */}
			<SettingsGroup
				slot="secrets-workspace"
				title="This workspace"
				intro={
					<Text size="2xs" tone="muted">
						What {workspace.name}'s commands may use. A value set here replaces a global
						entry with the same name.
					</Text>
				}
			>
				{failure === undefined ? null : (
					<Text size="2xs" className="text-destructive-subtle-foreground">
						{failure}
					</Text>
				)}
				{loading && view === undefined ? (
					<Text size="sm" tone="muted">
						Loading secrets…
					</Text>
				) : effective.length === 0 ? (
					<Text size="sm" tone="muted">
						No secrets here yet.
					</Text>
				) : (
					<div className="flex flex-col gap-1">
						{effective.map((secret) => (
							<SecretRow
								key={`${secret.source ?? secret.scope}:${secret.name}`}
								secret={secret}
								note={
									secret.source === "global"
										? "inherited from global"
										: secret.overrides === true
											? "replaces the global value"
											: "this workspace"
								}
								confirming={
									confirming === `${secret.source ?? secret.scope}:${secret.name}`
								}
								onConfirm={() =>
									setConfirming(`${secret.source ?? secret.scope}:${secret.name}`)
								}
								onKeep={() => setConfirming(undefined)}
								onRotate={() =>
									setEditor({
										kind: "rotate",
										scope: secret.source ?? secret.scope,
										secret,
									})
								}
								onForget={() =>
									void remove(secret.source ?? secret.scope, secret.name)
								}
								onOverride={
									secret.source === "global"
										? () => setEditor({ kind: "override", secret })
										: undefined
								}
							/>
						))}
					</div>
				)}
				<div>
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => setEditor({ kind: "create", scope: "workspace" })}
					>
						Add to this workspace
					</Button>
				</div>
			</SettingsGroup>

			<SettingsGroup
				slot="secrets-global"
				title="Global"
				intro={
					<Text size="2xs" tone="muted">
						Every workspace may use these. A workspace value with the same name wins
						there.
					</Text>
				}
			>
				{global.length === 0 ? (
					<Text size="sm" tone="muted">
						No global secrets.
					</Text>
				) : (
					<div className="flex flex-col gap-1">
						{global.map((secret) => (
							<SecretRow
								key={secret.name}
								secret={secret}
								note={
									overriddenNames.has(secret.name)
										? "replaced in this workspace"
										: "global"
								}
								confirming={confirming === `global:${secret.name}`}
								onConfirm={() => setConfirming(`global:${secret.name}`)}
								onKeep={() => setConfirming(undefined)}
								onRotate={() =>
									setEditor({ kind: "rotate", scope: "global", secret })
								}
								onForget={() => void remove("global", secret.name)}
								onOverride={
									overriddenNames.has(secret.name)
										? undefined
										: () => setEditor({ kind: "override", secret })
								}
							/>
						))}
					</div>
				)}
				<div>
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => setEditor({ kind: "create", scope: "global" })}
					>
						Add global secret
					</Button>
				</div>
			</SettingsGroup>

			{editor === undefined ? null : (
				<SecretDialog
					key={
						editor.kind === "create"
							? `create:${editor.scope}`
							: editor.kind === "override"
								? `override:${editor.secret.name}`
								: `rotate:${editor.scope}:${editor.secret.name}`
					}
					editor={editor}
					workspace={workspaceName}
					engine={engine}
					onClose={() => setEditor(undefined)}
					onDone={done}
				/>
			)}
		</Stack>
	);
}

interface SecretRowProps {
	secret: SecretMeta;
	note: string;
	confirming: boolean;
	onConfirm: () => void;
	onKeep: () => void;
	onRotate: () => void;
	onForget: () => void;
	onOverride?: (() => void) | undefined;
}

function SecretRow({
	secret,
	note,
	confirming,
	onConfirm,
	onKeep,
	onRotate,
	onForget,
	onOverride,
}: SecretRowProps) {
	return (
		<div
			data-slot="secret-row"
			data-name={secret.name}
			className="flex items-center gap-2 rounded-md px-2 py-1.5"
		>
			<div className="flex min-w-0 flex-1 flex-col">
				<Text as="span" size="sm" weight="medium">
					{secret.name}
				</Text>
				<Text as="span" size="2xs" tone="muted" className="truncate">
					{secret.description}
				</Text>
				<Text as="span" size="2xs" tone="muted" className="truncate">
					{detail(secret)} · {note}
				</Text>
			</div>
			{confirming ? (
				<span className="flex shrink-0 items-center gap-1">
					<Button type="button" variant="destructive" size="sm" onClick={onForget}>
						Forget
					</Button>
					<Button type="button" variant="ghost" size="sm" onClick={onKeep}>
						Keep
					</Button>
				</span>
			) : (
				<span className="flex shrink-0 items-center gap-1">
					{onOverride === undefined ? null : (
						<Button type="button" variant="ghost" size="sm" onClick={onOverride}>
							Override here
						</Button>
					)}
					<Button type="button" variant="ghost" size="sm" onClick={onRotate}>
						Replace value
					</Button>
					<Button type="button" variant="ghost" size="sm" onClick={onConfirm}>
						Forget
					</Button>
				</span>
			)}
		</div>
	);
}

function detail(secret: SecretMeta): string {
	const hosts = secret.hosts.length === 0 ? "no allowed hosts" : `for ${secret.hosts.join(", ")}`;
	const sandbox = secret.allowEnv ? "value may enter a container" : "kept out of containers";
	return `${hosts} · ${sandbox}`;
}

interface SecretDialogProps {
	editor: Editor;
	workspace: string;
	engine?: string | undefined;
	onClose: () => void;
	onDone: () => Promise<void>;
}

function SecretDialog({ editor, workspace, engine, onClose, onDone }: SecretDialogProps) {
	const rotating = editor.kind === "rotate";
	const source = editor.kind === "override" ? "workspace" : editor.scope;
	const initial = initialValues(editor);
	const [name, setName] = useState(initial.name);
	const [description, setDescription] = useState(initial.description);
	const [value, setValue] = useState("");
	const [hosts, setHosts] = useState(initial.hosts);
	const [substitution, setSubstitution] = useState(initial.substitution);
	const [allowEnv, setAllowEnv] = useState(initial.allowEnv);
	const [saving, setSaving] = useState(false);
	const [failure, setFailure] = useState<string | undefined>(undefined);

	const locations = substitutionValues(substitution);
	const validName = rotating || editor.kind === "override" || /^[A-Z][A-Z0-9_]*$/.test(name);
	const valid =
		value !== "" &&
		(rotating ||
			(description.trim() !== "" &&
				validName &&
				locations.length > 0 &&
				(engine !== "microsandbox" || splitHosts(hosts).length > 0)));

	const submit = async (): Promise<void> => {
		if (!valid || saving) return;
		setSaving(true);
		setFailure(undefined);
		try {
			if (editor.kind === "rotate") {
				await rotateSecret(editor.scope, workspace, editor.secret.name, value);
			} else {
				await saveSecret(source, workspace, {
					name: editor.kind === "override" ? editor.secret.name : name,
					description: description.trim(),
					value,
					hosts: splitHosts(hosts),
					substitution,
					allowEnv,
				});
			}
			await onDone();
		} catch (error) {
			setFailure(messageOf(error));
		} finally {
			setSaving(false);
		}
	};

	const title =
		editor.kind === "rotate"
			? `Replace ${editor.secret.name}`
			: editor.kind === "override"
				? `Override ${editor.secret.name} here`
				: editor.kind === "create" && editor.scope === "global"
					? "Add a global secret"
					: "Add a secret to this workspace";
	const intro =
		editor.kind === "rotate"
			? "The name, description, hosts, and scope stay; only the value changes."
			: editor.kind === "override"
				? `This value replaces the global ${editor.secret.name} for ${workspace} only.`
				: editor.scope === "global"
					? "Every workspace may use it. Keep it specific: a global value is available everywhere."
					: `Only ${workspace} may use it.`;
	const delivery =
		engine === "microsandbox"
			? "The value stays on the host and is substituted only for the hosts you name. A secret with no hosts cannot be delivered on this engine."
			: "This engine puts the value in the sandbox environment, where a command can read it.";

	return (
		<Dialog
			open
			onOpenChange={(next) => {
				if (!next) onClose();
			}}
		>
			<DialogContent className="sm:max-w-lg">
				<DialogHeader>
					<DialogTitle>{title}</DialogTitle>
					<DialogDescription>{intro}</DialogDescription>
				</DialogHeader>
				<div className="flex flex-col gap-3">
					{rotating ? null : (
						<SettingsField label="name">
							<Input
								inputSize="sm"
								value={name}
								disabled={editor.kind === "override"}
								placeholder="GITHUB_TOKEN"
								autoComplete="off"
								autoCapitalize="off"
								autoCorrect="off"
								spellCheck={false}
								data-1p-ignore="true"
								data-bwignore="true"
								data-lpignore="true"
								data-form-type="other"
								aria-label="Secret name"
								onChange={(event) =>
									setName(
										event.target.value
											.toUpperCase()
											.replace(/[^A-Z0-9_]/g, "")
											.slice(0, 128),
									)
								}
							/>
							<Text size="2xs" tone="muted">
								The environment variable a command uses. Uppercase letters, digits,
								and underscores.
							</Text>
						</SettingsField>
					)}
					{rotating ? null : (
						<SettingsField label="description">
							<Input
								inputSize="sm"
								value={description}
								placeholder="What is it for?"
								autoComplete="off"
								autoCapitalize="off"
								autoCorrect="off"
								data-1p-ignore="true"
								data-bwignore="true"
								data-lpignore="true"
								data-form-type="other"
								aria-label="Secret description"
								onChange={(event) => setDescription(event.target.value)}
							/>
						</SettingsField>
					)}
					<SettingsField label={rotating ? "new value" : "value"}>
						<SecretInput
							inputSize="sm"
							value={value}
							placeholder="the value is never shown again"
							aria-label="Secret value"
							onChange={(event) => setValue(event.target.value)}
						/>
					</SettingsField>
					{rotating ? null : (
						<>
							<SettingsField
								label={
									engine === "microsandbox"
										? "allowed hosts (required)"
										: "allowed hosts"
								}
							>
								<Input
									inputSize="sm"
									value={hosts}
									placeholder="api.github.com, *.example.org"
									autoComplete="off"
									autoCapitalize="off"
									autoCorrect="off"
									spellCheck={false}
									data-1p-ignore="true"
									data-bwignore="true"
									data-lpignore="true"
									data-form-type="other"
									aria-label="Allowed hosts"
									onChange={(event) => setHosts(event.target.value)}
								/>
								<Text size="2xs" tone="muted">
									Comma-separated. No scheme or path; a leading * is a wildcard.
									{engine === "microsandbox" && splitHosts(hosts).length === 0
										? " This engine needs at least one host to deliver the value."
										: ""}
								</Text>
							</SettingsField>
							<SettingsField label="substitute in">
								<ToggleGroup
									type="multiple"
									value={locations}
									onValueChange={(next) =>
										setSubstitution(substitutionFrom(next))
									}
									variant="outline"
									size="sm"
									spacing={0}
									aria-label="Substitution locations"
								>
									<ToggleGroupItem value="headers">headers</ToggleGroupItem>
									<ToggleGroupItem value="query">query</ToggleGroupItem>
									<ToggleGroupItem value="body">body</ToggleGroupItem>
								</ToggleGroup>
							</SettingsField>
							<SettingsField label="sandbox value">
								<ToggleGroup
									type="single"
									value={allowEnv ? "allow" : "keep"}
									onValueChange={(next) => {
										if (next === "allow") setAllowEnv(true);
										if (next === "keep") setAllowEnv(false);
									}}
									variant="outline"
									size="sm"
									spacing={0}
									aria-label="Value in a container sandbox"
								>
									<ToggleGroupItem value="allow">may enter</ToggleGroupItem>
									<ToggleGroupItem value="keep">keep out</ToggleGroupItem>
								</ToggleGroup>
								<Text size="2xs" tone="muted">
									Only matters on a container engine; microsandbox never puts the
									value in the sandbox.
								</Text>
							</SettingsField>
						</>
					)}
					<Text size="2xs" tone="muted">
						{delivery}
					</Text>
					{failure === undefined ? null : (
						<Text size="2xs" className="text-destructive-subtle-foreground">
							{failure}
						</Text>
					)}
				</div>
				<DialogFooter>
					<Button type="button" variant="outline" onClick={onClose}>
						Cancel
					</Button>
					<Button type="button" disabled={!valid || saving} onClick={() => void submit()}>
						{saving
							? "Saving…"
							: editor.kind === "rotate"
								? "Rotate value"
								: editor.kind === "override"
									? "Override here"
									: "Save secret"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

function initialValues(editor: Editor): {
	name: string;
	description: string;
	hosts: string;
	substitution: SecretSubstitution;
	allowEnv: boolean;
} {
	if (editor.kind === "override" || editor.kind === "rotate") {
		const secret = editor.secret;
		return {
			name: secret.name,
			description: secret.description,
			hosts: secret.hosts.join(", "),
			substitution: secret.substitution,
			allowEnv: secret.allowEnv,
		};
	}
	return {
		name: "",
		description: "",
		hosts: "",
		substitution: { headers: true, query: false, body: false },
		allowEnv: true,
	};
}

function substitutionValues(substitution: SecretSubstitution): string[] {
	const values: string[] = [];
	if (substitution.headers) values.push("headers");
	if (substitution.query) values.push("query");
	if (substitution.body) values.push("body");
	return values;
}

function substitutionFrom(values: readonly string[]): SecretSubstitution {
	return {
		headers: values.includes("headers"),
		query: values.includes("query"),
		body: values.includes("body"),
	};
}

function splitHosts(value: string): string[] {
	return value
		.split(",")
		.map((host) => host.trim().toLowerCase())
		.filter((host) => host !== "");
}

function messageOf(error: unknown): string {
	return error instanceof Error ? error.message : String(error);
}
