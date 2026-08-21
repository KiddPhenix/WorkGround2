// Run: tsx src/__tests__/desktop-icon-surface.test.ts
import assert from "node:assert/strict";
import { createIconSurfaceCoordinator, desktopIconLayoutBounds, type IconSurfaceBounds } from "../lib/desktopIconSurface";
import type { DesktopIconItem, DesktopIconSurfaceInput, DesktopIconSurfaceResult } from "../lib/bridge";

const response = (input: DesktopIconSurfaceInput): DesktopIconSurfaceResult => ({ width: input.width + input.envelope * 2, height: input.height + input.envelope * 2, x: 0, y: 0, generation: input.generation });
const flush = () => new Promise<void>((resolve) => setImmediate(resolve));
const bounds = (width: number, height: number): IconSurfaceBounds => ({ width, height });
function scheduler() {
	const timers: (() => void)[] = [];
	return { schedule(fn: () => void) { timers.push(fn); return () => { const i = timers.indexOf(fn); if (i >= 0) timers.splice(i, 1); }; }, fire() { while (timers.length) timers.shift()!(); }, pending: () => timers.length };
}
function deferred() {
	let resolve!: (value: DesktopIconSurfaceResult) => void;
	let reject!: (cause: unknown) => void;
	const promise = new Promise<DesktopIconSurfaceResult>((res, rej) => { resolve = res; reject = rej; });
	return { promise, resolve, reject };
}

async function testPrepareGatesCommit() {
	const pending = deferred();
	let committed = false;
	const coordinator = createIconSurfaceCoordinator({ apply: () => pending.promise });
	const prepare = coordinator.prepare(bounds(800, 600)).then((ready) => { if (ready) committed = true; });
	await flush();
	assert.equal(committed, false, "new content stays hidden before native resize resolves");
	pending.resolve({ width: 864, height: 664, x: 0, y: 0, generation: 1 });
	await prepare;
	assert.equal(committed, true, "native confirmation releases the render gate");
	assert.equal(coordinator.current()?.width, 864, "current geometry is the backend-clamped result");
}

async function testFailureDoesNotCommit() {
	let error = "";
	const coordinator = createIconSurfaceCoordinator({ apply: async () => { throw new Error("resize failed"); }, onError: (cause) => { error = String(cause); } });
	assert.equal(await coordinator.prepare(bounds(800, 600)), false);
	assert.match(error, /resize failed/);
}

async function testShrinkIsDelayedAndCancelled() {
	const calls: DesktopIconSurfaceInput[] = [];
	const timer = scheduler();
	const coordinator = createIconSurfaceCoordinator({ apply: async (input) => { calls.push(input); return response(input); }, schedule: timer.schedule });
	await coordinator.prepare(bounds(800, 600));
	coordinator.settle(bounds(400, 300));
	assert.equal(timer.pending(), 1);
	coordinator.settle(bounds(900, 650));
	assert.equal(timer.pending(), 0, "a new icon growth cancels stale shrink");
	await flush();
	assert.deepEqual(calls.map((call) => call.width), [800, 900]);
}

async function testOverlayBlocksShrink() {
	const calls: DesktopIconSurfaceInput[] = [];
	const timer = scheduler();
	const coordinator = createIconSurfaceCoordinator({ apply: async (input) => { calls.push(input); return response(input); }, schedule: timer.schedule });
	await coordinator.prepare(bounds(800, 600));
	coordinator.setOverlay(true);
	coordinator.settle(bounds(400, 300));
	assert.equal(timer.pending(), 0);
	coordinator.setOverlay(false);
	assert.equal(timer.pending(), 1);
	timer.fire(); await flush();
	assert.deepEqual(calls.map((call) => call.width), [800, 400]);
}

async function testSupersededPrepareCannotCommit() {
	const pending: ReturnType<typeof deferred>[] = [];
	const coordinator = createIconSurfaceCoordinator({ apply: () => { const item = deferred(); pending.push(item); return item.promise; } });
	const old = coordinator.prepare(bounds(700, 550));
	const next = coordinator.prepare(bounds(900, 650));
	pending[0].resolve({ width: 764, height: 614, x: 0, y: 0, generation: 1 });
	assert.equal(await old, false);
	pending[1].resolve({ width: 964, height: 714, x: 0, y: 0, generation: 2 });
	assert.equal(await next, true);
}

function item(id: string, row: "top" | "bottom", status: DesktopIconItem["status"] = "idle"): DesktopIconItem {
	return { id, kind: "task", sourceId: id, title: id, status, unreadCount: 0, notifications: [], position: { row, zone: "running", order: 0 }, revision: id };
}
const narrow = desktopIconLayoutBounds([item("one", "bottom")], false);
const running = desktopIconLayoutBounds([item("one", "bottom", "running")], false);
assert.deepEqual(running, narrow, "status decoration inside the native minimum does not resize the surface");
const crowded = desktopIconLayoutBounds(Array.from({ length: 9 }, (_, index) => item(String(index), "bottom")), false);
assert.ok(crowded.width > narrow.width, "new icons expand once the stable minimum envelope is exceeded");

await testPrepareGatesCommit();
await testFailureDoesNotCommit();
await testShrinkIsDelayedAndCancelled();
await testOverlayBlocksShrink();
await testSupersededPrepareCannotCommit();
process.stdout.write("desktop icon surface coordinator tests passed\n");
