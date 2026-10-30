import { useEffect, useState } from "react";
import type { ShareView } from "@/app/view-models";
import { Badge } from "@/components/ui/badge";
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
import { Text } from "@/components/typography/text";

export interface ShareDialogProps {
	/** The room whose members this is, when the dialog is open. A personal room never opens it. */
	room?: ShareView | undefined;
	/**
	 * The last thing the server refused, in its own words. An address the book does not hold is a 400 whose
	 * sentence says to ask the admin, and that sentence is what the person acts on.
	 */
	failure?: string | undefined;
	onAdd: (workspace: string, email: string) => void;
	onRemove: (workspace: string, email: string) => void;
	onClose: () => void;
}

/**
 * Who is in a room, and — for the person it belongs to — the way in.
 *
 * It opens from the room's own row on the workspaces list, which is where a workspace is named, and it is
 * offered on the room in view alone: a row that is not the room somebody is sitting in has no members this
 * page can answer for, and `state.workspace.members` is the room in view.
 *
 * The owner invites and removes; everybody else reads the list. There is no transfer of ownership and no
 * removal of the owner, so neither is offered: the API refuses both, and a control that only exists to be
 * refused is a lie (ADR 0034 §3, §6).
 */
export function ShareDialog({ room, failure, onAdd, onRemove, onClose }: ShareDialogProps) {
	const [email, setEmail] = useState("");
	// The dialog belongs to the room whose menu opened it: an address typed for one is not an address
	// typed for the next, and the refusal belongs to the attempt that was refused.
	useEffect(() => {
		setEmail("");
	}, [room?.name]);

	const administer = room?.administer === true;
	const add = (): void => {
		const address = email.trim();
		if (room === undefined || address === "") return;
		onAdd(room.name, address);
		// The address leaves the field whether or not it landed: a refusal is said under it, and a
		// field that keeps the words invites a second press of the same mistake.
		setEmail("");
	};

	return (
		<Dialog
			open={room !== undefined}
			onOpenChange={(next) => {
				if (!next) onClose();
			}}
		>
			<DialogContent className="sm:max-w-md">
				<DialogHeader>
					<DialogTitle>Share {room?.title ?? "this room"}</DialogTitle>
					<DialogDescription>
						{administer
							? "Anyone you add speaks in this room's conversations. They cannot make, rename or delete one."
							: "The room's owner adds and removes guests."}
					</DialogDescription>
				</DialogHeader>
				<div className="flex flex-col gap-2" data-slot="share-members">
					{(room?.members ?? []).map((member) => (
						<div
							key={member.email}
							data-slot="share-member"
							data-email={member.email}
							className="flex items-center gap-2 rounded-md border border-border bg-card px-2 py-1.5"
						>
							<Text as="span" size="sm" className="min-w-0 flex-1 truncate font-mono">
								{member.email}
							</Text>
							<Badge
								variant="outline"
								tone={member.role === "owner" ? "primary" : "neutral"}
							>
								{member.role}
							</Badge>
							{/* The owner never leaves, and the API refuses it: only a guest is offered the
							 * way out, and only the room's owner may take it. */}
							{administer && member.role === "guest" ? (
								<Button
									type="button"
									variant="ghost"
									size="sm"
									aria-label={`Remove ${member.email}`}
									onClick={() => onRemove(room.name, member.email)}
								>
									Remove
								</Button>
							) : null}
						</div>
					))}
					{room !== undefined && room.members.length <= 1 ? (
						<Text size="sm" tone="muted">
							Only you, so far.
						</Text>
					) : null}
				</div>
				{administer ? (
					<div className="flex flex-col gap-1">
						<div className="flex flex-wrap items-center gap-2">
							<Input
								inputSize="sm"
								type="email"
								value={email}
								placeholder="ana@example.com"
								aria-label="Address to add to this room"
								onChange={(event) => setEmail(event.target.value)}
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
								Add guest
							</Button>
						</div>
						{/* The book decides who may exist; a room only decides who may enter (ADR 0034 §1). The
						 * sentence is here so the 400 that says the same is a reminder rather than a surprise. */}
						<Text size="2xs" tone="muted">
							They have to be in People first.
						</Text>
						{failure === undefined ? null : (
							<Text size="2xs" className="text-destructive-subtle-foreground">
								{failure}
							</Text>
						)}
					</div>
				) : null}
				<DialogFooter>
					<Button type="button" variant="outline" onClick={onClose}>
						Close
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
