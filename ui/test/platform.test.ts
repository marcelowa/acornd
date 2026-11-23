import { afterAll, afterEach, beforeEach, describe, expect, test } from "bun:test";
import { JSDOM } from "jsdom";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * `usePlatform` as the console runs it: one component, a gateway that answers from the wire
 * fixtures, and a stream the test drives frame by frame. The hook is a facade over several
 * domain hooks, and the frames are where those hooks meet — a view frame lands in the transcript
 * and claims a mark, an auth frame lands in a login started elsewhere, a draft is flushed when the
 * stream opens — so these tests follow the frames rather than any one module.
 */

const browser = new JSDOM("<!doctype html><html><body><div id=root></div></body></html>", {
	url: "http://acornd.test/c/home/7",
});
const globals = globalThis as Record<string, unknown>;
globals["window"] = browser.window;
globals["document"] = browser.window.document;
globals["navigator"] = browser.window.navigator;
globals["IS_REACT_ACT_ENVIRONMENT"] = true;
// jsdom has no media queries; the theme follows the system through this one.
browser.window.matchMedia = ((query: string) => ({
	matches: false,
	media: query,
	addEventListener: () => undefined,
	removeEventListener: () => undefined,
})) as unknown as typeof browser.window.matchMedia;

type Listener = (event: unknown) => void;

/** The browser's EventSource, reduced to what `openStream` uses, with a hand to push frames. */
class FakeSource {
	static instances: FakeSource[] = [];
	readonly url: string;
	readyState = 0;
	closed = false;
	listeners = new Map<string, Listener[]>();
	constructor(url: string) {
		this.url = url;
		FakeSource.instances.push(this);
	}
	addEventListener(type: string, listener: Listener): void {
		this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
	}
	emit(type: string, data?: unknown): void {
		const event = data === undefined ? {} : { data: JSON.stringify(data) };
		for (const listener of this.listeners.get(type) ?? []) listener(event);
	}
	close(): void {
		this.closed = true;
		this.readyState = 2;
	}
}
globals["EventSource"] = FakeSource;

const fixtures = join(dirname(fileURLToPath(import.meta.url)), "../../fixtures/wire");
// A trust boundary: the fixtures are our own checked-in files.
const stateFixture = (await Bun.file(join(fixtures, "state.json")).json()) as Record<
	string,
	unknown
>;

interface Call {
	method: string;
	path: string;
	body: unknown;
}
let calls: Call[] = [];
/** A route the test holds open, so a reply can be made to arrive after a frame. */
let held = new Map<string, (response: Response) => void>();
const holdNext = new Set<string>();

function json(value: unknown, status = 200): Response {
	return new Response(JSON.stringify(value), {
		status,
		headers: { "content-type": "application/json" },
	});
}

function answer(method: string, path: string, body: unknown): Response {
	if (path === "/api/workspaces/home/status") return json({ status: "ready" });
	if (path.startsWith("/api/state")) return json(stateFixture);
	if (path === "/api/workspaces/home/commands") {
		return json({ commands: [{ name: "compact", description: "Compact" }] });
	}
	if (method === "POST" && path.endsWith("/draft")) return json(body);
	if (path === "/api/workspaces/home/providers/login") return json({ authId: "l1" });
	return new Response("not here", { status: 404 });
}

globals["fetch"] = (input: string, init?: RequestInit): Promise<Response> => {
	const url = new URL(input, "http://acornd.test");
	const method = init?.method ?? "GET";
	const body: unknown = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
	calls.push({ method, path: url.pathname + url.search, body });
	const response = answer(method, url.pathname + url.search, body);
	if (holdNext.has(url.pathname)) {
		holdNext.delete(url.pathname);
		return new Promise((resolve) => held.set(url.pathname, () => resolve(response)));
	}
	return Promise.resolve(response);
};

const { act, createElement, memo } = await import("react");
const { createRoot } = await import("react-dom/client");
const { usePlatform } = await import("../src/app/use-platform");
type Platform = ReturnType<typeof usePlatform>;

