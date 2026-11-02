import { useCallback, useEffect, useRef, useState } from "react";
import type { DoorReport, RemotePreview, RemoteStatus, RemoteStep } from "@/app/client";
import { completeHostname, doorPublished, tunnelLine } from "@/app/remote";
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
	/** Ask the gateway what its connector is doing now. The screen polls it while the door is opening. */
	onRefresh?: (() => void) | undefined;
}

/** One step as a person reads it: the verb, the piece, and the detail when there is one. */
function stepLine(step: RemoteStep): string {
	const verb = step.created ? "created" : step.moved ? "pointed" : "kept";
	return step.detail === undefined
		? `${verb} ${step.name}`
		: `${verb} ${step.name} · ${step.detail}`;
}

/** How one step reads before it is done: a tick, a dot for the one worth doing now, a circle for the rest. */
function mark(done: boolean, current: boolean): string {
	if (done) return "✓";
	return current ? "●" : "○";
}

/**
 * The door, from inside the console, as the four steps it is: connect Cloudflare, choose the address, make the
 * door, open it. Every step is on screen from the start and disabled until its turn, so the order is the page and
 * not something a person has to infer (ADR 0036 §6).
 *
 * Opening the door is automatic: Cloudflare takes a moment to publish the Access application, so after an apply
 * the screen asks the public hostname for the application's audience and waits until it is published, then
 * restarts the console itself. The panel says what will happen to this address before it does.
 */
