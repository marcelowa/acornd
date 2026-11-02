import { useEffect, useState } from "react";
import type { DoorReport, RemotePreview, RemoteStatus, RemoteStep } from "@/app/client";
import { completeHostname } from "@/app/remote";
import { Button } from "@/components/ui/button";
import { Input, inputVariants } from "@/components/ui/input";
import { Text } from "@/components/typography/text";
import { cn } from "cn";
import { SettingsGroup } from "./settings-group";

export interface RemoteSectionProps {
	/** What the install recorded, once the gateway answered. Absent means "not asked yet". */
	status?: RemoteStatus | undefined;
	/** What the token can see, once somebody checked it: the zones to pick from, and the team domain. */
	preview?: RemotePreview | undefined;
	/** A token is being checked. */
	previewBusy?: boolean | undefined;
	/** The refusal the last press earned, in the words the gateway used. */
	failure?: string | undefined;
	/** Whether a press is in flight: the button says so rather than the screen going quiet. */
	busy?: boolean | undefined;
	onApply: (request: { hostname: string; zone: string; token: string }) => void;
	/** Ask Cloudflare what this token can see. The token is used for the call and not stored by it. */
	onPreview?: ((token: string) => void) | undefined;
	/** Forget the stored token. Absent when there is none, or when the page is a story. */
	onForgetToken?: (() => void) | undefined;
	/** Restart the console so the door the file describes becomes the one it serves. */
	onEnable?: (() => void) | undefined;
	restarting?: boolean | undefined;
	restarted?: boolean | undefined;
	/** The last answer from outside, once somebody pressed the check. */
	check?: DoorReport | undefined;
	checking?: boolean | undefined;
	onCheck?: (() => void) | undefined;
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
 * Cloudflare, and the list of what the apply did. A token that works is kept 0600 in the install folder so the
 * door follows the book when People changes; it is never shown again, and the screen only says whether it is
 * there (ADR 0033 §3, §4 as amended).
 *
 * Two presses, in the order the install needs them (ADR 0036 §6): connect Cloudflare — a token, a zone the token
 * can see, and a name typed as a label or a whole one — and then turn it on, because a running process cannot
 * become a door it did not start with. Turning it on restarts the console onto loopback, so the panel says what
 * happens to the address this page was opened on *before* the button does it.
 */
export function RemoteSection({
	status,
	preview,
	previewBusy = false,
	failure,
	busy = false,
	check,
	checking = false,
	onApply,
	onPreview,
	onForgetToken,
	onEnable,
	restarting = false,
	restarted = false,
	onCheck,
}: RemoteSectionProps) {
	const [hostname, setHostname] = useState(status?.hostname ?? "");
	const [zone, setZone] = useState("");
	const [token, setToken] = useState("");
	const zones = preview?.zones ?? [];
	const teamDomain = preview?.teamDomain ?? status?.teamDomain ?? "";
	const target = completeHostname(hostname, zone);
	const ready =
		target !== "" && zone !== "" && (token.trim() !== "" || status?.tokenStored === true);
	const steps = status?.steps ?? [];
	// One zone is not a choice: it is the answer, and a picker with one option is a step for no reason. The
	// name rather than the array, because the answer is rebuilt on every render and the effect is not.
	const onlyZone = preview?.zones.length === 1 ? preview.zones[0]?.name : undefined;

	useEffect(() => {
		if (zone === "" && onlyZone !== undefined) setZone(onlyZone);
	}, [onlyZone, zone]);

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
						{teamDomain === "" ? null : (
							<>
								<dt>
									<Text size="2xs" tone="muted">
										Team domain
									</Text>
								</dt>
								<dd>
									<Text size="2xs" className="break-all">
										{teamDomain}
									</Text>
								</dd>
							</>
						)}
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
						<dt>
							<Text size="2xs" tone="muted">
								API token
							</Text>
						</dt>
						<dd>
							<Text
								size="2xs"
								tone={status.tokenStored === true ? "muted" : "default"}
							>
								{status.tokenStored === true
									? "stored on this machine, 0600 — the door follows People"
									: "not stored — adding somebody will not reach the door"}
							</Text>
						</dd>
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
							API token — kept on this machine so the door follows People
						</Text>
						<Input
							name="token"
							type="password"
							value={token}
							placeholder={
								status?.tokenStored === true
									? "Paste a new one to replace it"
									: "Paste it here"
							}
							autoComplete="off"
							onChange={(event) => setToken(event.target.value)}
						/>
					</label>
					<div className="flex items-center gap-2">
						<Button
							type="button"
							size="sm"
							variant="outline"
							disabled={
								previewBusy || (token.trim() === "" && status?.tokenStored !== true)
							}
							onClick={() => onPreview?.(token.trim())}
						>
							{previewBusy ? "Checking…" : "Check the token"}
						</Button>
						{teamDomain === "" ? null : (
							<Text size="2xs" tone="muted">
								Team domain {teamDomain}
							</Text>
						)}
					</div>

					{zones.length === 0 ? null : (
						<label className="flex flex-col gap-1">
							<Text size="2xs" tone="muted">
								The zone this token can see
							</Text>
							<select
								name="zone"
								value={zone}
								className={cn(inputVariants())}
								onChange={(event) => setZone(event.target.value)}
							>
								<option value="">Choose one</option>
								{zones.map((one) => (
									<option key={one.id} value={one.name}>
										{one.name}
									</option>
								))}
							</select>
						</label>
					)}

					<label className="flex flex-col gap-1">
						<Text size="2xs" tone="muted">
							Hostname a browser types
						</Text>
						<Input
							name="hostname"
							value={hostname}
							placeholder={zone === "" ? "console" : `console.${zone}`}
							autoComplete="off"
							onChange={(event) => setHostname(event.target.value)}
						/>
						<Text size="2xs" tone="muted" className="break-all">
							{target === ""
								? "A label such as console is completed with the zone; a whole name has to end with it."
								: `Answers at ${target}`}
						</Text>
					</label>

					<div className="flex items-center gap-2">
						<Button type="submit" size="sm" disabled={busy || !ready}>
							{busy
								? "Making it…"
								: status?.configured === true
									? "Apply again"
									: "Make the door"}
						</Button>
						{status?.tokenStored !== true || onForgetToken === undefined ? null : (
							<Button
								type="button"
								size="sm"
								variant="outline"
								disabled={busy}
								onClick={onForgetToken}
							>
								Forget token
							</Button>
						)}
					</div>
				</form>

				{status?.restartRequired === true ? (
					<div className="flex flex-col gap-2">
						<Text size="2xs">
							Cloudflare is ready. The console has to restart to serve the door, and
							this address stops answering when it does.
						</Text>
						<Text size="2xs" tone="muted" className="break-all">
							It comes back at https://{status.hostname}.
						</Text>
						<div className="flex items-center gap-2">
							<Button
								type="button"
								size="sm"
								disabled={restarting || restarted}
								onClick={onEnable}
							>
								{restarting
									? "Restarting…"
									: restarted
										? "Restarted"
										: "Turn it on"}
							</Button>
							{restarted ? (
								<Button asChild variant="link" size="sm">
									<a href={`https://${status.hostname ?? ""}`}>
										Open {status.hostname}
									</a>
								</Button>
							) : null}
						</div>
					</div>
				) : null}

				{status?.configured === true ? (
					<div className="flex flex-col gap-2">
						<div>
							<Button
								type="button"
								size="sm"
								variant="outline"
								disabled={checking}
								onClick={onCheck}
							>
								{checking ? "Checking…" : "Check the door"}
							</Button>
						</div>
						{check === undefined ? (
							<Text size="2xs" tone="muted">
								Fetches the hostname the way a browser with nobody signed in would,
								and looks for the login page and the keys behind it.
							</Text>
						) : (
							<>
								<Text size="2xs" tone={check.guarded ? "muted" : "default"}>
									{check.guarded ? "✅ " : "❌ "}
									{check.detail}
								</Text>
								{check.login === undefined ? null : (
									<Text size="2xs" tone="muted" className="break-all">
										login: {check.login}
									</Text>
								)}
							</>
						)}
					</div>
				) : null}

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
