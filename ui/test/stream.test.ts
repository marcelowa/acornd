import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { openStream, type StreamHandlers } from "../src/app/client";
import type { WireState } from "../src/app/wire";

/**
 * The stream client's job is to stay open. The browser reconnects a network blip and gives up for
 * good on anything else — a 502 from a gateway that is restarting is "anything else" — so these
 * tests are about the loop we run instead, and about the page catching up when it comes back.
 */

type Listener = (event: unknown) => void;

class FakeSource {
	static instances: FakeSource[] = [];
	static closedForGood = true;
	readonly url: string;
	readyState = 0;
	closed = false;
	listeners = new Map<string, Listener[]>();
	constructor(url: string) {
		this.url = url;
		FakeSource.instances.push(this);
	}

	addEventListener(type: string, listener: Listener): void {
		const list = this.listeners.get(type) ?? [];
		list.push(listener);
		this.listeners.set(type, list);
	}

	emit(type: string, event: unknown = {}): void {
		for (const listener of this.listeners.get(type) ?? []) listener(event);
	}

	/** The browser's own retry: the socket is down but may come back. */
	blip(): void {
		this.readyState = 0;
		this.emit("error");
	}

	/** What a restart looks like from here: closed, and not by us. */
	die(): void {
		this.readyState = FakeSource.closedForGood ? 2 : 0;
		this.emit("error");
	}

	close(): void {
		this.closed = true;
		this.readyState = 2;
	}
}

const timing = { retryMin: 2, retryMax: 8, quiet: 40, poll: 5 };

function handlers() {
	const seen = {
		opens: 0,
		reconnecting: 0,
		states: [] as WireState[],
		views: 0,
	};
	const value: StreamHandlers = {
		onOpen: () => {
			seen.opens += 1;
		},
		onState: (state) => {
			seen.states.push(state);
		},
		onView: () => {
			seen.views += 1;
		},
		onReconnecting: () => {
			seen.reconnecting += 1;
		},
	};
	return { seen, value };
}

const originalEventSource = globalThis.EventSource;
const originalFetch = globalThis.fetch;

beforeEach(() => {
	FakeSource.instances = [];
	globalThis.EventSource = FakeSource as unknown as typeof EventSource;
	// A state the client will accept: the workspace it asked for, and the lists a state carries.
	const state = {
		workspace: { name: "personal", path: "/tmp/personal" },
		workspaces: [],
		conversations: [],
	};
	globalThis.fetch = (async () =>
		new Response(JSON.stringify(state), {
			status: 200,
			headers: { "content-type": "application/json" },
		})) as unknown as typeof fetch;
});

afterEach(() => {
	globalThis.EventSource = originalEventSource;
	globalThis.fetch = originalFetch;
});

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

describe("the stream", () => {
	test("replaces a socket the browser has given up on", async () => {
		const { seen, value } = handlers();
		const handle = openStream("personal", "1", value, timing);
		FakeSource.instances[0]?.emit("open");
		FakeSource.instances[0]?.die();
		await wait(30);
		expect(FakeSource.instances.length).toBeGreaterThan(1);
		expect(seen.reconnecting).toBeGreaterThan(0);
		handle.close();
	});

	test("catches up on the state it missed, so a reconnected page is not stale", async () => {
		const { seen, value } = handlers();
		const handle = openStream("personal", "1", value, timing);
		FakeSource.instances[0]?.emit("open");
		await wait(10);
		expect(seen.states.length).toBe(1);
		expect(seen.states[0]?.workspace?.name).toBe("personal");
		handle.close();
	});

	test("declares a silent socket dead, even though it is still open", async () => {
		const { seen, value } = handlers();
		const handle = openStream("personal", "1", value, timing);
		FakeSource.instances[0]?.emit("open");
		await wait(70);
		expect(seen.reconnecting).toBeGreaterThan(0);
		expect(FakeSource.instances.length).toBeGreaterThan(1);
		handle.close();
	});

	test("a ping keeps a quiet connection alive", async () => {
		const { seen, value } = handlers();
		const handle = openStream("personal", "1", value, timing);
		FakeSource.instances[0]?.emit("open");
		for (let i = 0; i < 6; i += 1) {
			await wait(10);
			FakeSource.instances[0]?.emit("ping");
		}
		expect(seen.reconnecting).toBe(0);
		handle.close();
	});

	test("closing stops the loop for good", async () => {
		const { value } = handlers();
		const handle = openStream("personal", "1", value, timing);
		FakeSource.instances[0]?.emit("open");
		const count = FakeSource.instances.length;
		handle.close();
		FakeSource.instances[0]?.die();
		await wait(40);
		expect(FakeSource.instances.length).toBe(count);
	});
});
