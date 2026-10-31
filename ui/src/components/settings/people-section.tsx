import { useState } from "react";
import type { ProviderCatalogEntry } from "@/app/client";
import type { PersonSummary } from "@/app/view-models";
import { Stack } from "@/components/layout/stack";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Code } from "@/components/typography/code";
import { Input } from "@/components/ui/input";
import { Text } from "@/components/typography/text";
import { PersonModelsDialog } from "./person-models-dialog";
import { SettingsField, SettingsGroup } from "./settings-group";

export interface PeopleSectionProps {
	/** The book as the admin's route answered it. Undefined until the first answer arrives. */
	people?: PersonSummary[] | undefined;
	/** Every provider and model this install has: a person's list is narrowed from all of them. */
	catalog?: ProviderCatalogEntry[] | undefined;
	/** The first read is in flight: the page says so rather than showing an empty book. */
	loading?: boolean;
	/** The last refusal, in the server's words. */
	failure?: string | undefined;
	onAdd: (input: { email: string; name: string }) => void;
	onRevoke: (email: string) => void;
	onRestore: (email: string) => void;
	onSetCanCreate: (email: string, canCreate: boolean) => void;
	onSetModels: (email: string, models: string[]) => void;
}

/** The badge a record's state wears: a state is not a role, and the two are never one word (ADR 0034 §2). */
function stateTone(state: PersonSummary["state"]): "success" | "warning" | "danger" {
	if (state === "active") return "success";
	if (state === "revoked") return "danger";
	return "warning";
}

/** What a person may run, in the page's words. Empty is every model, and the page says which. */
function modelsLine(person: PersonSummary): string {
	if (person.role === "admin") return "runs every model: the install is theirs";
	if (person.models.length === 0) return "may run every model";
	if (person.models.length === 1) return `may run 1 model: ${person.models[0] ?? ""}`;
	return `may run ${person.models.length} models`;
}

/** When somebody was last here, or that they have never been: a date, because the book keeps dates. */
function seenLine(person: PersonSummary): string {
	if (person.lastSeen !== undefined && person.lastSeen !== "")
		return `last seen ${person.lastSeen}`;
	if (person.state === "invited") return "has not signed in yet";
	return "";
}

/**
 * The book of people: who may use this install, and what they may do on it.
 *
 * It is the admin's page and nobody else's — a person manages rooms, never the install (ADR 0034 §1) — and
 * the tab that holds it is drawn only for the admin, so this component is never rendered for a member.
 *
 * The admin's own row has no controls: they run every model by virtue of owning the install, and the book
 * refuses to revoke the one address that could add anybody back. Offering a switch that changes nothing
 * would be a second, quieter lie about what a role means.
 */
