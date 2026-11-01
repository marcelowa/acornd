import type { SettingsTab } from "@/app/use-platform";
import { setupDone, setupSteps, type SetupInput } from "@/app/setup";
import { Button } from "@/components/ui/button";
import { Text } from "@/components/typography/text";
import { SettingsGroup } from "./settings-group";

export interface SetupChecklistProps extends SetupInput {
	/** Take the person to the page that answers a step. */
	onOpen: (tab: SettingsTab) => void;
}

/**
 * The four questions a new install is asked, answered or not, on the page a person lands on when they go looking
 * for settings. It never blocks anything: a step not done is a button, and a step done is a line that says so
 * (ADR 0036 §7). When every one is answered the group disappears, because a checklist that stays finished is a
 * screen nobody reads again.
 */
export function SetupChecklist({ onOpen, ...input }: SetupChecklistProps) {
	const steps = setupSteps(input);
	if (setupDone(steps)) return null;
	return (
		<SettingsGroup
			slot="settings-setup"
			title="Getting started"
			intro={
				<Text size="2xs" tone="muted">
					Four things this install needs before it is useful. None of them is a
					prerequisite for another, and anything already done says so.
				</Text>
			}
		>
			<ul className="flex flex-col gap-4">
				{steps.map((step) => (
					<li key={step.id} className="flex items-start justify-between gap-3">
						<div className="flex min-w-0 flex-col gap-1">
							<Text size="2xs" tone={step.done ? "muted" : "default"}>
								{step.done ? "✓ " : ""}
								{step.title}
							</Text>
							<Text size="2xs" tone="muted">
								{step.why}
							</Text>
						</div>
						{step.done ? null : (
							<Button
								type="button"
								size="sm"
								variant="outline"
								className="shrink-0"
								onClick={() => onOpen(step.tab)}
							>
								{step.action}
							</Button>
						)}
					</li>
				))}
			</ul>
		</SettingsGroup>
	);
}
