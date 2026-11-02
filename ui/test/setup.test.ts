import { describe, expect, test } from "bun:test";
import { setupDone, setupSteps, type SetupInput } from "../src/app/setup";

/** A fresh install: no key, no model, no door, and only the owner in the book. */
const fresh: SetupInput = {
	hasProvider: false,
	hasModel: false,
	remoteConfigured: false,
	remoteRestartRequired: false,
	memberCount: 1,
};

describe("the Getting started checklist", () => {
	test("a fresh install is asked four things in the order the questions matter", () => {
		const steps = setupSteps(fresh);
		expect(steps.map((step) => step.id)).toEqual(["provider", "models", "remote", "people"]);
		expect(steps.every((step) => !step.done)).toBe(true);
	});

	test("each answer marks its own step, and only its own", () => {
		for (const [change, id] of [
			[{ hasProvider: true }, "provider"],
			[{ hasModel: true }, "models"],
			[{ remoteConfigured: true }, "remote"],
			[{ memberCount: 2 }, "people"],
		] as const) {
			const steps = setupSteps({ ...fresh, ...change });
			expect(steps.find((step) => step.id === id)?.done).toBe(true);
			expect(steps.filter((step) => step.done)).toHaveLength(1);
		}
	});

	test("a door that is applied but not serving is not done, and says what the press is", () => {
		const steps = setupSteps({ ...fresh, remoteConfigured: true, remoteRestartRequired: true });
		const remote = steps.find((step) => step.id === "remote");
		expect(remote?.done).toBe(false);
		expect(remote?.action).toBe("Turn it on");
	});

	test("the people step waits for the book to be read", () => {
		const steps = setupSteps({ ...fresh, memberCount: undefined });
		expect(steps.map((step) => step.id)).toEqual(["provider", "models", "remote"]);
	});

	test("everything answered is finished, and one left is not", () => {
		expect(
			setupDone(
				setupSteps({
					...fresh,
					hasProvider: true,
					hasModel: true,
					remoteConfigured: true,
					memberCount: 2,
				}),
			),
		).toBe(true);
		expect(setupDone(setupSteps({ ...fresh, hasProvider: true }))).toBe(false);
	});
});
