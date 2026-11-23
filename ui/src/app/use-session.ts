import {
	useCallback,
	useRef,
	useEffect,
	useState,
	type Dispatch,
	type MutableRefObject,
	type SetStateAction,
} from "react";
import { fetchState } from "./client";
import { toRights, toRoomRole } from "./map";
import type { Route } from "./use-navigation";
import type { RoomRights, RoomRole } from "./view-models";
import type { WireState } from "./wire";

/** The workspace in view as the gateway serves it, and what this caller may do in it. */
export interface SessionSlice {
	ready: boolean;
	/** The stream is following, or trying to be. */
	connection: "live" | "reconnecting";
	/** The last action the server refused, in its own words, until it is dismissed or a later
	 * action succeeds. */
	failure?: string | undefined;
	clearFailure: () => void;
	/** Ask the server for the state again; a success clears the last refusal. */
	reload: () => void;
	/** The workspace in view is opening, with the step it is on, or undefined when it is up. */
	opening?: { stage: string } | undefined;
	workerUp: boolean;
	workerError?: string | undefined;
	/** What this caller may change in the room in view: the state's one table, not a guess from a role. */
	can: RoomRights;
	/** What this caller is in the room in view; undefined when they are not in it. */
	role?: RoomRole | undefined;
	/** The install's role, not the room's: it is what shows the People page (ADR 0034 §1). */
	admin: boolean;
}

/**
 * The session with one workspace, for the hooks that act in it: the state the gateway last sent,
 * which workspace and conversation that is, and the one failure strip every action reports to.
 */
export interface Session {
	slice: SessionSlice;
	state: WireState | undefined;
	/** The workspace in view: the route's, or the state's when the route names none. */
	workspace: string;
	/** The conversation in view: the route's, or the state's when the route names none. */
	activeId: string;
	/** The view frame does not say which conversation it belongs to, so a frame handler reads the
	 * one the stream is following from here. */
	activeIdRef: MutableRefObject<string | undefined>;
	reportError: (cause: unknown) => void;
	clearFailure: () => void;
	/** Every command goes through here: a success clears the last refusal, a failure is it. */
	act: (request: Promise<unknown>, then?: () => void) => void;
	/** Read the state again. A retry is a decision to wait, so it drops the opening step. */
	reload: () => Promise<void>;
	/** The stream and the presses that answer with a whole state write it here. */
	setState: Dispatch<SetStateAction<WireState | undefined>>;
	setReady: Dispatch<SetStateAction<boolean>>;
	setOpening: Dispatch<SetStateAction<{ stage: string } | undefined>>;
	setConnection: Dispatch<SetStateAction<"live" | "reconnecting">>;
}

/** The state of the workspace the route names. The stream that keeps it current is `useStream`. */
export function useSession(route: Route): Session {
	const [state, setState] = useState<WireState | undefined>(undefined);
	/** A workspace that is opening, and the step it is on. Undefined means it is up (or failed,
	 * which is the failure strip's business). */
	const [opening, setOpening] = useState<{ stage: string } | undefined>(undefined);
	const [connection, setConnection] = useState<"live" | "reconnecting">("live");
	const [failure, setFailure] = useState<string | undefined>(undefined);
	const [ready, setReady] = useState(false);

	const reportError = useCallback((cause: unknown): void => {
		setFailure(cause instanceof Error ? cause.message : String(cause));
	}, []);

	const clearFailure = useCallback((): void => setFailure(undefined), []);

	const workspace = route.workspace === "" ? (state?.workspace.name ?? "") : route.workspace;
	const activeId =
		route.conversation === "" ? (state?.conversation?.id ?? "") : route.conversation;

	const activeIdRef = useRef<string | undefined>(undefined);
	useEffect(() => {
		activeIdRef.current = activeId;
	}, [activeId]);

	const reload = useCallback(async (): Promise<void> => {
		try {
			// A retry is a decision to wait: whatever an earlier poll was saying about the step is
			// not worth keeping on screen next to a request that is now in flight.
			setOpening(undefined);
			setState(await fetchState(workspace, route.conversation));
			setFailure(undefined);
		} catch (cause) {
			setFailure(cause instanceof Error ? cause.message : String(cause));
		}
	}, [workspace, route.conversation]);

	const act = useCallback(
		(request: Promise<unknown>, then?: () => void): void => {
			void request
				.then(() => {
					setFailure(undefined);
					then?.();
				})
				.catch(reportError);
		},
		[reportError],
	);

	return {
		slice: {
			ready,
			connection,
			failure,
			clearFailure,
			reload: () => {
				void reload();
			},
			opening,
			workerUp: state?.workerUp ?? false,
			workerError: state?.workerError,
			// A guest sees the agent, the model and the thinking with their values and cannot pick another,
			// and no explanatory sentence: the state carries the answer, so this is the one place that reads
			// it (ADR 0034 §4).
			can: toRights(state?.can),
			role: toRoomRole(state?.role),
			admin: state?.admin === true,
		},
		state,
		workspace,
		activeId,
		activeIdRef,
		reportError,
		clearFailure,
		act,
		reload,
		setState,
		setReady,
		setOpening,
		setConnection,
	};
}