let platform: Platform | undefined;
let renders = 0;
/** How often a consumer that reads only the transcript rendered. */
let transcriptRenders = 0;
const TranscriptReader = memo(function TranscriptReader(_props: {
	entries: Platform["entries"];
}): null {
	transcriptRenders += 1;
	return null;
});
function Probe() {
	platform = usePlatform();
	renders += 1;
	return createElement(TranscriptReader, { entries: platform.entries });
}

/** The fields whose identity changed: a memoised consumer of any other field would not render. */
function changed(before: Platform, after: Platform): string[] {
	const old = new Map<string, unknown>(Object.entries(before));
	return Object.entries(after)
		.filter(([key, value]) => !Object.is(old.get(key), value))
		.map(([key]) => key)
		.sort();
}

function now(): Platform {
	if (platform === undefined) throw new Error("the probe has not rendered");
	return platform;
}

/** Let fetches resolve and React commit what they set. */
async function settle(ms = 0): Promise<void> {
	await act(async () => {
		await new Promise((resolve) => setTimeout(resolve, ms));
	});
}

function source(): FakeSource {
	const open = FakeSource.instances.filter((one) => !one.closed);
	const last = open.at(-1);
	if (last === undefined || open.length !== 1) {
		throw new Error(`expected one open stream, found ${open.length}`);
	}
	return last;
}

async function frame(value: unknown): Promise<void> {
	await act(async () => {
		source().emit("frame", value);
		await Promise.resolve();
	});
}

let root: ReturnType<typeof createRoot> | undefined;

beforeEach(async () => {
	calls = [];
	held = new Map();
	holdNext.clear();
	FakeSource.instances = [];
	platform = undefined;
	renders = 0;
	transcriptRenders = 0;
	browser.window.localStorage.clear();
	browser.window.history.replaceState(null, "", "/c/home/7");
	const container = browser.window.document.getElementById("root");
	await act(async () => {
		root = createRoot(container as unknown as Element);
		root.render(createElement(Probe));
		await Promise.resolve();
	});
	await settle();
});

afterEach(async () => {
	await act(async () => {
		root?.unmount();
		await Promise.resolve();
	});
});

afterAll(() => {
	browser.window.close();
});

