import { useState } from "react";
import type { ProviderCatalogEntry } from "@/app/client";
import type { PersonSummary } from "@/app/view-models";
import { Button } from "@/components/ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
} from "@/components/ui/dialog";
import { Text } from "@/components/typography/text";

export interface PersonModelsDialogProps {
	/** The person whose list this is: what they may run, as `provider/modelId`. */
	person: PersonSummary;
	/**
	 * Every provider and every model this install has. The list is narrowed from all of them, never from
	 * what is left — choosing from the narrowed list would make it impossible to widen again.
	 */
	catalog: ProviderCatalogEntry[];
	onSave: (models: string[]) => void;
	onClose: () => void;
}

/**
 * What one person may run.
 *
 * An empty list is every model, which is what the book means by no list: everything starts ticked, and
 * saving with everything ticked puts the record back to "no list at all". The list governs *choosing* a
 * model, not speaking in a room somebody else set up — a guest runs the model the room was given (ADR 0034
 * §4) — so this is a limit on what they may pin, and the sentence above the list says so.
 */
export function PersonModelsDialog({ person, catalog, onSave, onClose }: PersonModelsDialogProps) {
	const narrowed = person.models.length > 0;
	const every = catalog.flatMap((provider) =>
		provider.models.map((model) => `${provider.id}/${model.id}`),
	);
	const [chosen, setChosen] = useState<Set<string>>(
		() => new Set(narrowed ? person.models : every),
	);
	const toggle = (value: string): void => {
		setChosen((current) => {
			const next = new Set(current);
			if (next.has(value)) next.delete(value);
			else next.add(value);
			return next;
		});
	};
	const saving = every.filter((value) => chosen.has(value));

	return (
		<Dialog open onOpenChange={(open) => (open ? undefined : onClose())}>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>{person.name ?? person.email}: models they may choose</DialogTitle>
					<DialogDescription>
						{saving.length} of {every.length} models allowed. Everything allowed is the
						same as no limit at all.
					</DialogDescription>
				</DialogHeader>
				<div className="flex max-h-80 flex-col gap-2 overflow-y-auto">
					{catalog.length === 0 ? (
						<Text size="sm" tone="muted">
							This install has no models to choose from yet.
						</Text>
					) : null}
					{catalog.map((provider) => (
						<div key={provider.id} className="flex flex-col gap-1">
							<Text size="2xs" weight="medium" tone="muted">
								{provider.name}
							</Text>
							{provider.models.map((model) => {
								const value = `${provider.id}/${model.id}`;
								return (
									<label key={value} className="flex items-center gap-2">
										<input
											type="checkbox"
											checked={chosen.has(value)}
											onChange={() => toggle(value)}
										/>
										<Text size="2xs">{model.name}</Text>
										<Text size="2xs" tone="muted">
											{model.id}
										</Text>
									</label>
								);
							})}
						</div>
					))}
				</div>
				<DialogFooter>
					<Button type="button" size="sm" variant="ghost" onClick={onClose}>
						Cancel
					</Button>
					<Button
						type="button"
						size="sm"
						onClick={() => onSave(saving.length === every.length ? [] : saving)}
					>
						Save
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