export function RemoteSection({
	status,
	preview,
	previewBusy = false,
	failure,
	busy = false,
	check,
	checking = false,
	restarting = false,
	restarted = false,
	onApply,
	onPreview,
	onForgetToken,
	onEnable,
	onCheck,
	onRefresh,
}: RemoteSectionProps) {
	const [hostname, setHostname] = useState(status?.hostname ?? "");
	const [zone, setZone] = useState("");
	const [token, setToken] = useState("");
	const [polls, setPolls] = useState(0);
	const [opening, setOpening] = useState(false);
	const zones = preview?.zones ?? [];
	const teamDomain = preview?.teamDomain ?? status?.teamDomain ?? "";
	const target = completeHostname(hostname, zone);
	const tokenReady = token.trim() !== "" || status?.tokenStored === true;
	const ready = target !== "" && zone !== "" && tokenReady;
	const steps = status?.steps ?? [];
	const configured = status?.configured === true;
	const needsOpening = configured && status?.restartRequired === true && !restarted;
	const published = doorPublished(check?.login, status?.audience);
	const tunnel = tunnelLine(status?.tunnel, status?.tunnelDetail);

	// One zone is not a choice: it is the answer, and a picker with one option is a step for no reason. The
	// name rather than the array, because the answer is rebuilt on every render and the effect is not.
	const onlyZone = preview?.zones.length === 1 ? preview.zones[0]?.name : undefined;

	useEffect(() => {
		if (zone === "" && onlyZone !== undefined) setZone(onlyZone);
	}, [onlyZone, zone]);

	// While the door is waiting to open, ask two questions on a loop: has Cloudflare published the application
	// (the check), and what is this process's connector doing (the status). Both answers are what the steps
	// below show, so a wait is visible rather than a screen that looks finished when it is not.
	useEffect(() => {
		if (!needsOpening) {
			setPolls(0);
			return;
		}
		const timer = window.setInterval(() => {
			onRefresh?.();
			onCheck?.();
			setPolls((count) => count + 1);
		}, 3000);
		return () => window.clearInterval(timer);
	}, [needsOpening, onRefresh, onCheck]);

	// Once the application is published, open the door: the restart is the last thing the apply needed, and a
	// button for it left the person to guess when Cloudflare had caught up.
	const opened = useRef(false);
	const openDoor = useCallback(() => {
		if (opening) return;
		setOpening(true);
		onEnable?.();
	}, [opening, onEnable]);
	useEffect(() => {
		if (!needsOpening) {
			opened.current = false;
			setOpening(false);
			return;
		}
		if (opened.current || !published) return;
		opened.current = true;
		openDoor();
	}, [needsOpening, published, openDoor]);

	const done = {
		connect: preview !== undefined,
		address: zone !== "" && target !== "",
		door: configured,
		open: configured && !needsOpening && status?.restartRequired !== true,
	};
	const current = !done.connect
		? "connect"
		: !done.address
			? "address"
			: !done.door
				? "door"
				: !done.open
					? "open"
					: "";

	return (
		<SettingsGroup
			slot="settings-remote"
			title="Remote access"
			intro={
				<Text size="2xs" tone="muted">
					{configured
						? `A browser reaches this install at ${status.hostname}. Everybody the book admits gets in by email; nothing else does.`
						: "Nobody can reach this install from outside yet. A Cloudflare account, a domain, and one API token are what it takes."}
				</Text>
			}
		>
			<div className="flex flex-col gap-4">
				{configured ? (
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
								Tunnel
							</Text>
						</dt>
						<dd>
							<Text
								size="2xs"
								tone={status.tunnel === "connected" ? "muted" : "default"}
							>
								{tunnel === "" ? "not running" : tunnel}
							</Text>
						</dd>
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
					</dl>
				) : null}

				<form
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
					<ol className="flex flex-col gap-4">
						<li className="flex flex-col gap-2">
							<Text size="2xs" tone={done.connect ? "muted" : "default"}>
								{mark(done.connect, current === "connect")} 1. Connect your
								Cloudflare account
							</Text>
							<Input
								name="token"
								type="password"
								value={token}
								placeholder={
									status?.tokenStored === true
										? "Paste a new one to replace the stored token"
										: "Paste the API token"
								}
								autoComplete="off"
								onChange={(event) => setToken(event.target.value)}
							/>
							<div className="flex flex-wrap items-center gap-2">
								<Button
									type="button"
									size="sm"
									variant="outline"
									disabled={previewBusy || !tokenReady}
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
						</li>

						<li className="flex flex-col gap-2">
							<Text size="2xs" tone={done.address ? "muted" : "default"}>
								{mark(done.address, current === "address")} 2. Choose the address
							</Text>
							<select
								name="zone"
								value={zone}
								className={cn(inputVariants())}
								disabled={preview === undefined}
								onChange={(event) => setZone(event.target.value)}
							>
								<option value="">
									{preview === undefined
										? "Check the token first"
										: "Choose the zone"}
								</option>
								{zones.map((one) => (
									<option key={one.id} value={one.name}>
										{one.name}
									</option>
								))}
							</select>
							<Input
								name="hostname"
								value={hostname}
								placeholder={zone === "" ? "console" : `console.${zone}`}
								autoComplete="off"
								disabled={zone === ""}
								onChange={(event) => setHostname(event.target.value)}
							/>
							<Text size="2xs" tone="muted" className="break-all">
								{target === ""
									? "A label such as console is completed with the zone; a whole name has to end with it."
									: `Answers at ${target}`}
							</Text>
						</li>

						<li className="flex flex-col gap-2">
							<Text size="2xs" tone={done.door ? "muted" : "default"}>
								{mark(done.door, current === "door")} 3. Make the door
							</Text>
							<div className="flex flex-wrap items-center gap-2">
								<Button type="submit" size="sm" disabled={busy || !ready}>
									{busy
										? "Making it…"
										: configured
											? "Apply again"
											: "Make the door"}
								</Button>
								{status?.tokenStored !== true ||
								onForgetToken === undefined ? null : (
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
						</li>

						<li className="flex flex-col gap-2">
							<Text size="2xs" tone={done.open ? "muted" : "default"}>
								{mark(done.open, current === "open")} 4. Open it
							</Text>
							{!configured ? (
								<>
									<Button type="button" size="sm" disabled>
										Waiting for the door to be made
									</Button>
									<Text size="2xs" tone="muted">
										The console restarts itself once Cloudflare has the door.
									</Text>
								</>
							) : needsOpening ? (
								<>
									<Text size="2xs">
										{published
											? "Cloudflare has published the application. Opening the door…"
											: "Cloudflare is publishing the application. This usually takes a few seconds after the first apply."}
									</Text>
									<Text size="2xs" tone="muted" className="break-all">
										This address stops answering when the console restarts; it
										comes back at https://{status.hostname}.
									</Text>
									{restarting ? (
										<Text size="2xs" tone="muted">
											Restarting the console…
										</Text>
									) : null}
									<div className="flex flex-wrap items-center gap-2">
										<Button
											type="button"
											size="sm"
											disabled={
												opening ||
												restarting ||
												restarted ||
												(!published && polls < 10)
											}
											onClick={openDoor}
										>
											{opening || restarting
												? "Opening…"
												: published
													? "Open the door"
													: polls < 10
														? "Waiting for Cloudflare…"
														: "Open it anyway"}
										</Button>
										{polls >= 10 && !published ? (
											<Button
												type="button"
												size="sm"
												variant="outline"
												disabled={checking}
												onClick={onCheck}
											>
												{checking ? "Checking…" : "Check again"}
											</Button>
										) : null}
									</div>
								</>
							) : (
								<>
									<Text size="2xs">
										The console serves the door
										{tunnel === "" ? "." : `, and ${tunnel}.`}
									</Text>
									{restarted ? (
										<Button asChild variant="link" size="sm">
											<a href={`https://${status.hostname ?? ""}`}>
												Open {status.hostname}
											</a>
										</Button>
									) : null}
								</>
							)}
						</li>
						<li className="flex flex-col gap-2">
							<Text size="2xs" tone={check?.guarded === true ? "muted" : "default"}>
								{mark(check?.guarded === true, false)} 5. Check it from outside
							</Text>
							<div>
								<Button
									type="button"
									size="sm"
									variant="outline"
									disabled={!configured || checking}
									onClick={onCheck}
								>
									{checking ? "Checking…" : "Check the door"}
								</Button>
							</div>
							{check === undefined ? (
								<Text size="2xs" tone="muted">
									Fetches the hostname the way a browser with nobody signed in
									would, and looks for the login page and the keys behind it.
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
						</li>
					</ol>
				</form>

				{configured ? (
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
			</div>
		</SettingsGroup>
	);
}
