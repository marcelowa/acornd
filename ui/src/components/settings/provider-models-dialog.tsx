import { useState } from "react";
import type { ProviderCatalogEntry } from "@/app/client";
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

export interface ProviderModelsDialogProps {
	provider: ProviderCatalogEntry;
	/** What this install may use from it; empty means none of them. */
	allowed: string[];
	onSave: (models: string[]) => void;
	onClose: () => void;
}

/**
 * What one provider's key may be spent on.
 *
 * The list belongs to the connection, not to a person: one credentials file is spent by everyone, so "what may this
 * key buy" is a property of the key. Nothing starts ticked: an install with no list allows no model, and the admin
 * ticks what the key may buy. Saving with nothing ticked allows nothing, which is a state the admin may mean.
 */
export function ProviderModelsDialog({
	provider,
	allowed,
	onSave,
	onClose,
}: ProviderModelsDialogProps) {
	const [chosen, setChosen] = useState<Set<string>>(() => new Set(allowed));
	const toggle = (id: string): void => {
		setChosen((current) => {
			const next = new Set(current);
			if (next.has(id)) next.delete(id);
			else next.add(id);
			return next;
		});
	};
	const saving = provider.models.filter((one) => chosen.has(one.id)).map((one) => one.id);
	return (
		<Dialog open onOpenChange={(open) => (open ? undefined : onClose())}>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>{provider.name}: models this install may use</DialogTitle>
					<DialogDescription>
						{saving.length} of {provider.models.length} models allowed. Nothing is
						allowed until you tick it.
					</DialogDescription>
				</DialogHeader>
				<div className="max-h-80 overflow-y-auto">
					<ul className="flex flex-col gap-1">
						{provider.models.map((model) => (
							<li key={model.id}>
								<label className="flex items-center gap-2">
									<input
										type="checkbox"
										checked={chosen.has(model.id)}
										onChange={() => toggle(model.id)}
									/>
									<Text size="2xs">{model.name}</Text>
									<Text size="2xs" tone="muted">
										{model.id}
									</Text>
								</label>
							</li>
						))}
					</ul>
				</div>
				<DialogFooter>
					<Button
						type="button"
						size="sm"
						variant="outline"
						onClick={() => setChosen(new Set(provider.models.map((one) => one.id)))}
					>
						Select all
					</Button>
					<Button
						type="button"
						size="sm"
						variant="outline"
						onClick={() => setChosen(new Set())}
					>
						Clear
					</Button>
					<Button type="button" size="sm" variant="ghost" onClick={onClose}>
						Cancel
					</Button>
					<Button type="button" size="sm" onClick={() => onSave(saving)}>
						Save
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
