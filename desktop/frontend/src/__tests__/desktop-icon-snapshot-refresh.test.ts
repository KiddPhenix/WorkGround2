import assert from "node:assert/strict";
import {
	createDesktopIconSnapshotRefresh,
	desktopIconEventWakesSnapshot,
	SNAPSHOT_EVENT_DEBOUNCE_MS,
	SNAPSHOT_RECOVERY_MS,
	subscribeDesktopIconSnapshotRefresh,
	type SnapshotTimerHost,
} from "../components/widget/desktopIconSnapshotRefresh";

class FakeTimers implements SnapshotTimerHost {
	private now = 0;
	private nextID = 1;
	private readonly timers = new Map<number, { at: number; fn: () => void }>();

	setTimeout(fn: () => void, delay: number): number {
		const id = this.nextID++;
		this.timers.set(id, { at: this.now + delay, fn });
		return id;
	}

	clearTimeout(id: number): void {
		this.timers.delete(id);
	}

	advance(ms: number): void {
		const target = this.now + ms;
		for (;;) {
			const next = [...this.timers.entries()]
				.filter(([, timer]) => timer.at <= target)
				.sort((left, right) => left[1].at - right[1].at || left[0] - right[0])[0];
			if (!next) break;
			const [id, timer] = next;
			this.timers.delete(id);
			this.now = timer.at;
			timer.fn();
		}
		this.now = target;
	}

	delays(): number[] {
		return [...this.timers.values()].map((timer) => timer.at - this.now).sort((a, b) => a - b);
	}
}

const flush = async () => { await Promise.resolve(); await Promise.resolve(); };

assert.deepEqual(
	["turn_started", "turn_done", "message", "tool_dispatch", "tool_result", "approval_request", "ask_request", "retrying", "compaction_started", "compaction_done"]
		.map((kind) => desktopIconEventWakesSnapshot(kind as Parameters<typeof desktopIconEventWakesSnapshot>[0])),
	Array(10).fill(true),
	"durable agent state boundaries wake the authoritative snapshot",
);
assert.deepEqual(
	["text", "reasoning", "tool_progress", "usage", "notice", "phase", "task_memory_updated"]
		.map((kind) => desktopIconEventWakesSnapshot(kind as Parameters<typeof desktopIconEventWakesSnapshot>[0])),
	Array(7).fill(false),
	"high-frequency events never trigger full snapshot scans",
);

// Idle widgets perform one initial authoritative read and then only a slow
// recovery read. The removed one-second scan cannot reappear unnoticed.
{
	const timers = new FakeTimers();
	let calls = 0;
	const refresh = createDesktopIconSnapshotRefresh(async () => { calls++; }, timers);
	refresh.start();
	await flush();
	assert.equal(calls, 1);
	assert.deepEqual(timers.delays(), [SNAPSHOT_RECOVERY_MS]);
	assert.equal(timers.delays().includes(1000), false, "idle refresh never schedules a one-second poll");
	timers.advance(SNAPSHOT_RECOVERY_MS - 1);
	assert.equal(calls, 1);
	timers.advance(1);
	await flush();
	assert.equal(calls, 2, "the low-frequency recovery poll repairs missed events");
	refresh.dispose();
}

// A burst of state notifications is only a wake-up hint and converges through
// one backend snapshot after the trailing debounce.
{
	const timers = new FakeTimers();
	let calls = 0;
	const refresh = createDesktopIconSnapshotRefresh(async () => { calls++; }, timers);
	refresh.start();
	await flush();
	refresh.wake();
	timers.advance(SNAPSHOT_EVENT_DEBOUNCE_MS / 2);
	refresh.wake();
	refresh.wake();
	timers.advance(SNAPSHOT_EVENT_DEBOUNCE_MS - 1);
	assert.equal(calls, 1);
	timers.advance(1);
	await flush();
	assert.equal(calls, 2, "burst events merge into one refresh");
	refresh.dispose();
}

// Events arriving during a slow request mark the source dirty. They cannot
// overlap the request, and one follow-up snapshot converges after it settles.
{
	const timers = new FakeTimers();
	let resolveFirst!: () => void;
	let calls = 0;
	let active = 0;
	let maxActive = 0;
	const first = new Promise<void>((resolve) => { resolveFirst = resolve; });
	const refresh = createDesktopIconSnapshotRefresh(async () => {
		calls++;
		active++;
		maxActive = Math.max(maxActive, active);
		if (calls === 1) await first;
		active--;
	}, timers);
	refresh.start();
	refresh.wake();
	refresh.wake();
	timers.advance(SNAPSHOT_EVENT_DEBOUNCE_MS * 2);
	assert.equal(calls, 1);
	resolveFirst();
	await flush();
	timers.advance(SNAPSHOT_EVENT_DEBOUNCE_MS);
	await flush();
	assert.equal(calls, 2);
	assert.equal(maxActive, 1, "snapshot refresh is strictly single-flight");
	refresh.dispose();
}

// Component teardown removes every event subscription and both timer classes.
{
	const timers = new FakeTimers();
	let calls = 0;
	let unsubscribed = 0;
	const listeners: Array<() => void> = [];
	const sources = Array.from({ length: 8 }, () => (wake: () => void) => {
		listeners.push(wake);
		return () => { unsubscribed++; };
	});
	const refresh = createDesktopIconSnapshotRefresh(async () => { calls++; }, timers);
	const unsubscribe = subscribeDesktopIconSnapshotRefresh(refresh, sources);
	refresh.start();
	await flush();
	listeners.forEach((wake) => wake());
	unsubscribe();
	refresh.dispose();
	assert.equal(unsubscribed, sources.length);
	assert.deepEqual(timers.delays(), [], "dispose clears debounce and recovery timers");
	timers.advance(SNAPSHOT_RECOVERY_MS);
	await flush();
	assert.equal(calls, 1, "disposed refresh cannot be resurrected by late timers");
}

console.log("desktop icon snapshot refresh tests passed");
