import type { SettingsTab } from "./use-platform";

/**
 * The four things an install needs before it is useful, and what is left of each.
 *
 * This is a checklist rather than a wizard: nothing here blocks a conversation, and a person who wants to do
 * them out of order is not stopped. It exists because the order is not obvious the first time — a model needs a
 * provider, the people need a door or a network they share, and the console says none of that (ADR 0036 §7).
 *
 * The facts are the console's own: whether a provider is authenticated, whether the model picker has a value,
 * whether the door is configured and serving, and how many people the book holds.
 */
export interface SetupInput {
	/** At least one provider has a working key or sign-in. */
	hasProvider: boolean;
	/** A model is in force: the picker has a value, which is the install's default when nobody chose one. */
	hasModel: boolean;
	/** `remote.json` records a door. */
	remoteConfigured: boolean;
	/** The door is recorded but this process did not start with it: a restart is what turns it on. */
	remoteRestartRequired: boolean;
	/** Everybody in the book, the owner included. Absent until the book has been read. */
	memberCount?: number | undefined;
}

export interface SetupStep {
	id: "provider" | "models" | "remote" | "people";
	title: string;
	why: string;
	action: string;
	tab: SettingsTab;
	done: boolean;
}

/** The checklist, in the order the questions are asked, whichever of them are already answered. */
export function setupSteps(input: SetupInput): SetupStep[] {
	const remoteOn = input.remoteConfigured && !input.remoteRestartRequired;
	const steps: SetupStep[] = [
		{
			id: "provider",
			title: "Add a provider key",
			why: "The assistant runs a model, and a model comes from a provider this install has a key for.",
			action: "Add a key",
			tab: "providers",
			done: input.hasProvider,
		},
		{
			id: "models",
			title: "Choose the models this install may use",
			why: "A fresh install allows none until you tick them, and the default is what a new conversation starts from.",
			action: "Choose models",
			tab: "providers",
			done: input.hasModel,
		},
		{
			id: "remote",
			title: "Reach it from outside",
			why: "A tunnel and a sign-in that need no VPN; everybody you invite gets in with a code sent to their email.",
			action: input.remoteRestartRequired ? "Turn it on" : "Set it up",
			tab: "remote",
			done: remoteOn,
		},
	];

	// A step nobody can answer yet is not shown: a page that says "invite somebody" before it can know whether
	// anybody was invited is a page that nags about work already done.
	if (input.memberCount !== undefined) {
		steps.push({
			id: "people",
			title: "Invite somebody",
			why: "Add their address, then send them the link. They sign in with a code, and nothing else gets in.",
			action: "Add a person",
			tab: "people",
			done: input.memberCount > 1,
		});
	}
	return steps;
}

/** Every step answered: the screen stops saying anything rather than asking again. */
export function setupDone(steps: SetupStep[]): boolean {
	return steps.every((step) => step.done);
}
