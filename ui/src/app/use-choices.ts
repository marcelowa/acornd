import { useCallback, useMemo } from "react";
import {
	setAgent,
	setDefaultAgent,
	setDefaultModel,
	setDefaultThinking,
	type AgentChange,
	type DefaultScope,
} from "./client";
import {
	contextWindowFor,
	stalePins,
	toAgentGroups,
	toModelChoices,
	toModelGroups,
	toScopeDefaults,
	toThinkingGroups,
} from "./map";
import type { Session } from "./use-session";
import type { ChoicePicker, PickerGroup } from "./view-models";
import type { WireResolvedAgent } from "./wire";

/** What runs: the conversation's model, thinking and agent, and the defaults a new one starts from. */
export interface ChoicesSlice {
	/** The conversation in view was created by another one, so its choices are not its own. */
	subagent: boolean;
	/** Model, thinking, and agent as the conversation resolves them, with their layer. */
	model: ChoicePicker;
	thinking: ChoicePicker;
	agent: ChoicePicker;
	/** The running model's window, when the providers name it. */
	contextWindow?: number | undefined;
	resolved?: WireResolvedAgent | undefined;
	/** The level that was asked for when the model could not express it. */
	thinkingRequested?: string | undefined;
	/** What each scope says, before resolution: one tab per scope in the settings screen. */
	defaults: {
		workspace: { model: string; thinking: string; agent: string };
		global: { model: string; thinking: string; agent: string };
	};
	/** Set a default, in this workspace or everywhere. */
	setDefault: (kind: "model" | "thinking" | "agent", scope: DefaultScope, value: string) => void;
	/** The name a `provider/modelId` is shown under, when the providers know it. */
	modelLabel: (value: string) => string;
	/** The levels a model offers, for the thinking default that follows it. */
	thinkingFor: (model: string) => PickerGroup[];
}

/** Every picker can go back to inheriting: a choice has to be undoable. */
function withDefault(groups: PickerGroup[]): PickerGroup[] {
	return [...groups, { options: [{ value: "", label: "Use the default" }] }];
}

export function useChoices(session: Session): ChoicesSlice {
	const { state, workspace, activeId, reportError, clearFailure, act, reload, setState } =
		session;
	const { can } = session.slice;

	// How a turn lands while one is running: the worker has to offer steering at all, and then
	// the choice is the person's, not a heuristic.
	const choose = useCallback(
		(change: AgentChange) => {
			void setAgent(workspace, activeId, change)
				.then((next) => {
					setState(next);
					clearFailure();
				})
				.catch(reportError);
		},
		[workspace, activeId, reportError, setState, clearFailure],
	);

	// The composer's three choices. `state.agent` is the effective binding, so a picker shows
	// what will actually run; `inherited` says whether this conversation chose it, and
	// `defaultsOrigin` names the layer when it did not.
	const modelChoices = useMemo(() => toModelChoices(state?.providers ?? []), [state]);
	const effectiveModel =
		state === undefined ? "" : `${state.agent.provider}/${state.agent.modelId}`;
	const contextWindow = useMemo(
		() => contextWindowFor(state?.providers ?? [], effectiveModel),
		[state, effectiveModel],
	);
	const thinkingLevels = useMemo(
		() => modelChoices.find((choice) => choice.value === effectiveModel)?.thinkingLevels ?? [],
		[modelChoices, effectiveModel],
	);
	const thinkingValue = state?.agent.thinkingLevel ?? "";
	const agentValue =
		state?.resolved?.name ??
		(state?.agent.definition !== undefined && state.agent.definition !== ""
			? state.agent.definition
			: (state?.defaultAgent ?? ""));

	// A pin can outlive what it names; the value is still shown — it is what the conversation
	// is pinned to — and it is marked when it cannot run here any more.
	const stale = state === undefined ? { model: false, agent: false } : stalePins(state);
	// A subagent runs what its creator gave it: the pickers show that rather than offering a
	// change the gateway would not apply.
	const subagent = state?.subagent === true;
	const fixed = activeId === "" || subagent || !can.model;
	const pinnedModel = modelChoices.find((choice) => choice.value === effectiveModel);

	const model = useMemo<ChoicePicker>(
		() => ({
			value: pinnedModel?.label ?? state?.agent.modelId ?? "",
			invalid: stale.model,
			note: stale.model ? "not available here" : undefined,
			groups: withDefault(toModelGroups(state?.providers ?? [])),
			disabled: fixed || modelChoices.length === 0,
			onSelect: (value) => {
				if (value === "") {
					choose({ provider: "", modelId: "" });
					return;
				}
				const slash = value.indexOf("/");
				choose({ provider: value.slice(0, slash), modelId: value.slice(slash + 1) });
			},
		}),
		[pinnedModel, state, stale.model, fixed, modelChoices.length, choose],
	);

	const thinking = useMemo<ChoicePicker>(
		() => ({
			value: thinkingValue,
			note:
				state !== undefined &&
				(state.agent.thinkingRequested ?? "") !== "" &&
				state.agent.thinkingRequested !== thinkingValue
					? `${state.agent.thinkingRequested ?? ""} was asked`
					: undefined,
			groups: withDefault(toThinkingGroups(thinkingLevels)),
			disabled: fixed || (thinkingLevels.length === 0 && thinkingValue === ""),
			onSelect: (value) => choose({ thinkingLevel: value }),
		}),
		[thinkingValue, state, thinkingLevels, fixed, choose],
	);

	const agent = useMemo<ChoicePicker>(
		() => ({
			value: agentValue,
			invalid: stale.agent,
			note: stale.agent ? "definition is gone" : subagent ? "set by its creator" : undefined,
			groups: withDefault(toAgentGroups(state?.agents ?? [])),
			disabled: fixed,
			onSelect: (value) => choose({ definition: value }),
		}),
		[agentValue, stale.agent, subagent, state, fixed, choose],
	);

	const setDefault = useCallback(
		(kind: "model" | "thinking" | "agent", scope: DefaultScope, value: string) => {
			const request =
				kind === "model"
					? setDefaultModel(workspace, scope, value)
					: kind === "thinking"
						? setDefaultThinking(workspace, scope, value)
						: setDefaultAgent(workspace, scope, value);
			act(request, () => void reload());
		},
		[act, workspace, reload],
	);

	const defaults = useMemo(
		() => ({
			workspace: toScopeDefaults(state?.workspaceDefaults),
			global: toScopeDefaults(state?.installDefaults),
		}),
		[state],
	);

	const modelLabel = useCallback(
		(value: string): string =>
			modelChoices.find((choice) => choice.value === value)?.label ??
			value.split("/").at(-1) ??
			"",
		[modelChoices],
	);

	const thinkingFor = useCallback(
		(model: string): PickerGroup[] =>
			toThinkingGroups(
				modelChoices.find((choice) => choice.value === model)?.thinkingLevels ?? [],
			),
		[modelChoices],
	);

	return {
		subagent,
		model,
		thinking,
		agent,
		contextWindow,
		resolved: state?.resolved,
		thinkingRequested: state?.agent.thinkingRequested,
		defaults,
		setDefault,
		modelLabel,
		thinkingFor,
	};
}
