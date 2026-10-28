import { useState } from "react";
import type { RemoteStatus, RemoteStep } from "@/app/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Text } from "@/components/typography/text";
import { SettingsGroup } from "./settings-group";

export interface RemoteSectionProps {
	/** What the install recorded, once the gateway answered. Absent means "not asked yet". */
	status?: RemoteStatus | undefined;
	/** The refusal the last press earned, in the words the gateway used. */
	failure?: string | undefined;
	/** Whether a press is in flight: the button says so rather than the screen going quiet. */
	busy?: boolean | undefined;
	onApply: (request: { hostname: string; zone: string; token: string }) => void;
}

/** One step as a person reads it: the verb, the piece, and the detail when there is one. */
function stepLine(step: RemoteStep): string {
	const verb = step.created ? "created" : step.moved ? "pointed" : "kept";
	return step.detail === undefined
		? `${verb} ${step.name}`
		: `${verb} ${step.name} · ${step.detail}`;
}

/**
 * The door, from inside the console: what a browser reaches this install at, the one token that talks to
 * Cloudflare, and the list of what the apply did. It replaces the first terminal a family install needed — and
 * the token lives only in the form: it is used for the press and never shown again, because what the running
 * process keeps is the tunnel's token, not the account's (ADR 0033 §4).
 */
export function RemoteSection({ status, failure, busy = false, onApply }: RemoteSectionProps) {
	const [hostname, setHostname] = useState(status?.hostname ?? "");
	const [zone, setZone] = useState("");
	const [token, setToken] = useState("");
	const steps = status?.steps ?? [];
	return (
		<SettingsGroup
			slot="settings-remote"
			title="Remote access"
			intro={
				<Text size="2xs" tone="muted">
					{status?.configured === true
						? `A browser reaches this install at ${status.hostname}. Everybody the book admits gets in by email; nothing else does.`
						: "Nobody can reach this install from outside yet. A Cloudflare account, a domain, and one API token are what it takes."}
				</Text>
			}
		>
			<div className="flex flex-col gap-3">
				{status?.configured === true ? (
					<dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
						<dt>
							<Text size="2xs" tone="muted">
								Hostname
							</Text>
						</dt>
						<dd>
							<Text size="2xs">{status.hostname}</Text>
						</dd>
						{status.audience === undefined ? null : (
							<>
								<dt>
									<Text size="2xs" tone="muted">
										Audience
									</Text>
								</dt>
								<dd>
									<Text size="2xs" className="break-all">
										{status.audience}
									</Text>
								</dd>
							</>
						)}
						{status.updated === undefined ? null : (
							<>
								<dt>
									<Text size="2xs" tone="muted">
										Last apply
									</Text>
								</dt>
								<dd>
									<Text size="2xs">{status.updated}</Text>
								</dd>
							</>
						)}
					</dl>
				) : null}

				<form
					className="flex flex-col gap-2"
					onSubmit={(event) => {
						event.preventDefault();
						if (busy) return;
						onApply({
							hostname: hostname.trim(),
							zone: zone.trim(),
							token: token.trim(),
						});
					}}
				>
					<label className="flex flex-col gap-1">
						<Text size="2xs" tone="muted">
							Hostname a browser types
						</Text>
						<Input
							name="hostname"
							value={hostname}
							placeholder="console.example.com"
							autoComplete="off"
							onChange={(event) => setHostname(event.target.value)}
						/>
					</label>
					<label className="flex flex-col gap-1">
						<Text size="2xs" tone="muted">
							The Cloudflare zone it belongs to
						</Text>
						<Input
							name="zone"
							value={zone}
							placeholder="example.com"
							autoComplete="off"
							onChange={(event) => setZone(event.target.value)}
						/>
					</label>
					<label className="flex flex-col gap-1">
						<Text size="2xs" tone="muted">
							API token — used now, never stored
						</Text>
						<Input
							name="token"
							type="password"
							value={token}
							placeholder="Paste it here"
							autoComplete="off"
							onChange={(event) => setToken(event.target.value)}
						/>
					</label>
					<div>
						<Button
							type="submit"
							size="sm"
							disabled={busy || hostname.trim() === "" || zone.trim() === ""}
						>
							{busy
								? "Making it…"
								: status?.configured === true
									? "Apply again"
									: "Make the door"}
						</Button>
					</div>
				</form>

				{failure === undefined ? null : (
					<Text size="2xs" className="text-destructive">
						{failure}
					</Text>
				)}

				{steps.length === 0 ? null : (
					<ul className="flex flex-col gap-1">
						{steps.map((step) => (
							<li key={step.name}>
								<Text size="2xs" tone="muted">
									{stepLine(step)}
								</Text>
							</li>
						))}
					</ul>
				)}
			</div>
		</SettingsGroup>
	);
}
