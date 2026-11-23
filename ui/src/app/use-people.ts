import { useCallback, useEffect, useState } from "react";
import {
	addPerson as addPersonRequest,
	fetchPeople,
	fetchProviderCatalog,
	restorePerson as restorePersonRequest,
	revokePerson as revokePersonRequest,
	setPersonCanCreate as setPersonCanCreateRequest,
	setPersonModels as setPersonModelsRequest,
	type ProviderCatalogEntry,
} from "./client";
import { toPeople } from "./map";
import type { Route } from "./use-navigation";
import type { PersonSummary } from "./view-models";
import type { WirePerson } from "./wire";

/** The book of people as the admin's page reads it: the records, and what a list may be narrowed to. */
export interface PeopleBook {
	people: PersonSummary[];
	/** Every provider and model this install has, never the narrowed list: a list is widened from all. */
	catalog: ProviderCatalogEntry[];
}

/** The People page: the install's book of who may sign in, and what each of them may run. */
export interface PeopleSlice {
	/** The book of people, for the admin's page; undefined until that page is opened. */
	people?: PeopleBook | undefined;
	/** The first read of the book is in flight. */
	peopleLoading: boolean;
	/** The last thing the book refused, in its own words: shown on the page it was asked from. */
	peopleFailure?: string | undefined;
	/** Name somebody. They are invited until they sign in. */
	addPerson: (input: { email: string; name: string }) => void;
	/** Take access away; their name stays in every room. */
	revokePerson: (email: string) => void;
	/** Put a revoked person back. */
	restorePerson: (email: string) => void;
	setPersonCanCreate: (email: string, canCreate: boolean) => void;
	/** What they may choose; an empty list is every model. */
	setPersonModels: (email: string, models: string[]) => void;
}

/** What a refusal said, as a page shows it: the gateway writes sentences, and a status code is not one. */
function messageOf(error: unknown): string {
	return error instanceof Error ? error.message : String(error);
}

/**
 * The book of people. It is the admin's file, not the room's, so it does not ride the state stream: it is
 * read when the page that shows it is opened, and every write answers the whole document, so a write's
 * answer is the next render and no second read is needed (people.go, ADR 0032 §3).
 */
export function usePeople(route: Route, admin: boolean): PeopleSlice {
	const [peopleBook, setPeopleBook] = useState<PeopleBook | undefined>(undefined);
	const [peopleLoading, setPeopleLoading] = useState(false);
	const [peopleFailure, setPeopleFailure] = useState<string | undefined>(undefined);
	useEffect(() => {
		// The book is read as soon as the settings open, not when the People page is picked: the Getting started
		// checklist on the first page has to know whether anybody has been invited, and a page that lies about
		// that is worse than one that does not mention it.
		if (route.screen !== "settings" || !admin || peopleBook !== undefined) return;
		let cancelled = false;
		setPeopleLoading(true);
		setPeopleFailure(undefined);
		void Promise.all([fetchPeople(), fetchProviderCatalog()])
			.then(
				([people, catalog]) => {
					if (cancelled) return;
					setPeopleBook({ people: toPeople(people), catalog });
				},
				(cause: unknown) => {
					// A page that cannot read the book says so where the book would be, in the gateway's
					// words: the People tab is the admin's, and a 403 here is the install saying no.
					if (!cancelled) setPeopleFailure(messageOf(cause));
				},
			)
			.finally(() => {
				if (!cancelled) setPeopleLoading(false);
			});
		return () => {
			cancelled = true;
		};
	}, [route.screen, route.settingsTab, admin, peopleBook]);

	/** One write on the book: its answer is the whole document, and a refusal is the page's own sentence. */
	const writeBook = useCallback((request: Promise<WirePerson[]>): void => {
		void request.then(
			(people) => {
				setPeopleBook((current) =>
					current === undefined ? current : { ...current, people: toPeople(people) },
				);
				setPeopleFailure(undefined);
			},
			(cause: unknown) => setPeopleFailure(messageOf(cause)),
		);
	}, []);
	const addPerson = useCallback(
		(input: { email: string; name: string }) => writeBook(addPersonRequest(input)),
		[writeBook],
	);
	const revokePerson = useCallback(
		(email: string) => writeBook(revokePersonRequest(email)),
		[writeBook],
	);
	const restorePerson = useCallback(
		(email: string) => writeBook(restorePersonRequest(email)),
		[writeBook],
	);
	const setPersonCanCreate = useCallback(
		(email: string, canCreate: boolean) =>
			writeBook(setPersonCanCreateRequest(email, canCreate)),
		[writeBook],
	);
	const setPersonModels = useCallback(
		(email: string, models: string[]) => writeBook(setPersonModelsRequest(email, models)),
		[writeBook],
	);

	return {
		people: peopleBook,
		peopleLoading,
		peopleFailure,
		addPerson,
		revokePerson,
		restorePerson,
		setPersonCanCreate,
		setPersonModels,
	};
}
