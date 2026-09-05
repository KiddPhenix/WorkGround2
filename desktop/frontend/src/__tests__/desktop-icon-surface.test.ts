// Run: tsx src/__tests__/desktop-icon-surface.test.ts
import assert from "node:assert/strict";
import { createIconSurfaceCoordinator, createIconSurfaceLifecycle, DESKTOP_ICON_OVERLAY_BOUNDS, desktopIconLayoutBounds, type IconSurfaceBounds } from "../lib/desktopIconSurface";
import type { DesktopIconItem, DesktopIconSurfaceInput, DesktopIconSurfaceResult } from "../lib/bridge";

const response = (input: DesktopIconSurfaceInput): DesktopIconSurfaceResult => ({ revision: input.revision, width: input.width + input.envelope * 2, height: input.height + input.envelope * 2, x: 0, y: 0 });
const flush = () => new Promise<void>((resolve) => setImmediate(resolve));
const bounds = (width: number, height: number): IconSurfaceBounds => ({ width, height });
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
	pending.resolve({ revision: 0, width: 864, height: 664, x: 0, y: 0 });
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

async function testSurfaceOnlyGrows() {
	const calls: DesktopIconSurfaceInput[] = [];
	const applied: DesktopIconSurfaceResult[] = [];
	const coordinator = createIconSurfaceCoordinator({ apply: async (input) => { calls.push(input); return response(input); }, onApplied: (result) => applied.push(result) });
	await coordinator.prepare(bounds(800, 600));
	coordinator.settle(bounds(400, 300));
	await flush();
	assert.deepEqual(calls.map((call) => call.width), [800], "a smaller stable layout never shrinks the native surface");
	coordinator.settle(bounds(900, 650));
	await flush();
	assert.deepEqual(calls.map((call) => call.width), [800, 900]);
	coordinator.settle(bounds(700, 500));
	await flush();
	assert.deepEqual(calls.map((call) => call.width), [800, 900], "the maximum survives after transient content closes");
	assert.deepEqual(applied.map((result) => result.width), [864, 964], "every changed authoritative geometry is observable by hit-region sync");
}

async function testInitialNativeSurfaceDoesNotShrink() {
	const calls: DesktopIconSurfaceInput[] = [];
	const coordinator = createIconSurfaceCoordinator({
		apply: async (input) => { calls.push(input); return response(input); },
		initialBounds: bounds(1016, 656),
	});
	coordinator.settle(bounds(600, 500));
	assert.equal(await coordinator.prepare(bounds(800, 600)), true);
	assert.deepEqual(calls, [], "mounting content inside the initial native canvas does not resize it downward");
	assert.equal(await coordinator.prepare(bounds(1216, 836)), true);
	assert.deepEqual(calls.map((call) => [call.width, call.height]), [[1216, 836]]);
}

async function testConcurrentPrepareCoalescesSingleFlight() {
	const pending: ReturnType<typeof deferred>[] = [];
	let active = 0;
	let maxActive = 0;
	const coordinator = createIconSurfaceCoordinator({ apply: () => {
		active += 1;
		maxActive = Math.max(maxActive, active);
		const item = deferred();
		pending.push(item);
		return item.promise.finally(() => { active -= 1; });
	} });
	const old = coordinator.prepare(bounds(700, 550));
	const next = coordinator.prepare(bounds(900, 650));
	assert.equal(pending.length, 1, "a larger concurrent intent is merged behind the current native call");
	pending[0].resolve({ revision: 0, width: 764, height: 614, x: 0, y: 0 });
	assert.equal(await old, true, "the first prepare resolves from the first authoritative geometry");
	await flush();
	assert.equal(pending.length, 2, "the merged larger intent drains after the first call settles");
	pending[1].resolve({ revision: 0, width: 964, height: 714, x: 0, y: 0 });
	assert.equal(await next, true);
	assert.equal(maxActive, 1, "native surface mutation is strictly single-flight without request generations");
	assert.equal(coordinator.current()?.width, 964, "authoritative geometry can never roll back from an out-of-order response");
}

async function testActivityLifecycleRecreatesDisposedCoordinator() {
	const calls: DesktopIconSurfaceInput[] = [];
	const lifecycle = createIconSurfaceLifecycle({
		apply: async (input) => { calls.push(input); return response(input); },
	});
	assert.equal(await lifecycle.prepare(bounds(700, 550)), false, "a hidden Activity cannot mutate the native surface");
	lifecycle.activate();
	assert.equal(await lifecycle.prepare(bounds(700, 550)), true);
	assert.equal(lifecycle.current()?.width, 764);
	lifecycle.deactivate();
	assert.equal(lifecycle.current(), null, "hiding Activity releases the old native coordinator");
	lifecycle.activate();
	assert.equal(await lifecycle.prepare(bounds(700, 550)), true, "Activity reveal gets a live coordinator instead of the disposed ref");
	assert.equal(calls.length, 2, "each visible native lifetime independently converges from its authority");
	lifecycle.dispose();
}

function item(id: string, row: "top" | "bottom", status: DesktopIconItem["status"] = "idle"): DesktopIconItem {
	return { id, kind: "task", sourceId: id, title: id, status, unreadCount: 0, notifications: [], position: { row, zone: "running", order: 0 }, revision: id };
}
const narrow = desktopIconLayoutBounds([item("one", "bottom")], false);
const running = desktopIconLayoutBounds([item("one", "bottom", "running")], false);
assert.deepEqual(running, narrow, "status decoration inside the native minimum does not resize the surface");
const crowded = desktopIconLayoutBounds(Array.from({ length: 9 }, (_, index) => item(String(index), "bottom")), false);
assert.ok(crowded.width > narrow.width, "new icons expand once the stable minimum envelope is exceeded");
const zoomed = desktopIconLayoutBounds(Array.from({ length: 9 }, (_, index) => item(String(index), "bottom")), false, 1.5);
assert.ok(zoomed.width > 1016, "zoomed dense rows can grow beyond the former 1080px native cap");
assert.ok(DESKTOP_ICON_OVERLAY_BOUNDS.width >= 1200 && DESKTOP_ICON_OVERLAY_BOUNDS.height >= 800, "management popups reserve a larger surface before mounting");

await testPrepareGatesCommit();
await testFailureDoesNotCommit();
await testSurfaceOnlyGrows();
await testInitialNativeSurfaceDoesNotShrink();
await testConcurrentPrepareCoalescesSingleFlight();
await testActivityLifecycleRecreatesDisposedCoordinator();
// Identical geometry from different native entries carries distinct identity.
// A disposed coordinator cannot publish the old native reply into the new one.
{
	const oldReply = deferred();
	const calls: number[] = [];
	let oldPublished = false;
	const old = createIconSurfaceCoordinator({ revision: 1, apply: async input => { calls.push(input.revision); return oldReply.promise; }, onApplied: () => { oldPublished = true; } });
	const waiting = old.prepare(bounds(700, 550));
	old.dispose();
	const current = createIconSurfaceCoordinator({ revision: 3, apply: async input => { calls.push(input.revision); return response(input); } });
	await current.prepare(bounds(700, 550));
	oldReply.resolve({ revision: 1, width: 764, height: 614, x: 0, y: 0 });
	assert.equal(await waiting, false);
	await flush();
	assert.deepEqual(calls, [1, 3]);
	assert.equal(current.current()?.revision, 3);
	assert.equal(oldPublished, false);
}
process.stdout.write("desktop icon surface coordinator tests passed\n");
