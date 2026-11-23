import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { saveDraft } from "./client";
import { shownDraft, type Draft, type RoomDraft } from "./draft";
import type { Session } from "./use-session";
import type { WireDraft, WireDraftAttachment } from "./wire";

/** How long a keystroke waits before the draft is written down. The words are on screen the
 * whole time: the wait is only so a sentence is not one POST and one state event per letter. */
const DRAFT_SETTLE_MS = 400;

/** The composer's unsent words, kept per room. */
export interface DraftSlice {
	/** The room in view and its unsent message: this machine's words in front of the
	 * workspace's copy, so a half-typed line waits in the room on every machine. */
	draft?: RoomDraft | undefined;
	/** Record the draft of the conversation in view. Batched; the newest words win. */
	setDraft: (text: string, attachments: WireDraftAttachment[]) => void;
}

export interface Drafts {
	slice: DraftSlice;
	/** Write every edit that has not landed. The stream calls it when it opens, which is when a
	 * draft saved while it was away can land; `keepalive` is for a page that is going away. */
	flushDrafts: (keepalive?: boolean) => void;
}

/** The draft of the room in view, written to the workspace a moment after typing stops. */
export function useDraft(session: Session): Drafts {
	const { state, workspace, activeId, activeIdRef } = session;
	/**
	 * The draft of the room in view. The workspace holds the words; this machine holds the two
	 * things it needs to be honest about them — the edit that has not landed yet, and the last
	 * value the server took from here, which is what tells a state event carrying this machine's
	 * own words from one carrying another machine's.
	 */
	const draftEdits = useRef(new Map<string, Draft>());
	const draftWritten = useRef(new Map<string, WireDraft>());
	/** A counter with no value of its own: the maps above are refs, because the flush reads and
	 * empties them without being re-made for every keystroke, so this is what re-renders the
	 * composer when one of them moves. */
	const [, setDraftMoved] = useState(0);
	const draftTimer = useRef<number | undefined>(undefined);

	const flushDrafts = useCallback(
		(keepalive = false): void => {
			if (draftTimer.current !== undefined) {
				window.clearTimeout(draftTimer.current);
				draftTimer.current = undefined;
			}
			for (const [room, draft] of [...draftEdits.current]) {
				void saveDraft(workspace, room, draft, keepalive)
					.then((stored) => {
						// Only the edit the server just took is cleared: the person may have typed more
						// while the save was in flight, and those words are still to come.
						if (draftEdits.current.get(room) === draft) draftEdits.current.delete(room);
						draftWritten.current.set(room, stored);
						setDraftMoved((moved) => moved + 1);
					})
					.catch(() => {
						// The words stay on screen and the next flush tries again. A draft is not worth
						// an error strip, and nothing is lost on this machine either way.
					});
			}
		},
		[workspace],
	);

	const recordDraft = useCallback(
		(room: string, draft: Draft): void => {
			draftEdits.current.set(room, draft);
			setDraftMoved((moved) => moved + 1);
			if (draftTimer.current !== undefined) return;
			draftTimer.current = window.setTimeout(() => {
				draftTimer.current = undefined;
				flushDrafts();
			}, DRAFT_SETTLE_MS);
		},
		[flushDrafts],
	);

	const setDraft = useCallback(
		(text: string, attachments: WireDraftAttachment[]): void => {
			const room = activeIdRef.current ?? "";
			if (room === "") return;
			recordDraft(room, { text, attachments });
		},
		[recordDraft, activeIdRef],
	);

	// The write that has to leave while the page does: a reload mid-sentence is the reload the
	// draft exists for, and the settle timer would not have fired by then.
	useEffect(() => {
		const leaving = (): void => {
			if (document.visibilityState === "hidden") flushDrafts(true);
		};
		const leavingPage = (): void => flushDrafts(true);
		document.addEventListener("visibilitychange", leaving);
		window.addEventListener("pagehide", leavingPage);
		return () => {
			document.removeEventListener("visibilitychange", leaving);
			window.removeEventListener("pagehide", leavingPage);
			// Leaving this workspace is the last moment its drafts can be named: a draft is saved
			// where its room lives.
			flushDrafts(true);
		};
	}, [flushDrafts]);

	const draftRoom = state?.conversation?.id ?? "";
	// While a switch lands, the state still describes the room just left; its draft is not the
	// one this composer should show.
	const draftServer = draftRoom === activeId ? state?.draft : undefined;
	const draftEdit = activeId === "" ? undefined : draftEdits.current.get(activeId);
	const draftWrittenHere = activeId === "" ? undefined : draftWritten.current.get(activeId);
	// The map entries are replaced, never changed in place, so they say when the draft moved.
	const draft = useMemo<RoomDraft | undefined>(
		() =>
			activeId === ""
				? undefined
				: {
						conversation: activeId,
						...shownDraft({
							edit: draftEdit,
							server: draftServer,
							written: draftWrittenHere,
						}),
					},
		[activeId, draftEdit, draftServer, draftWrittenHere],
	);

	return { slice: { draft, setDraft }, flushDrafts };
}
