import { useCallback, useEffect, useState } from "react";

const RAIL_WIDTH_KEY = "acornd.ui.railWidth";
const RAIL_COLLAPSED_KEY = "acornd.ui.railCollapsed";
const THEME_KEY = "acornd.ui.theme";

/** The choices this browser keeps across a reload: the rail's width and fold, and the theme. */
export interface PreferencesSlice {
	railWidth?: number | undefined;
	/** The desktop column: false keeps it open, which is the default. */
	railCollapsed: boolean;
	toggleRail: () => void;
	setRailWidth: (width: number) => void;
	/** The effective theme, which is the system's until a choice says otherwise. */
	theme: "light" | "dark";
	/** What was chosen: light, dark, or following the system. */
	themeChoice: "light" | "dark" | "system";
	setThemeChoice: (choice: "light" | "dark" | "system") => void;
}

/** Preferences are ours and local; a corrupt value is a fresh start, not a crash. */
export function read<T>(key: string, fallback: T): T {
	try {
		const raw = window.localStorage.getItem(key);
		return raw === null ? fallback : (JSON.parse(raw) as T);
	} catch {
		return fallback;
	}
}

/**
 * The theme someone chose. `system` is the default and a choice like any other: picking it
 * back up means following the system again, which is not the same as having chosen light.
 */
function storedTheme(): "light" | "dark" | "system" {
	try {
		const raw = window.localStorage.getItem(THEME_KEY);
		const value: unknown = raw === null ? null : JSON.parse(raw);
		return value === "light" || value === "dark" || value === "system" ? value : "system";
	} catch {
		return "system";
	}
}

function systemTheme(): "light" | "dark" {
	return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

/** The preferences that survive a reload. They read nothing from the gateway. */
export function usePreferences(): PreferencesSlice {
	const [railWidth, setRailWidthState] = useState<number | undefined>(
		() => read<number | null>(RAIL_WIDTH_KEY, null) ?? undefined,
	);
	const [railCollapsed, setRailCollapsed] = useState<boolean>(() =>
		read(RAIL_COLLAPSED_KEY, false),
	);
	const [system, setSystem] = useState<"light" | "dark">(systemTheme);
	const [choice, setChoice] = useState<"light" | "dark" | "system">(storedTheme);
	const theme = choice === "system" ? system : choice;

	// The system decides until someone says otherwise, and still moves the theme while no
	// choice has been made.
	useEffect(() => {
		const query = window.matchMedia("(prefers-color-scheme: dark)");
		const follow = (): void => setSystem(query.matches ? "dark" : "light");
		query.addEventListener("change", follow);
		return () => query.removeEventListener("change", follow);
	}, []);

	useEffect(() => {
		document.documentElement.classList.toggle("dark", theme === "dark");
		window.localStorage.setItem(THEME_KEY, JSON.stringify(choice));
	}, [theme, choice]);

	const setRailWidth = useCallback((width: number) => {
		setRailWidthState(width);
		window.localStorage.setItem(RAIL_WIDTH_KEY, JSON.stringify(width));
	}, []);

	const toggleRail = useCallback(() => {
		setRailCollapsed((current) => {
			const next = !current;
			window.localStorage.setItem(RAIL_COLLAPSED_KEY, JSON.stringify(next));
			return next;
		});
	}, []);

	return {
		railWidth,
		railCollapsed,
		toggleRail,
		setRailWidth,
		theme,
		themeChoice: choice,
		setThemeChoice: setChoice,
	};
}