export function PeopleSection({
	people,
	catalog = [],
	loading = false,
	failure,
	onAdd,
	onRevoke,
	onRestore,
	onSetCanCreate,
	onSetModels,
}: PeopleSectionProps) {
	const [email, setEmail] = useState("");
	const [name, setName] = useState("");
	/** The person whose model list is open, if any. */
	const [editing, setEditing] = useState<PersonSummary | undefined>(undefined);

	const add = (): void => {
		const address = email.trim();
		if (address === "") return;
		onAdd({ email: address, name: name.trim() });
		// The field empties whether or not it landed: a refusal is said under it, and a field that keeps
		// the words invites a second press of the same mistake.
		setEmail("");
		setName("");
	};

	return (
		<SettingsGroup
			slot="people-section"
			title="People"
			intro={
				<Text size="2xs" tone="muted">
					Who may use this install. Everybody here gets a room of their own, and the rooms
					they were invited to. A guest is invited from the room, not from here.
				</Text>
			}
		>
			{failure === undefined ? null : (
				<Text size="2xs" className="text-destructive-subtle-foreground">
					{failure}
				</Text>
			)}

			{loading && people === undefined ? (
				<Text size="sm" tone="muted">
					Reading the book…
				</Text>
			) : people === undefined ? null : people.length === 0 ? (
				// An install with no book, and one whose book names nobody, are the same question: there is
				// no admin yet, and the command line is where one is claimed. The gateway's own sentence,
				// with the command in it.
				<Stack gap="2xs">
					<Text size="sm" tone="muted">
						This install has no admin yet. Claim it from the machine that runs it, and
						then everybody else can be named here:
					</Text>
					<Code>acornd people claim you@example.com</Code>
				</Stack>
			) : (
				<div className="flex flex-col gap-1">
					{people.map((person) => (
						<div
							key={person.email}
							data-slot="person-row"
							data-email={person.email}
							data-state={person.state}
							className="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-md border border-border bg-card px-3 py-2"
						>
							<div className="flex min-w-0 flex-1 flex-col">
								<Text as="span" size="sm" weight="medium" className="truncate">
									{person.name === undefined || person.name === ""
										? person.email
										: person.name}
								</Text>
								<Text
									as="span"
									size="2xs"
									tone="muted"
									className="truncate font-mono"
								>
									{person.email}
								</Text>
								<Text as="span" size="2xs" tone="muted">
									{[modelsLine(person), seenLine(person)]
										.filter((part) => part !== "")
										.join(" · ")}
								</Text>
							</div>
							<div className="flex shrink-0 flex-wrap items-center gap-2">
								<Badge
									variant="outline"
									tone={person.role === "admin" ? "primary" : "neutral"}
								>
									{person.role}
								</Badge>
								<Badge variant="soft" tone={stateTone(person.state)}>
									{person.state}
								</Badge>
								{person.role === "admin" ? null : (
									<>
										{/* The install's permission to lead a room, on by default: without it a
										 * member may be a guest and nothing more (ADR 0034 §3). A revoked person
										 * leads nothing, so the switch waits until they are back. */}
										{person.state === "revoked" ? null : (
											<label className="flex items-center gap-1.5">
												<input
													type="checkbox"
													checked={person.canCreate}
													onChange={(event) =>
														onSetCanCreate(
															person.email,
															event.target.checked,
														)
													}
												/>
												<Text size="2xs" tone="muted">
													can make rooms
												</Text>
											</label>
										)}
										{person.state === "revoked" ? (
											<Button
												type="button"
												variant="outline"
												size="sm"
												aria-label={`Restore ${person.email}`}
												onClick={() => onRestore(person.email)}
											>
												Restore
											</Button>
										) : (
											<>
												<Button
													type="button"
													variant="outline"
													size="sm"
													aria-label={`Models for ${person.email}`}
													onClick={() => setEditing(person)}
												>
													Models…
												</Button>
												{/* Revoking is one press because coming back is one press: the row
												 * turns to `revoked` and grows a Restore, and nothing was deleted
												 * (ADR 0034 §1). */}
												<Button
													type="button"
													variant="ghost"
													size="sm"
													aria-label={`Revoke ${person.email}`}
													onClick={() => onRevoke(person.email)}
												>
													Revoke
												</Button>
											</>
										)}
									</>
								)}
							</div>
						</div>
					))}
				</div>
			)}

			{/* The book's own way in: an address and the name to read it under. A record is invited
			 * until that address signs in, so nothing is sent from here — the admin tells them. */}
			<SettingsField label="add somebody">
				<div className="flex flex-wrap items-center gap-2">
					<Input
						inputSize="sm"
						type="email"
						value={email}
						placeholder="ana@example.com"
						aria-label="Address to add"
						onChange={(event) => setEmail(event.target.value)}
						onKeyDown={(event) => {
							if (event.key !== "Enter") return;
							event.preventDefault();
							add();
						}}
					/>
					<Input
						inputSize="sm"
						value={name}
						placeholder="Ana (optional)"
						aria-label="Name to read the address under"
						onChange={(event) => setName(event.target.value)}
						onKeyDown={(event) => {
							if (event.key !== "Enter") return;
							event.preventDefault();
							add();
						}}
					/>
					<Button
						type="button"
						size="sm"
						variant="outline"
						disabled={email.trim() === ""}
						onClick={add}
					>
						Add person
					</Button>
				</div>
				<Text size="2xs" tone="muted">
					They can sign in once they are here, and make a room of their own unless you say
					otherwise.
				</Text>
			</SettingsField>

			{editing === undefined ? null : (
				<PersonModelsDialog
					key={editing.email}
					person={editing}
					catalog={catalog}
					onSave={(models) => {
						onSetModels(editing.email, models);
						setEditing(undefined);
					}}
					onClose={() => setEditing(undefined)}
				/>
			)}
		</SettingsGroup>
	);
}