describe("the platform facade", () => {
	test("loads the workspace and follows the conversation in the address", async () => {
		expect(now().ready).toBe(true);
		expect(now().failure).toBeUndefined();
		expect(now().screen).toBe("conversation");
		expect(now().workspaces.map((one) => one.name)).toEqual(["home"]);
		expect(now().commands.map((one) => one.name)).toEqual(["compact"]);
		expect(now().active?.id).toBe("7");
		expect(source().url).toBe("/api/stream?workspace=home&conversation=7");
	});

	test("a view frame lands in the transcript, and a steer claims the message it becomes", async () => {
		await act(async () => {
			source().emit("open");
			await Promise.resolve();
		});
		await frame({
			type: "view",
			value: {
				docs: { "pi.inbox": { items: [{ id: "i1", mode: "steer", content: "hold on" }] } },
			},
		});
		await frame({
			type: "view",
			value: {
				entries: [{ id: 20, kind: "acornd.message", model: [{ content: "hold on" }] }],
			},
		});
		const entry = now().entries.find((one) => one.id === "20");
		expect(entry?.kind).toBe("user");
		expect(entry !== undefined && entry.kind === "user" ? entry.action : undefined).toBe(
			"steer",
		);
	});

	test("selecting another conversation clears the transcript and reopens the stream on it", async () => {
		await frame({
			type: "view",
			value: { entries: [{ id: 20, kind: "acornd.message", model: [{ content: "old" }] }] },
		});
		expect(now().entries.length).toBeGreaterThan(0);
		const before = source();
		await act(async () => {
			now().select("8");
			await Promise.resolve();
		});
		expect(before.closed).toBe(true);
		expect(source().url).toBe("/api/stream?workspace=home&conversation=8");
		expect(now().entries).toEqual([]);
		expect(browser.window.location.pathname).toBe("/c/home/8");
		// A frame from the stream that was closed must not land on the new conversation.
		await act(async () => {
			before.emit("frame", {
				type: "view",
				value: {
					entries: [{ id: 21, kind: "acornd.message", model: [{ content: "late" }] }],
				},
			});
			await Promise.resolve();
		});
		expect(now().entries).toEqual([]);
	});

	test("an auth frame that beats the login's reply still reaches the dialog", async () => {
		holdNext.add("/api/workspaces/home/providers/login");
		const provider = now().providers[0];
		if (provider === undefined) throw new Error("the fixture has no provider");
		let started: Promise<void> | undefined;
		await act(async () => {
			started = now().startLogin(provider, "oauth");
			await Promise.resolve();
		});
		await frame({
			type: "auth",
			authId: "l1",
			event: { kind: "progress", message: "waiting" },
		});
		expect(now().authFlow).toBeUndefined();
		await act(async () => {
			held.get("/api/workspaces/home/providers/login")?.(json({ authId: "l1" }));
			await started;
		});
		expect(now().authFlow?.authId).toBe("l1");
		expect(now().authFlow?.progress).toBe("waiting");
	});

	test("a draft is written to the room in view once typing settles", async () => {
		await act(async () => {
			now().setDraft("half a thought", []);
			await Promise.resolve();
		});
		expect(now().draft?.text).toBe("half a thought");
		expect(calls.some((call) => call.path.endsWith("/draft"))).toBe(false);
		await settle(450);
		const saved = calls.find((call) => call.path.endsWith("/draft"));
		expect(saved?.path).toBe("/api/workspaces/home/conversations/7/draft");
		expect(saved?.body).toEqual({ text: "half a thought", attachments: [] });
	});

	test("the settings screen has its own address, and Back returns to the conversation", async () => {
		await act(async () => {
			now().showSettingsTab("people");
			await Promise.resolve();
		});
		expect(now().screen).toBe("settings");
		expect(now().settingsTab).toBe("people");
		expect(browser.window.location.pathname).toBe("/settings/people");
		await act(async () => {
			browser.window.history.back();
			await new Promise((resolve) => setTimeout(resolve, 10));
		});
		expect(now().screen).toBe("conversation");
	});

	test("the workspaces layer closes on Back without moving the address", async () => {
		await act(async () => {
			now().openWorkspaces();
			await Promise.resolve();
		});
		expect(now().workspacesOpen).toBe(true);
		await act(async () => {
			browser.window.history.back();
			await new Promise((resolve) => setTimeout(resolve, 10));
		});
		expect(now().workspacesOpen).toBe(false);
		expect(browser.window.location.pathname).toBe("/c/home/7");
	});
});

describe("one slice changing leaves the other slices' fields as they were", () => {
	async function change(press: () => void): Promise<string[]> {
		await settle();
		const before = now();
		await act(async () => {
			press();
			await Promise.resolve();
		});
		return changed(before, now());
	}

	test("the theme", async () => {
		expect(await change(() => now().setThemeChoice("dark"))).toEqual(["theme", "themeChoice"]);
	});

	test("the rail's fold", async () => {
		expect(await change(() => now().toggleRail())).toEqual(["railCollapsed"]);
	});

	test("the workspaces layer", async () => {
		expect(await change(() => now().openWorkspaces())).toEqual(["workspacesOpen"]);
	});

	test("the draft", async () => {
		expect(await change(() => now().setDraft("a few words", []))).toEqual(["draft"]);
	});

	test("a view frame", async () => {
		const fields = await change(() =>
			source().emit("frame", {
				type: "view",
				value: {
					entries: [{ id: 20, kind: "acornd.message", model: [{ content: "hi" }] }],
				},
			}),
		);
		expect(fields).toEqual(["context", "entries"]);
	});

	test("a consumer of the transcript does not render for the theme", async () => {
		await settle();
		const facade = renders;
		const transcript = transcriptRenders;
		await act(async () => {
			now().setThemeChoice("dark");
			await Promise.resolve();
		});
		expect(renders).toBeGreaterThan(facade);
		expect(transcriptRenders).toBe(transcript);
	});
});
