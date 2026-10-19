import { useState } from "react";
import type { SecretAnswerInput } from "@/app/client";
import type { WireSecretEvent } from "@/app/wire";
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
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

export interface SecretRequestDialogProps {
	/** The open request, if any. The value arrives only from this card. */
	request?: WireSecretEvent | undefined;
	engine?: string | undefined;
	onAnswer: (requestId: string, input: SecretAnswerInput) => Promise<void>;
	onCancel: (requestId: string) => Promise<void>;
}

/**
 * The agent asked for a secret. The card is the only place the value is typed: it posts to
 * the gateway, which writes the vault, and the tool waiting on the other side learns only
 * whether it was set. A value typed into the composer instead would be in the transcript.
 */
export function SecretRequestDialog({
	request,
	engine,
	onAnswer,
	onCancel,
}: SecretRequestDialogProps) {
	if (request === undefined) return null;
	// The key at the call site remounts this for a new request, so the fields start from that
	// request's suggestions rather than the previous one's.
	return (
		<SecretRequestCard
			request={request}
			engine={engine}
			onAnswer={onAnswer}
			onCancel={onCancel}
		/>
	);
}

function SecretRequestCard({
	request,
	engine,
	onAnswer,
	onCancel,
}: {
	request: WireSecretEvent;
	engine?: string | undefined;
	onAnswer: SecretRequestDialogProps["onAnswer"];
	onCancel: SecretRequestDialogProps["onCancel"];
}) {
	const [description, setDescription] = useState(request.description ?? "");
	const [value, setValue] = useState("");
	const [hosts, setHosts] = useState((request.hosts ?? []).join(", "));
	const [scope, setScope] = useState<"workspace" | "global">("workspace");
	const [saving, setSaving] = useState(false);
	const [failure, setFailure] = useState<string | undefined>(undefined);

	const hostList = hosts
		.split(",")
		.map((host) => host.trim().toLowerCase())
		.filter((host) => host !== "");
	const valid =
		value !== "" &&
		description.trim() !== "" &&
		(engine !== "microsandbox" || hostList.length > 0);

	const answer = async (): Promise<void> => {
		if (!valid || saving) return;
		setSaving(true);
		setFailure(undefined);
		try {
			await onAnswer(request.requestId, {
				value,
				description: description.trim(),
				hosts: hostList,
				scope,
			});
		} catch (error) {
			setFailure(error instanceof Error ? error.message : String(error));
		} finally {
			setSaving(false);
		}
	};

	const cancel = async (): Promise<void> => {
		try {
			await onCancel(request.requestId);
		} catch (error) {
			setFailure(error instanceof Error ? error.message : String(error));
		}
	};

	const engineCopy =
		engine === "microsandbox"
			? "The value stays on the host and is substituted only for the hosts below."
			: "The value enters this sandbox's environment, where a command can read it.";

	return (
		<Dialog
			open
			onOpenChange={(next) => {
				if (!next) void cancel();
			}}
		>
			<DialogContent className="sm:max-w-md">
				<DialogHeader>
					<DialogTitle>Set {request.name ?? "a secret"}</DialogTitle>
					<DialogDescription>
						The agent needs this to run a command. The value goes straight to the vault;
						it will not appear in this conversation or to the model.
					</DialogDescription>
				</DialogHeader>
				<Stack gap="sm">
					<label className="flex flex-col gap-1">
						<Text size="2xs" tone="muted">
							what it is for
						</Text>
						<Input
							inputSize="sm"
							value={description}
							placeholder="A read-only token for the release script"
							aria-label="Secret description"
							onChange={(event) => setDescription(event.target.value)}
						/>
					</label>
					<label className="flex flex-col gap-1">
						<Text size="2xs" tone="muted">
							value
						</Text>
						<Input
							inputSize="sm"
							type="password"
							value={value}
							placeholder="the value is never shown again"
							autoComplete="new-password"
							spellCheck={false}
							aria-label="Secret value"
							onChange={(event) => setValue(event.target.value)}
						/>
					</label>
					<label className="flex flex-col gap-1">
						<Text size="2xs" tone="muted">
							allowed hosts{engine === "microsandbox" ? " (required)" : ""}
						</Text>
						<Input
							inputSize="sm"
							value={hosts}
							placeholder="api.github.com, *.example.org"
							autoComplete="off"
							spellCheck={false}
							aria-label="Allowed hosts"
							onChange={(event) => setHosts(event.target.value)}
						/>
					</label>
					<div className="flex flex-col gap-1">
						<Text size="2xs" tone="muted">
							where
						</Text>
						<ToggleGroup
							type="single"
							value={scope}
							onValueChange={(next) => {
								if (next === "workspace" || next === "global") setScope(next);
							}}
							variant="outline"
							size="sm"
							spacing={0}
							aria-label="Secret scope"
						>
							<ToggleGroupItem value="workspace">This workspace</ToggleGroupItem>
							<ToggleGroupItem value="global">Every workspace</ToggleGroupItem>
						</ToggleGroup>
						{scope === "global" ? (
							<Text size="2xs" tone="muted">
								Every workspace will be able to use it.
							</Text>
						) : null}
					</div>
					<Text size="2xs" tone="muted">
						{engineCopy}
						{engine === "microsandbox" && hostList.length === 0
							? " A secret with no hosts cannot be used on this engine."
							: ""}
					</Text>
					{failure === undefined ? null : (
						<Text size="2xs" className="text-destructive-subtle-foreground">
							{failure}
						</Text>
					)}
				</Stack>
				<DialogFooter>
					<Button type="button" variant="outline" onClick={() => void cancel()}>
						Not now
					</Button>
					<Button type="button" disabled={!valid || saving} onClick={() => void answer()}>
						{saving ? "Saving…" : "Save secret"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
